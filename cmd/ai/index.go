package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// The session index (~/.config/ai/sessions.db) is one table of every
// profile's conversations, whatever the provider keeps them in. The recent
// lists and pickers read it instead of re-reading each provider's store on
// every refresh, and a source that is slow to decode (a multi-megabyte
// transcript, later Antigravity's protobuf) is decoded once, when it changes.
//
// It is a cache, never the record. Every row is rebuilt from the provider's
// own files, so deleting sessions.db costs one slower refresh and nothing
// else; when it cannot be opened at all, the readers fall back to scanning the
// sources directly. See doc/session-store.md.

const sessionIndexSchema = `
CREATE TABLE IF NOT EXISTS session (
	profile     TEXT NOT NULL,
	provider    TEXT NOT NULL,
	id          TEXT NOT NULL,
	title       TEXT NOT NULL,
	folder      TEXT NOT NULL,
	created     INTEGER NOT NULL,
	updated     INTEGER NOT NULL,
	headless    INTEGER NOT NULL DEFAULT 0,
	source      TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	PRIMARY KEY (profile, id)
);
CREATE INDEX IF NOT EXISTS session_recent ON session (profile, headless, updated DESC);
CREATE INDEX IF NOT EXISTS session_source ON session (profile, source);
CREATE TABLE IF NOT EXISTS turn (
	profile   TEXT NOT NULL,
	id        TEXT NOT NULL,
	seq       INTEGER NOT NULL,
	from_user INTEGER NOT NULL,
	text      TEXT NOT NULL,
	PRIMARY KEY (profile, id, seq)
);
CREATE TABLE IF NOT EXISTS turn_source (
	profile     TEXT NOT NULL,
	id          TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	PRIMARY KEY (profile, id)
);
`

// sessionIndexMu serialises this process's use of the index. The TUI reads the
// selected profile and the all-profiles union from separate goroutines, and one
// writer at a time is cheaper than SQLITE_BUSY retries between them. Another
// ai process is kept in step by WAL and the busy timeout instead.
var sessionIndexMu sync.Mutex

func sessionIndexPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, "sessions.db"), nil
}

// openSessionIndex opens (creating on first use) the index. It is opened per
// call rather than held: a refresh is rare next to the cost of a stale handle
// when the config directory moves under it, which is every test.
func openSessionIndex() (*sql.DB, error) {
	path, err := sessionIndexPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{`PRAGMA busy_timeout = 5000`, `PRAGMA journal_mode = WAL`, sessionIndexSchema} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// indexedRecentSessions answers recentSessions from the index after bringing
// the profile's rows up to date. Interactive and headless sessions are limited
// separately: a night of wave workers must not push every conversation a person
// had out of a list that then hides the workers.
func indexedRecentSessions(profile Profile, limit int) ([]recordedSession, bool) {
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return nil, false
	}
	defer db.Close()
	if err := refreshSessionIndex(db, profile); err != nil {
		return nil, false
	}
	var records []recordedSession
	for _, headless := range []int{0, 1} {
		rows, err := db.Query(`SELECT id, title, folder, created, updated, headless FROM session
			WHERE profile = ? AND headless = ? ORDER BY updated DESC LIMIT ?`, profile.Name, headless, limit)
		if err != nil {
			return nil, false
		}
		for rows.Next() {
			var record recordedSession
			var created, updated int64
			var isHeadless int
			if err := rows.Scan(&record.session.id, &record.session.title, &record.folder, &created, &updated, &isHeadless); err != nil {
				continue
			}
			record.when, record.lastActive = indexTime(created), indexTime(updated)
			record.headless = isHeadless == 1
			record.profile = profile.Name
			records = append(records, record)
		}
		rows.Close()
	}
	if profile.Provider == "opencode" {
		records = unionLiveOpenCodeSessions(profile, records)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].activity().After(records[j].activity()) })
	return limitEachKind(records, limit), true
}

// limitEachKind keeps the newest limit interactive and the newest limit
// headless records, in their existing order. It runs after live sessions join,
// which can be newer than anything the index returned.
func limitEachKind(records []recordedSession, limit int) []recordedSession {
	kept := records[:0:0]
	counts := map[bool]int{}
	for _, record := range records {
		if counts[record.headless] < limit {
			counts[record.headless]++
			kept = append(kept, record)
		}
	}
	return kept
}

// unionLiveOpenCodeSessions adds sessions that so far exist only in a running
// instance's private store. They are not indexed: the archive is the record,
// and they reach it when the instance merges.
func unionLiveOpenCodeSessions(profile Profile, records []recordedSession) []recordedSession {
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		seen[record.session.id] = true
	}
	for _, dbPath := range liveOpenCodeStores(profile) {
		live, err := queryOpenCodeSessions(dbPath, 0)
		if err != nil {
			continue
		}
		for _, record := range live {
			if !seen[record.session.id] {
				seen[record.session.id] = true
				record.profile = profile.Name
				records = append(records, record)
			}
		}
	}
	return records
}

// refreshSessionIndex brings one profile's rows in line with its sources:
// changed sources are re-read, unchanged ones cost a stat, and rows whose
// source is gone are dropped.
func refreshSessionIndex(db *sql.DB, profile Profile) error {
	switch profile.Provider {
	case "claude":
		return refreshTranscriptRows(db, profile, claudeTranscripts(profile), func(path string) (recordedSession, bool) {
			record, ok := readClaudeTranscript(path)
			record.headless = claudeTranscriptHeadless(path)
			return record, ok
		})
	case "codex":
		root, err := profileRoot()
		if err != nil {
			return err
		}
		paths := codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions"))
		return refreshTranscriptRows(db, profile, paths, func(path string) (recordedSession, bool) {
			record, ok := readCodexRollout(path, "")
			record.headless = codexRolloutHeadless(path)
			return record, ok
		})
	case "opencode":
		return refreshOpenCodeRows(db, profile)
	case "antigravity":
		sessions, err := antigravitySessions(profile)
		if err != nil {
			return err
		}
		// The start time is protobuf in the conversation's first step: read
		// once, when the conversation is (re)indexed, never on a refresh.
		return refreshStoreRows(db, profile, antigravitySummariesPath(profile), sessions, func(record *recordedSession) {
			record.when = antigravityStartTime(profile, record.session.id)
		})
	}
	return nil
}

// refreshTranscriptRows indexes a provider that keeps one file per
// conversation. The fingerprint is the file's size and modification time: both
// providers only ever append, so either moving means something was said.
func refreshTranscriptRows(db *sql.DB, profile Profile, paths []string, read func(string) (recordedSession, bool)) error {
	known := map[string]string{}
	rows, err := db.Query(`SELECT source, fingerprint FROM session WHERE profile = ?`, profile.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var source, fingerprint string
		if rows.Scan(&source, &fingerprint) == nil {
			known[source] = fingerprint
		}
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	present := make(map[string]bool, len(paths))
	for _, path := range paths {
		present[path] = true
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		fingerprint := strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		if known[path] == fingerprint {
			continue
		}
		record, ok := read(path)
		if !ok || record.session.id == "" {
			continue
		}
		if err := upsertIndexedSession(tx, profile, record, path, fingerprint); err != nil {
			return err
		}
	}
	for source := range known {
		if !present[source] {
			if err := deleteIndexedSource(tx, profile.Name, source); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// refreshOpenCodeRows indexes the profile's archive. Every session there has
// its own time_updated, which is the fingerprint.
func refreshOpenCodeRows(db *sql.DB, profile Profile) error {
	root, err := profileRoot()
	if err != nil {
		return err
	}
	archive := filepath.Join(root, profile.Name, "data", "opencode", "opencode.db")
	var sessions []recordedSession
	if fileExists(archive) {
		if sessions, err = queryOpenCodeSessions(archive, 0); err != nil {
			return err
		}
	}
	return refreshStoreRows(db, profile, archive, sessions, nil)
}

// refreshStoreRows indexes a provider whose conversations are rows in a store
// rather than files: the fingerprint is each session's own last activity and
// title. fillIn, when given, completes a record that is about to be written,
// for fields that cost a decode the listing did not pay for.
func refreshStoreRows(db *sql.DB, profile Profile, source string, sessions []recordedSession, fillIn func(*recordedSession)) error {
	known := map[string]string{}
	rows, err := db.Query(`SELECT id, fingerprint FROM session WHERE profile = ?`, profile.Name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, fingerprint string
		if rows.Scan(&id, &fingerprint) == nil {
			known[id] = fingerprint
		}
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	present := make(map[string]bool, len(sessions))
	for _, record := range sessions {
		present[record.session.id] = true
		fingerprint := fmt.Sprintf("%d:%s", record.activity().UnixNano(), record.session.title)
		if known[record.session.id] == fingerprint {
			continue
		}
		if fillIn != nil {
			fillIn(&record)
		}
		if err := upsertIndexedSession(tx, profile, record, source, fingerprint); err != nil {
			return err
		}
	}
	for id := range known {
		if !present[id] {
			if err := deleteIndexedSession(tx, profile.Name, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func upsertIndexedSession(tx *sql.Tx, profile Profile, record recordedSession, source, fingerprint string) error {
	headless := 0
	if record.headless {
		headless = 1
	}
	_, err := tx.Exec(`INSERT INTO session (profile, provider, id, title, folder, created, updated, headless, source, fingerprint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile, id) DO UPDATE SET title = excluded.title, folder = excluded.folder,
			created = excluded.created, updated = excluded.updated, headless = excluded.headless,
			source = excluded.source, fingerprint = excluded.fingerprint`,
		profile.Name, profile.Provider, record.session.id, record.session.title, record.folder,
		unixNano(record.when), unixNano(record.lastActive), headless, source, fingerprint)
	return err
}

func deleteIndexedSource(tx *sql.Tx, profile, source string) error {
	rows, err := tx.Query(`SELECT id FROM session WHERE profile = ? AND source = ?`, profile, source)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := deleteIndexedSession(tx, profile, id); err != nil {
			return err
		}
	}
	return nil
}

func deleteIndexedSession(tx *sql.Tx, profile, id string) error {
	for _, stmt := range []string{
		`DELETE FROM session WHERE profile = ? AND id = ?`,
		`DELETE FROM turn WHERE profile = ? AND id = ?`,
		`DELETE FROM turn_source WHERE profile = ? AND id = ?`,
	} {
		if _, err := tx.Exec(stmt, profile, id); err != nil {
			return err
		}
	}
	return nil
}

// cachedSessionTurns returns a conversation's turns if they were materialized
// from the source as it is now. Turns are cached only when something asked for
// them (the preview, a handoff), never on refresh: reading every turn of every
// transcript up front would put the whole 2 GB of them in the first refresh.
func cachedSessionTurns(profileName, id string) ([]handoffMessage, bool) {
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return nil, false
	}
	defer db.Close()
	var current, cached string
	if db.QueryRow(`SELECT s.fingerprint, t.fingerprint FROM session s JOIN turn_source t
		ON t.profile = s.profile AND t.id = s.id WHERE s.profile = ? AND s.id = ?`, profileName, id).Scan(&current, &cached) != nil ||
		current != cached {
		return nil, false
	}
	rows, err := db.Query(`SELECT from_user, text FROM turn WHERE profile = ? AND id = ? ORDER BY seq`, profileName, id)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	messages := []handoffMessage{}
	for rows.Next() {
		var fromUser int
		var message handoffMessage
		if rows.Scan(&fromUser, &message.text) == nil {
			message.fromUser = fromUser == 1
			messages = append(messages, message)
		}
	}
	return messages, rows.Err() == nil
}

// cacheSessionTurns stores a conversation's turns against the fingerprint its
// index row has now. A session the index does not know (one only in a live
// instance) is not cached: its source is still being written.
func cacheSessionTurns(profileName, id string, messages []handoffMessage) {
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return
	}
	defer db.Close()
	var fingerprint string
	if db.QueryRow(`SELECT fingerprint FROM session WHERE profile = ? AND id = ?`, profileName, id).Scan(&fingerprint) != nil {
		return
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM turn WHERE profile = ? AND id = ?`, profileName, id); err != nil {
		return
	}
	for seq, message := range messages {
		fromUser := 0
		if message.fromUser {
			fromUser = 1
		}
		if _, err := tx.Exec(`INSERT INTO turn (profile, id, seq, from_user, text) VALUES (?, ?, ?, ?, ?)`,
			profileName, id, seq, fromUser, message.text); err != nil {
			return
		}
	}
	if _, err := tx.Exec(`INSERT INTO turn_source (profile, id, fingerprint) VALUES (?, ?, ?)
		ON CONFLICT (profile, id) DO UPDATE SET fingerprint = excluded.fingerprint`, profileName, id, fingerprint); err != nil {
		return
	}
	_ = tx.Commit()
}

// claudeTranscriptHeadless reports whether a Claude transcript was written by
// `claude -p` (entrypoint "sdk-cli") rather than an interactive session
// ("cli"). The field is on every entry, so the first few lines settle it.
func claudeTranscriptHeadless(path string) bool {
	entrypoint := firstJSONField(path, "entrypoint", 20)
	return entrypoint != "" && entrypoint != "cli"
}

// codexRolloutHeadless reports whether a Codex rollout came from `codex exec`,
// whose session_meta names an exec originator. Unverified on the workstation,
// which has no Codex rollouts; the interactive CLI's originator does not
// contain "exec".
func codexRolloutHeadless(path string) bool {
	return bytes.Contains([]byte(firstJSONField(path, "originator", 3)), []byte("exec"))
}

// firstJSONField finds a string field in the first lines of a JSONL file. The
// lines can be megabytes long, so it reads them whole with a Reader rather
// than a Scanner and its token cap.
func firstJSONField(path, field string, lines int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for range lines {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte(`"`+field+`"`)) {
			if value := findJSONString(line, field); value != "" {
				return value
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

// findJSONString returns the first string value named field anywhere in one
// JSON document, at any depth.
func findJSONString(line []byte, field string) string {
	var value any
	if json.Unmarshal(line, &value) != nil {
		return ""
	}
	var walk func(any) string
	walk = func(node any) string {
		switch typed := node.(type) {
		case map[string]any:
			if text, ok := typed[field].(string); ok {
				return text
			}
			for _, child := range typed {
				if found := walk(child); found != "" {
					return found
				}
			}
		case []any:
			for _, child := range typed {
				if found := walk(child); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return walk(value)
}

func unixNano(when time.Time) int64 {
	if when.IsZero() {
		return 0
	}
	return when.UnixNano()
}

func indexTime(nanos int64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}
