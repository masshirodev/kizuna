package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Concurrent OpenCode instances each run on a private copy of the profile's
// data and state directories, because upstream OpenCode keeps a single
// single-writer SQLite store (opencode.db) that concurrent processes corrupt
// or lock each other out of. Claude and Codex can share one home because
// their transcripts are append-only per-session files; OpenCode cannot, so it
// gets isolation plus a merge back into the profile on the way out.
//
// Layout per launch (beneath the profile directory):
//
//	instances/run-XXX/
//	  .active.lock        liveness, as for every other instance
//	  instance.json       launch folder + start time, as for the others
//	  .seeded             seed receipt (JSON); absence means the seed never
//	                      finished and the directory holds no user data
//	  data/opencode/      XDG_DATA_HOME: auth.json + opencode.db seeded copies
//	  state/opencode/     XDG_STATE_HOME: model.json + prompt-history copies
//
// XDG_CONFIG_HOME keeps pointing at the profile's config tree, which is
// read-mostly during a run. Merging is idempotent (INSERT OR IGNORE over UUID
// keys), so a retry after a crash can never duplicate a session.

// usesIsolatedDataDir reports whether a provider runs concurrent instances on
// private data/state copies rather than on the shared profile directories.
// Only OpenCode qualifies: Codex and Claude share safely, Antigravity has no
// config-home override to isolate with, and DeepSeek has no isolated state at
// all (it runs the opencode binary against the machine's ordinary config).
func usesIsolatedDataDir(profile Profile) bool {
	return profile.Provider == "opencode"
}

const (
	// isolatedSeedFile is the seed receipt. A directory carrying it ran (or
	// is running) a real session and must be merged, never deleted outright.
	// A stale directory without it is a failed seed and holds nothing to keep.
	isolatedSeedFile = ".seeded"
	// mergeLockFile serialises merges (and nothing else) per profile, so two
	// instances exiting at once cannot interleave writes to the profile DB.
	mergeLockFile = ".merge.lock"
	// mergingSuffix marks a directory a merger has claimed. The rename is
	// atomic, so the loser of a double merge backs off and the idempotent
	// merge makes the winner safe to retry.
	mergingSuffix = ".merging"
)

// isolatedSeedInfo is what the merger needs to tell "this instance changed
// it" apart from "this is still the seed". Without the seed hashes, an
// instance that never refreshed its auth would merge its stale copy over a
// newer one another instance already merged back.
type isolatedSeedInfo struct {
	SeededAt    string `json:"seeded_at"`
	AuthSHA256  string `json:"auth_sha256"`
	ModelSHA256 string `json:"model_sha256"`
	// HistorySize is the byte size of the profile's prompt-history.jsonl at
	// seed time; everything past it in the instance copy is this run's.
	HistorySize int64 `json:"history_size"`
}

// isIsolatedInstanceDir reports whether lockDir holds a seeded OpenCode store.
// Plain Codex/Claude instance directories and exclusive lock dirs never do.
func isIsolatedInstanceDir(lockDir string) bool {
	if lockDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(lockDir, isolatedSeedFile))
	return err == nil
}

// acquireIsolatedInstance creates an instance directory, seeds it with the
// profile's data/state copies, and returns a cleanup that merges the store
// back before removing the directory. The returned note is "" when there was
// nothing worth reporting.
func acquireIsolatedInstance(profile Profile, workdir string) (string, func() string, error) {
	instanceDir, err := createBareInstanceDir(workdir)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() string { return retireIsolatedInstance(workdir, profile, instanceDir) }
	// Created before the exclusive-lock check, like every other instance, so
	// an exclusive caller racing this launch sees it and vice versa.
	active, err := activeLock(filepath.Join(workdir, ".active.lock"), true)
	if err != nil || active {
		_ = os.RemoveAll(instanceDir)
		if err != nil {
			return "", nil, err
		}
		return "", nil, profileBusyError(workdir)
	}
	if err := seedIsolatedInstance(workdir, instanceDir); err != nil {
		_ = os.RemoveAll(instanceDir)
		return "", nil, err
	}
	return instanceDir, cleanup, nil
}

// seedIsolatedInstance copies the profile's data/state into the instance. The
// seed receipt is written last: anything that fails before it leaves a
// directory the merger may delete without a second thought.
func seedIsolatedInstance(workdir, instanceDir string) error {
	dataDir := filepath.Join(instanceDir, "data", "opencode")
	stateDir := filepath.Join(instanceDir, "state", "opencode")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	info := isolatedSeedInfo{SeededAt: time.Now().UTC().Format(time.RFC3339)}
	if src := filepath.Join(workdir, "data", "opencode", "opencode.db"); fileExists(src) {
		if err := snapshotSQLiteDB(src, filepath.Join(dataDir, "opencode.db")); err != nil {
			return fmt.Errorf("seed session store: %w", err)
		}
	}
	if auth, err := readFileIfExists(filepath.Join(workdir, "data", "opencode", "auth.json")); err != nil {
		return err
	} else if auth != nil {
		if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), auth, 0600); err != nil {
			return err
		}
		info.AuthSHA256 = sha256Hex(auth)
	}
	if model, err := readFileIfExists(filepath.Join(workdir, "state", "opencode", "model.json")); err != nil {
		return err
	} else if model != nil {
		if err := os.WriteFile(filepath.Join(stateDir, "model.json"), model, 0600); err != nil {
			return err
		}
		info.ModelSHA256 = sha256Hex(model)
	}
	historySrc := filepath.Join(workdir, "state", "opencode", "prompt-history.jsonl")
	if history, err := readFileIfExists(historySrc); err != nil {
		return err
	} else if history != nil {
		if err := os.WriteFile(filepath.Join(stateDir, "prompt-history.jsonl"), history, 0600); err != nil {
			return err
		}
		info.HistorySize = int64(len(history))
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(instanceDir, isolatedSeedFile), append(encoded, '\n'), 0600)
}

// snapshotSQLiteDB copies a SQLite database through the engine rather than
// the filesystem, so the copy is transactionally consistent even if another
// ai process is merging into the source at that moment. VACUUM INTO also
// folds any WAL content into the copy, which a file copy would leave behind
// in a -wal file nobody copied.
func snapshotSQLiteDB(src, dst string) error {
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout = 10000`); err != nil {
		return err
	}
	_, err = db.Exec(`VACUUM INTO '` + strings.ReplaceAll(dst, "'", "''") + `'`)
	return err
}

// retireIsolatedInstance merges an instance store back into the profile and
// removes the instance directory. It claims the directory with an atomic
// rename first, so a concurrent merger (a kill racing a natural exit, or two
// stray scans) backs off instead of merging twice; the merge itself is
// idempotent anyway. Directories without a seed receipt are failed seeds and
// are removed without merging.
func retireIsolatedInstance(workdir string, profile Profile, instanceDir string) string {
	if !isIsolatedInstanceDir(instanceDir) {
		_ = os.RemoveAll(instanceDir)
		return ""
	}
	claimed := fmt.Sprintf("%s%s-%d", instanceDir, mergingSuffix, os.Getpid())
	if err := os.Rename(instanceDir, claimed); err != nil {
		return ""
	}
	note, err := withProfileMergeLockResult(workdir, func() (string, error) {
		return mergeIsolatedStore(workdir, profile, claimed)
	})
	if err != nil {
		_ = os.WriteFile(filepath.Join(claimed, "merge-error.txt"),
			[]byte(time.Now().UTC().Format(time.RFC3339)+" "+err.Error()+"\n"), 0600)
		// Back to its plain name so the next stray scan retries it. The merge
		// is idempotent, so whatever already landed stays landed exactly once.
		_ = os.Rename(claimed, instanceDir)
		return fmt.Sprintf("merge failed (%s); store kept for retry", err)
	}
	_ = os.RemoveAll(claimed)
	return note
}

// mergeIsolatedStore folds one claimed instance store into the profile. Files
// merge only when this instance actually changed them past their seed state;
// the database merge is additive by row id.
func mergeIsolatedStore(workdir string, profile Profile, instanceDir string) (string, error) {
	seed, err := readIsolatedSeed(instanceDir)
	if err != nil {
		return "", err
	}
	var notes []string
	sessions, warnings, err := mergeInstanceDatabase(
		filepath.Join(workdir, "data", "opencode", "opencode.db"),
		filepath.Join(instanceDir, "data", "opencode", "opencode.db"),
	)
	if err != nil {
		return "", err
	}
	if sessions > 0 {
		notes = append(notes, fmt.Sprintf("merged %s from %s", plural(sessions, "session"), profile.Name))
	}
	if note := mergeInstanceFileIfChanged(
		filepath.Join(workdir, "data", "opencode", "auth.json"),
		filepath.Join(instanceDir, "data", "opencode", "auth.json"),
		seed.AuthSHA256, true); note != "" {
		notes = append(notes, "auth refreshed")
	}
	// The model pick is a preference, not history: last writer wins quietly.
	mergeInstanceFileIfChanged(
		filepath.Join(workdir, "state", "opencode", "model.json"),
		filepath.Join(instanceDir, "state", "opencode", "model.json"),
		seed.ModelSHA256, true)
	if warning := mergeInstanceHistory(workdir, instanceDir, seed.HistorySize); warning != "" {
		warnings = append(warnings, warning)
	}
	carryInstanceLogs(workdir, instanceDir)
	if len(warnings) > 0 {
		notes = append(notes, strings.Join(warnings, "; "))
	}
	return strings.Join(notes, "; "), nil
}

// opencodeMergeSkippedTables are never merged back. The account pointer and
// secrets would replay stale OAuth rotations over the profile's live tokens
// (the credential rows themselves merge time-guarded; see below);
// migration bookkeeping belongs to whichever opencode opens the DB next; the
// event stream is consumed coordinator state, not history.
var opencodeMergeSkippedTables = map[string]bool{
	"account_state": true,
	"migration":     true, "data_migration": true, "event": true, "event_sequence": true,
}

// opencodeAuthTables merge with a recency guard instead of blindly: the row
// with the newest time_updated wins, so an instance seeded before another one
// refreshed cannot regress the profile's tokens on exit.
var opencodeAuthTables = map[string]bool{
	"account": true, "control_account": true, "credential": true,
}

// mergeInstanceDatabase folds the instance session store into the profile's.
// New rows arrive by id; rows the profile already has are left alone, except
// auth rows, which compare time_updated. A missing profile database means the
// profile never ran, so the instance store is adopted wholesale.
func mergeInstanceDatabase(profileDB, instanceDB string) (int, []string, error) {
	if _, err := os.Stat(instanceDB); err != nil {
		return 0, nil, nil
	}
	if _, err := os.Stat(profileDB); errors.Is(err, os.ErrNotExist) {
		return adoptInstanceDatabase(profileDB, instanceDB)
	}
	db, err := sql.Open("sqlite", profileDB)
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	// One connection for the whole merge: ATTACH is per-connection, and a
	// pooled query landing on another connection would see no "incoming".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 15000`); err != nil {
		return 0, nil, err
	}
	if _, err := db.Exec(`ATTACH DATABASE '` + strings.ReplaceAll(instanceDB, "'", "''") + `' AS incoming`); err != nil {
		return 0, nil, err
	}
	defer db.Exec(`DETACH DATABASE incoming`) //nolint:errcheck
	tables, err := attachedTableNames(db, "incoming")
	if err != nil {
		return 0, nil, err
	}
	// Counted before the write transaction opens: with the pool pinned to one
	// connection for the ATTACH, anything touching the pool past Begin would
	// wait on the transaction's own connection forever. Reported only when
	// the session table itself merges cleanly below; a skipped table must
	// not be announced.
	sessions, sessionMergeable := countNewSessionsIfMergeable(db, tables)
	tx, err := db.Begin()
	if err != nil {
		return 0, nil, err
	}
	rolledBack := false
	defer func() {
		if !rolledBack {
			_ = tx.Rollback()
		}
	}()
	var warnings []string
	sessionMerged := sessionMergeable
	for _, table := range tables {
		if opencodeMergeSkippedTables[table] {
			continue
		}
		mainCols, err := tableColumns(tx, "main", table)
		if err != nil {
			continue // table the profile DB does not have; nothing to merge into
		}
		incomingCols, err := tableColumns(tx, "incoming", table)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped %s (unreadable)", table))
			if table == "session" {
				sessionMerged = false
			}
			continue
		}
		if !equalStrings(mainCols.names, incomingCols.names) {
			warnings = append(warnings, fmt.Sprintf("skipped %s (schema drift)", table))
			if table == "session" {
				sessionMerged = false
			}
			continue
		}
		var execErr error
		if opencodeAuthTables[table] {
			execErr = mergeAuthTable(tx, table, mainCols)
		} else {
			execErr = mergeAdditiveTable(tx, table, mainCols.names)
		}
		if execErr != nil {
			warnings = append(warnings, fmt.Sprintf("skipped %s (%s)", table, execErr))
			if table == "session" {
				sessionMerged = false
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	rolledBack = true
	if !sessionMerged {
		sessions = 0
	}
	return sessions, warnings, nil
}

// adoptInstanceDatabase checkpoints the instance store and copies it into
// place as the profile database. Only used when the profile never ran, so
// there is nothing to conflict with.
func adoptInstanceDatabase(profileDB, instanceDB string) (int, []string, error) {
	db, err := sql.Open("sqlite", instanceDB)
	if err != nil {
		return 0, nil, err
	}
	_, checkpointErr := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	closeErr := db.Close()
	if checkpointErr != nil {
		return 0, nil, checkpointErr
	}
	if closeErr != nil {
		return 0, nil, closeErr
	}
	if err := os.MkdirAll(filepath.Dir(profileDB), 0700); err != nil {
		return 0, nil, err
	}
	if err := copyFileBytes(instanceDB, profileDB, 0600); err != nil {
		return 0, nil, err
	}
	return countSessionsIn(profileDB)
}

// countSessionsIn reports how many sessions a store holds, for the merge
// note. A store without a session table (or one that cannot be read) counts
// as empty rather than failing the merge around it.
func countSessionsIn(dbPath string) (int, []string, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, nil, nil
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session`).Scan(&count); err != nil {
		return 0, nil, nil
	}
	return count, nil, nil
}

// mergeAdditiveTable inserts rows the profile lacks. Colliding ids mean
// "already merged" (a retry, or a row that predates the seed), and OR IGNORE
// keeps the retry idempotent. Edits to pre-existing rows are deliberately not
// carried: the merge is a union of sessions, not a sync.
func mergeAdditiveTable(tx *sql.Tx, table string, columns []string) error {
	quoted := quoteIdentifiers(columns)
	_, err := tx.Exec(`INSERT OR IGNORE INTO main.` + quoteIdentifier(table) +
		` (` + strings.Join(quoted, ", ") + `) SELECT ` + strings.Join(quoted, ", ") +
		` FROM incoming.` + quoteIdentifier(table))
	return err
}

// mergeAuthTable carries newer credential rows across. A strict greater-than
// on time_updated means clock ties keep the profile's copy rather than
// flipping a coin between two refreshes.
func mergeAuthTable(tx *sql.Tx, table string, columns tableColumnsResult) error {
	var pk []string
	hasUpdated := false
	for index, name := range columns.names {
		if columns.pk[index] {
			pk = append(pk, name)
		}
		if name == "time_updated" {
			hasUpdated = true
		}
	}
	if len(pk) == 0 || !hasUpdated {
		return errors.New("no primary key or time_updated to order by")
	}
	quoted := quoteIdentifiers(columns.names)
	// Two statements rather than one UPSERT: INSERT .. SELECT .. FROM ..
	// ON CONFLICT does not parse (the ON binds to the FROM as a join
	// constraint), so newer rows update first and missing rows insert after.
	assignments := make([]string, 0, len(columns.names))
	for _, name := range columns.names {
		skip := false
		for _, key := range pk {
			if key == name {
				skip = true
				break
			}
		}
		if !skip {
			assignments = append(assignments, quoteIdentifier(name)+`=incoming_row.`+quoteIdentifier(name))
		}
	}
	if len(assignments) == 0 {
		return errors.New("nothing but the key to merge")
	}
	join := make([]string, 0, len(pk))
	for _, key := range pk {
		join = append(join, quoteIdentifier(table)+`.`+quoteIdentifier(key)+`=incoming_row.`+quoteIdentifier(key))
	}
	if _, err := tx.Exec(`UPDATE ` + quoteIdentifier(table) + ` SET ` + strings.Join(assignments, ", ") +
		` FROM incoming.` + quoteIdentifier(table) + ` AS incoming_row WHERE ` +
		strings.Join(join, " AND ") + ` AND incoming_row."time_updated" > ` + quoteIdentifier(table) + `."time_updated"`); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO main.` + quoteIdentifier(table) +
		` (` + strings.Join(quoted, ", ") + `) SELECT ` + strings.Join(quoted, ", ") +
		` FROM incoming.` + quoteIdentifier(table))
	return err
}

type tableColumnsResult struct {
	names []string
	pk    []bool
}

// sqlQuerier is what table inspection needs; *sql.DB and *sql.Tx both
// satisfy it, so the pre-merge checks can run outside the write transaction.
type sqlQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func tableColumns(db sqlQuerier, schema, table string) (tableColumnsResult, error) {
	rows, err := db.Query(`PRAGMA ` + schema + `.table_info(` + quoteIdentifier(table) + `)`)
	if err != nil {
		return tableColumnsResult{}, err
	}
	defer rows.Close()
	var result tableColumnsResult
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, isPK int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &isPK); err != nil {
			return tableColumnsResult{}, err
		}
		result.names = append(result.names, name)
		result.pk = append(result.pk, isPK > 0)
	}
	if err := rows.Err(); err != nil {
		return tableColumnsResult{}, err
	}
	if len(result.names) == 0 {
		return tableColumnsResult{}, errors.New("no such table")
	}
	return result, nil
}

func attachedTableNames(db *sql.DB, schema string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM ` + schema + `.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(tables)
	return tables, nil
}

func tableExists(db sqlQuerier, schema, table string) bool {
	var one int
	err := db.QueryRow(`SELECT 1 FROM ` + schema + `.sqlite_master WHERE type='table' AND name=` + quoteString(table)).Scan(&one)
	return err == nil
}

func hasTable(tables []string, name string) bool {
	for _, table := range tables {
		if table == name {
			return true
		}
	}
	return false
}

// countNewSessionsIfMergeable counts incoming sessions absent from the
// profile store, or reports the session table unmergeable (missing or
// drifted) so the caller announces nothing it did not do.
func countNewSessionsIfMergeable(db *sql.DB, tables []string) (int, bool) {
	if !hasTable(tables, "session") || !tableExists(db, "main", "session") {
		return 0, false
	}
	mainCols, err := tableColumns(db, "main", "session")
	if err != nil {
		return 0, false
	}
	incomingCols, err := tableColumns(db, "incoming", "session")
	if err != nil || !equalStrings(mainCols.names, incomingCols.names) {
		return 0, false
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM incoming.session AS s WHERE NOT EXISTS
		(SELECT 1 FROM main.session AS m WHERE m.id = s.id)`).Scan(&count); err != nil {
		return 0, false
	}
	return count, true
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteIdentifiers(names []string) []string {
	quoted := make([]string, len(names))
	for index, name := range names {
		quoted[index] = quoteIdentifier(name)
	}
	return quoted
}

func quoteString(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// mergeInstanceFileIfChanged carries a file back only when this instance
// changed it past its seed hash. backup decides whether the profile's copy is
// kept beside the replacement; auth.json is worth a backup, a model pick is
// not.
func mergeInstanceFileIfChanged(profilePath, instancePath, seedSHA string, backup bool) string {
	current, err := readFileIfExists(instancePath)
	if err != nil || current == nil {
		return ""
	}
	if seedSHA != "" && sha256Hex(current) == seedSHA {
		return "" // untouched by this instance; the profile's newer copy (if any) stands
	}
	profile, err := readFileIfExists(profilePath)
	if err != nil {
		return ""
	}
	if profile != nil && bytes.Equal(profile, current) {
		return "" // already current (a retry, or nothing actually changed)
	}
	if backup && profile != nil {
		backupPath := fmt.Sprintf("%s.%d.bak", profilePath, time.Now().Unix())
		_ = os.WriteFile(backupPath, profile, 0600)
	}
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		return ""
	}
	if err := os.WriteFile(profilePath, current, 0600); err != nil {
		return ""
	}
	return "changed"
}

// mergeInstanceHistory appends the prompt-history lines this run added. The
// suffix check keeps a retried merge from duplicating what already landed.
func mergeInstanceHistory(workdir, instanceDir string, seedSize int64) string {
	profilePath := filepath.Join(workdir, "state", "opencode", "prompt-history.jsonl")
	instance, err := readFileIfExists(filepath.Join(instanceDir, "state", "opencode", "prompt-history.jsonl"))
	if err != nil || instance == nil || int64(len(instance)) <= seedSize {
		return ""
	}
	tail := instance[seedSize:]
	profile, err := readFileIfExists(profilePath)
	if err != nil {
		return "prompt history kept"
	}
	if profile != nil && bytes.HasSuffix(profile, tail) {
		return ""
	}
	if err := os.MkdirAll(filepath.Dir(profilePath), 0700); err != nil {
		return "prompt history kept"
	}
	handle, err := os.OpenFile(profilePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return "prompt history kept"
	}
	_, writeErr := handle.Write(tail)
	closeErr := handle.Close()
	if writeErr != nil || closeErr != nil {
		return "prompt history kept"
	}
	return ""
}

// carryInstanceLogs moves the instance's opencode logs under the profile log
// directory with an instance prefix, so a merged-away run stays debuggable.
func carryInstanceLogs(workdir, instanceDir string) {
	entries, err := os.ReadDir(filepath.Join(instanceDir, "data", "opencode", "log"))
	if err != nil || len(entries) == 0 {
		return
	}
	prefix := filepath.Base(strings.TrimSuffix(instanceDir, mergingSuffix))
	target := filepath.Join(workdir, "data", "opencode", "log")
	if err := os.MkdirAll(target, 0700); err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		_ = os.Rename(
			filepath.Join(instanceDir, "data", "opencode", "log", entry.Name()),
			filepath.Join(target, prefix+"-"+entry.Name()))
	}
}

func readIsolatedSeed(instanceDir string) (isolatedSeedInfo, error) {
	var info isolatedSeedInfo
	data, err := os.ReadFile(filepath.Join(instanceDir, isolatedSeedFile))
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func readFileIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func copyFileBytes(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// withProfileMergeLockResult runs fn under the profile merge lock, reclaiming
// a stale lock the way every other lock here is reclaimed.
func withProfileMergeLockResult(workdir string, fn func() (string, error)) (string, error) {
	unlock, err := acquireNamedLock(filepath.Join(workdir, mergeLockFile))
	if err != nil {
		return "", err
	}
	defer unlock()
	return fn()
}

// acquireNamedLock is acquireProcessLock for an explicit lock path. The merge
// lock lives beside the profile's active lock rather than inside a directory
// of its own.
func acquireNamedLock(lockPath string) (func(), error) {
	for attempt := 0; attempt < 3; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				_ = file.Close()
				_ = os.Remove(lockPath)
				return nil, err
			}
			if err := file.Close(); err != nil {
				_ = os.Remove(lockPath)
				return nil, err
			}
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		active, inspectErr := profileLockIsActive(lockPath)
		if errors.Is(inspectErr, os.ErrNotExist) {
			continue
		}
		if inspectErr != nil || active {
			return nil, profileBusyError(filepath.Dir(lockPath))
		}
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, profileBusyError(filepath.Dir(lockPath))
}

// reclaimStrayIsolatedInstances merges OpenCode instance stores whose
// processes are gone: the power-off, term-killed, and SIGKILLed launches that
// never ran their exit merge. Live instances are left alone, and directories
// without a seed receipt are failed seeds holding no user data. It returns one
// human-readable note per merged profile, for the TUI log or the CLI output.
func reclaimStrayIsolatedInstances(cfg Config) []string {
	var notes []string
	root, err := profileRoot()
	if err != nil {
		return nil
	}
	for _, profile := range cfg.Profiles {
		if !usesIsolatedDataDir(profile) {
			continue
		}
		workdir := filepath.Join(root, profile.Name)
		entries, err := os.ReadDir(filepath.Join(workdir, instancesDirectory))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".creating-") {
				continue
			}
			dir := filepath.Join(workdir, instancesDirectory, entry.Name())
			stale, err := isolatedInstanceDirIsStale(dir)
			if err != nil || !stale {
				continue
			}
			if !isIsolatedInstanceDir(dir) {
				_ = os.RemoveAll(dir)
				continue
			}
			if note := retireIsolatedInstance(workdir, profile, dir); note != "" {
				notes = append(notes, profile.Name+": "+note)
			}
		}
	}
	return notes
}

// isolatedInstanceDirIsStale reports whether no process recorded in the
// directory's lock is still alive. A missing lock means the launch died before
// recording anything, which is stale by definition.
func isolatedInstanceDirIsStale(dir string) (bool, error) {
	active, err := activeLock(filepath.Join(dir, ".active.lock"), false)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return !active, err
}

// profileOfInstanceDir resolves an instance directory back to its profile.
// lockDir is <root>/<profile>/instances/<run>, so the profile is two levels
// up; anything else shaped is not an instance directory.
func profileOfInstanceDir(lockDir string) (string, Profile, bool) {
	if filepath.Base(filepath.Dir(lockDir)) != instancesDirectory {
		return "", Profile{}, false
	}
	workdir := filepath.Dir(filepath.Dir(lockDir))
	name := filepath.Base(workdir)
	if !validName(name) {
		return "", Profile{}, false
	}
	return workdir, Profile{Name: name, Provider: "opencode"}, true
}

// opencodeSessionInProfileDB reports whether a session id has merged into the
// profile store. The resume picker shows live sessions from running instances
// too, but only merged ones can actually be reopened: a fresh launch seeds
// from the profile database, so an id that lives only in another instance's
// copy would resume into nothing.
func opencodeSessionInProfileDB(profile Profile, id string) bool {
	if id == "" {
		return false
	}
	root, err := profileRoot()
	if err != nil {
		return false
	}
	dbPath := filepath.Join(root, profile.Name, "data", "opencode", "opencode.db")
	if !fileExists(dbPath) {
		return false
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var one int
	return db.QueryRow(`SELECT 1 FROM session WHERE id = ?`, id).Scan(&one) == nil
}
