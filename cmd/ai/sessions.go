package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// instanceSession names the conversation a running instance has open. Both
// fields are best effort: providers that expose neither leave them empty, and
// the TUI simply shows the instance without a title.
type instanceSession struct {
	id    string
	title string
}

// sessionLookupTimeout bounds the provider CLI call made to list live
// sessions, so a hung binary cannot freeze the picker that asked for it.
const sessionLookupTimeout = 5 * time.Second

// describeInstances fills in the session each instance is working on. It is
// meant to run off the UI thread: claude is asked over a subprocess and codex
// is answered by reading its session log, both of which take long enough to be
// felt as a stutter if done during a keypress.
func describeInstances(profile Profile, instances []profileInstance) []profileInstance {
	described := append([]profileInstance(nil), instances...)
	switch profile.Provider {
	case "claude":
		live := claudeLiveSessions(profile)
		for index, instance := range described {
			described[index].session = matchClaudeSession(live, instance)
		}
	case "codex":
		for index, instance := range described {
			described[index].session = codexSessionInFolder(profile, instance.folder)
		}
	}
	return described
}

type claudeAgent struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// claudeLiveSessions asks Claude Code for the sessions running under this
// profile. The profile environment is applied so the answer covers that
// profile's isolated state and nothing else.
func claudeLiveSessions(profile Profile) []claudeAgent {
	ctx, cancel := context.WithTimeout(context.Background(), sessionLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, profile.Command, "agents", "--json")
	cmd.Env = launchEnvironment(profile, "", "", os.Environ())
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var agents []claudeAgent
	if err := json.Unmarshal(output, &agents); err != nil {
		return nil
	}
	return agents
}

// matchClaudeSession prefers the PID recorded at launch. The launch folder is
// the fallback, which is what answers when the CLI reports a different process
// in the same tree than the one ai-session started.
func matchClaudeSession(agents []claudeAgent, instance profileInstance) instanceSession {
	for _, agent := range agents {
		if agent.PID == instance.pid {
			return instanceSession{id: agent.SessionID, title: agent.Name}
		}
	}
	if instance.folder == "" {
		return instanceSession{}
	}
	for _, agent := range agents {
		if agent.CWD == instance.folder {
			return instanceSession{id: agent.SessionID, title: agent.Name}
		}
	}
	return instanceSession{}
}

// codexSessionSearchLimit caps how many recorded sessions are opened while
// looking for the newest one in a folder. The rollouts are ordered by when they
// were last written, so the match is almost always in the first few.
const codexSessionSearchLimit = 60

// codexSessionInFolder finds the most recent Codex session recorded for a
// folder. Codex has no command that lists sessions as data, so its own rollout
// log is read instead — the session id from the header, and the first real
// user message as the title.
func codexSessionInFolder(profile Profile, folder string) instanceSession {
	if folder == "" {
		return instanceSession{}
	}
	root, err := profileRoot()
	if err != nil {
		return instanceSession{}
	}
	rollouts := pathsByModTime(codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions")))
	for index, path := range rollouts {
		if index >= codexSessionSearchLimit {
			break
		}
		if record, ok := readCodexRollout(path, folder); ok {
			return record.session
		}
	}
	return instanceSession{}
}

// describeLiveInstances names what each running instance is working on using
// only the transcripts already on disk. The hijack picker asks the provider CLI
// instead, because reattaching needs the session id the CLI is holding open;
// this panel refreshes on a timer and cannot spend a subprocess per profile on
// every tick.
func describeLiveInstances(profiles []Profile, instances []profileInstance) []profileInstance {
	described := append([]profileInstance(nil), instances...)
	byProfile := make(map[string][]recordedSession, len(profiles))
	for index, instance := range described {
		if instance.folder == "" {
			continue
		}
		records, read := byProfile[instance.profile]
		if !read {
			records = recentSessions(profileNamed(profiles, instance.profile), recentSessionLimit)
			byProfile[instance.profile] = records
		}
		// recentSessions is newest first, so the first match in the launch
		// folder is the conversation this instance most likely has open.
		for _, record := range records {
			if record.folder == instance.folder {
				described[index].session = record.session
				break
			}
		}
	}
	return described
}

func profileNamed(profiles []Profile, name string) Profile {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile
		}
	}
	return Profile{}
}

// recordedSession is one conversation read back out of a provider's own log:
// what it was about, where it ran, when it started, and when it was last
// touched. It is what the recent list is built from, and it is deliberately not
// instanceSession — a recorded session need not still be running.
type recordedSession struct {
	session instanceSession
	folder  string
	// when is when the conversation began. It is what the handoff brief names
	// as the point the work started from, and it never changes once read.
	when time.Time
	// lastActive is when the conversation was last written to, which is the
	// message that actually decided the list's order. A session opened three
	// days ago and answered a minute ago belongs at the top; ordering by when
	// buried it under everything started since.
	lastActive time.Time
	// profile is the account that recorded the conversation. It is empty for a
	// record read by a caller that already knows which profile it asked about,
	// and set by the union reader so one picker can list several accounts at
	// once and still reopen each row under the profile that owns it.
	profile string
}

// activity is the time a conversation is ordered and dated by: when it was last
// written to, falling back to when it began for a record whose file could not
// be stat-ed.
func (record recordedSession) activity() time.Time {
	if !record.lastActive.IsZero() {
		return record.lastActive
	}
	return record.when
}

// recentSessionLimit caps a profile's recent list. The panel shows a handful of
// rows; reading more only to throw them away is work done on every refresh.
const recentSessionLimit = 12

// allSessionLimit caps the union list the pickers offer when they are switched
// to every profile. It is wider than one profile's panel because it is a
// deliberate search across accounts rather than a glance at the last few.
const allSessionLimit = 80

// recentSessions lists what a profile has worked on lately, newest activity
// first. Both providers keep their own transcript on disk, so this reads their
// logs rather than asking either CLI: the answer has to arrive for five profiles
// at once, and a subprocess per profile is not a refresh, it is a stall.
func recentSessions(profile Profile, limit int) []recordedSession {
	if limit <= 0 {
		limit = recentSessionLimit
	}
	var paths []string
	switch profile.Provider {
	case "codex":
		root, err := profileRoot()
		if err != nil {
			return nil
		}
		paths = codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions"))
		// A resumed Codex session is appended to the rollout it began in, so the
		// file it was created as is no longer its rank. Ordering by the filename
		// (which is the creation time) would read the wrong twelve and then sort
		// them correctly but incompletely.
		paths = pathsByModTime(paths)
	case "claude":
		paths = claudeTranscripts(profile)
	case "opencode":
		records := opencodeSessions(profile, limit)
		for index := range records {
			records[index].profile = profile.Name
		}
		return records
	default:
		return nil
	}

	records := make([]recordedSession, 0, limit)
	for index, path := range paths {
		if len(records) >= limit || index >= codexSessionSearchLimit {
			break
		}
		var record recordedSession
		var ok bool
		if profile.Provider == "codex" {
			record, ok = readCodexRollout(path, "")
		} else {
			record, ok = readClaudeTranscript(path)
		}
		if ok {
			record.profile = profile.Name
			records = append(records, record)
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].activity().After(records[j].activity()) })
	return records
}

// allRecentSessions reads every profile's recent list and returns one union,
// newest activity first. The union is what the pickers list when they are
// switched away from the selected profile; each record carries the account that
// recorded it, so the row can be reopened under the right one.
func allRecentSessions(profiles []Profile, limit int) []recordedSession {
	if limit <= 0 {
		limit = allSessionLimit
	}
	var records []recordedSession
	for _, profile := range profiles {
		records = append(records, recentSessions(profile, limit)...)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].activity().After(records[j].activity()) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records
}

// opencodeSessions reads a profile's OpenCode session store directly: unlike
// Claude and Codex, OpenCode keeps a SQLite database with title, folder, and
// timestamp as plain columns, so a single sorted, limited query stands in for
// the list-then-read-each-file shape the other two providers need. A missing
// database (no session run yet) or any query error is treated the same as
// "nothing to show" rather than surfaced, matching every other reader here.
//
// With concurrent instances, each running launch owns a private copy of the
// store. The read unions the profile database with every live instance's
// copy, so the live panel names what every instance is doing. Stale,
// not-yet-merged copies are deliberately excluded: only merged sessions can
// actually be reopened (a fresh launch seeds from the profile database), and
// the next ai start merges the strays in.
func opencodeSessions(profile Profile, limit int) []recordedSession {
	paths := opencodeSessionDBs(profile)
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var records []recordedSession
	for index, dbPath := range paths {
		rows, err := queryOpenCodeSessions(dbPath, 0)
		if err != nil {
			if index == 0 {
				return nil
			}
			continue
		}
		for _, record := range rows {
			if seen[record.session.id] {
				continue
			}
			seen[record.session.id] = true
			records = append(records, record)
		}
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].activity().After(records[j].activity()) })
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records
}

// opencodeSessionDBs lists the session stores to union: the profile's, then
// every live isolated instance's copy.
func opencodeSessionDBs(profile Profile) []string {
	root, err := profileRoot()
	if err != nil {
		return nil
	}
	workdir := filepath.Join(root, profile.Name)
	var paths []string
	if dbPath := filepath.Join(workdir, "data", "opencode", "opencode.db"); fileExists(dbPath) {
		paths = append(paths, dbPath)
	}
	lockDirs, err := activeProfileInstanceLocks(workdir)
	if err != nil {
		return paths
	}
	for _, lockDir := range lockDirs {
		if !isIsolatedInstanceDir(lockDir) {
			continue
		}
		if dbPath := filepath.Join(lockDir, "data", "opencode", "opencode.db"); fileExists(dbPath) {
			paths = append(paths, dbPath)
		}
	}
	return paths
}

// queryOpenCodeSessions reads a session store newest activity first. A store
// written by an OpenCode old enough not to have time_updated still lists, read
// from time_created instead — the panel is a convenience, and refusing to list
// it would be a worse answer than dating it by when it began.
func queryOpenCodeSessions(dbPath string, limit int) ([]recordedSession, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	records, err := queryOpenCodeSessionRows(db, "time_updated", limit)
	if err != nil {
		return queryOpenCodeSessionRows(db, "time_created", limit)
	}
	return records, nil
}

// queryOpenCodeSessionRows runs the listing against one known timestamp column,
// selected twice so a store without time_updated still gets a lastActive back.
func queryOpenCodeSessionRows(db *sql.DB, column string, limit int) ([]recordedSession, error) {
	query := "SELECT id, title, directory, time_created, " + column + " FROM session ORDER BY " + column + " DESC"
	var rows *sql.Rows
	var err error
	if limit > 0 {
		rows, err = db.Query(query+" LIMIT ?", limit)
	} else {
		rows, err = db.Query(query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []recordedSession
	for rows.Next() {
		var id, title, directory string
		var createdMillis, updatedMillis int64
		if err := rows.Scan(&id, &title, &directory, &createdMillis, &updatedMillis); err != nil {
			continue
		}
		records = append(records, recordedSession{
			session:    instanceSession{id: id, title: title},
			folder:     directory,
			when:       time.UnixMilli(createdMillis),
			lastActive: time.UnixMilli(updatedMillis),
		})
	}
	return records, rows.Err()
}

// pathsByModTime orders transcript paths by when they were last written to,
// newest first. A path that cannot be stat-ed sorts last rather than dropping
// out: the reader will fail to open it and skip it on its own terms.
func pathsByModTime(paths []string) []string {
	type stamped struct {
		path string
		mod  time.Time
	}
	stamps := make([]stamped, 0, len(paths))
	for _, path := range paths {
		when := time.Time{}
		if info, err := os.Stat(path); err == nil {
			when = info.ModTime()
		}
		stamps = append(stamps, stamped{path: path, mod: when})
	}
	sort.SliceStable(stamps, func(i, j int) bool { return stamps[i].mod.After(stamps[j].mod) })
	ordered := make([]string, 0, len(stamps))
	for _, stamp := range stamps {
		ordered = append(ordered, stamp.path)
	}
	return ordered
}

// claudeTranscripts lists a profile's Claude conversation logs newest first.
// Claude names them by session id rather than by time, so unlike the Codex
// rollouts these have to be stat-ed to be ordered.
//
// Subagent transcripts are skipped. They live under a `subagents/` folder
// beside their parent conversation and are written every time a subagent runs,
// so a session that used them leaves a dozen recent-looking files behind — none
// of which is a conversation that can be resumed. Left in, they were most of the
// list after any session that delegated.
func claudeTranscripts(profile Profile) []string {
	root, err := profileRoot()
	if err != nil {
		return nil
	}
	projects := filepath.Join(root, profile.Name, "claude", "projects")
	type transcript struct {
		path    string
		modTime time.Time
	}
	var files []transcript
	_ = filepath.WalkDir(projects, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if isSubagentTranscript(path) {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, transcript{path: path, modTime: info.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

// isSubagentTranscript reports whether a Claude transcript is a subagent's
// rather than a conversation's. A subagent log sits directly under a
// `subagents` directory.
func isSubagentTranscript(path string) bool {
	return filepath.Base(filepath.Dir(path)) == "subagents"
}

// readClaudeTranscript pulls one conversation out of a Claude log: where and
// when it ran, and what it is called. Reading stops as soon as both are settled
// rather than decoding a transcript that can run to megabytes of tool output.
func readClaudeTranscript(path string) (recordedSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return recordedSession{}, false
	}
	defer file.Close()

	record := recordedSession{session: instanceSession{id: claudeSessionID(path)}}
	// The file is appended to on every turn, so its modification time is when
	// the conversation was last spoken in — the fact the list is ordered by.
	if info, err := file.Stat(); err == nil {
		record.lastActive = info.ModTime()
	}
	found, named := false, false
	// opening is what was first asked. It titles the row only if the log never
	// names the conversation, so a session Claude has since named is not still
	// listed under the sentence that started it.
	opening := ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	// The loop stops as soon as the record is settled — a placed, dated, named
	// conversation — whichever order the two records arrive in. The check sits
	// ahead of Scan() so a settled transcript does not read one more line, and
	// a line here can be 300 KB.
	for lines := 0; lines < claudeTranscriptScanLines && !(found && named) && scanner.Scan(); lines++ {
		line := scanner.Bytes()
		// The lines between the opening message and the conversation's name are
		// the expensive ones — one attachment runs to tens of kilobytes, and the
		// first forty lines of a transcript here measured 300 KB — and once the
		// opening is recorded nothing but a name record can still change the
		// answer. A key absent from the raw bytes cannot be set on the decoded
		// struct, so those lines are skipped without being parsed at all.
		if found && opening != "" && !bytes.Contains(line, claudeTitleKey) && !bytes.Contains(line, claudeAgentNameKey) {
			continue
		}
		var entry struct {
			Type      string `json:"type"`
			CWD       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
			IsMeta    bool   `json:"isMeta"`
			AITitle   string `json:"aiTitle"`
			AgentName string `json:"agentName"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if name, exact := claudeConversationName(entry.Type, entry.AITitle, entry.AgentName); name != "" && !named {
			record.session.title, named = name, exact
			continue
		}
		if entry.Type != "user" {
			continue
		}
		// Where and when come from the first user entry even when its text is
		// something the CLI wrote, so a conversation whose opening lines are all
		// preamble is still placed and dated.
		if !found {
			found = true
			record.folder = entry.CWD
			if when, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				record.when = when
			}
		}
		if entry.IsMeta || opening != "" {
			continue
		}
		opening = summariseTitle(claudeMessageText(entry.Message.Content))
	}
	if record.session.title == "" {
		record.session.title = opening
	}
	return record, found || record.session.title != ""
}

// The two keys that carry a conversation's name, as they appear on the wire.
var (
	claudeTitleKey     = []byte(`"aiTitle"`)
	claudeAgentNameKey = []byte(`"agentName"`)
)

// claudeConversationName is the name Claude Code gave a conversation itself,
// which is what its own UI shows and therefore what the picker should show
// instead of the sentence the session happened to open with. Two records carry
// one: `ai-title` is the phrase written when the conversation is named, and
// `agent-name` is the slug an agent session runs under. The first is the name
// proper and ends the search; the second is only better than the opening
// message, so it is kept and scanning continues in case a title follows.
func claudeConversationName(entryType, aiTitle, agentName string) (string, bool) {
	switch entryType {
	case "ai-title":
		return strings.TrimSpace(aiTitle), true
	case "agent-name":
		return strings.TrimSpace(agentName), false
	}
	return "", false
}

// claudeMessageText flattens the two shapes a Claude message body takes: a bare
// string for a plain prompt, and a list of parts once anything is attached. Text
// the CLI injected is skipped, so an attached reminder never becomes the title
// of the message it was attached to.
func claudeMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return userText(text)
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		if typed := userText(part.Text); typed != "" {
			return typed
		}
	}
	return ""
}

// userText reduces one message body to what the user actually typed: nothing
// for a block the CLI injected, and the prompt alone for one the CLI wrapped
// around it. Both providers go through it, because both wrap and both inject.
func userText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || isInjectedPreamble(text) {
		return ""
	}
	return unwrapInjected(text)
}

// injectedPreambles are the blocks the CLIs put in front of a conversation:
// environment dumps, slash-command echoes, harness reminders, project
// instructions, and caveats about who actually typed what. None of them names a
// session, and every one of them arrives as a user message — so a reader that
// takes the first user message titles half the list "Caveat: The messages
// below…".
// The command families are matched by prefix rather than by tag: each has
// several members (caveat, stdout, stderr; name, message, args) and enumerating
// them means a new one silently becomes a session title. The instruction dump is
// matched short of its path for the same reason — it names whichever directory
// the CLI was started in.
var injectedPreambles = []string{
	"<environment_context>",
	"<local-command-",
	"<command-",
	"<system-reminder>",
	"<user-prompt-submit-hook>",
	"Caveat: The messages below",
	"# AGENTS.md instructions",
	"<turn_aborted>",
}

// injectedWrappers are the blocks a CLI wraps around what the user typed rather
// than sending ahead of it. They cannot be skipped the way a preamble can: the
// prompt is inside, under a heading. Dropping the whole message loses the
// prompt, and keeping it titles the session with the wrapper.
var injectedWrappers = []struct{ prefix, seam string }{
	// Codex's IDE integration leads with the open tabs and then labels the part
	// the user actually typed.
	{"# Context from my IDE setup:", "## My request for Codex:"},
}

// unwrapInjected returns what the user typed inside a wrapper, the text
// unchanged when it is not one, and nothing for a wrapper with no prompt in it —
// that block is all context, which makes it a preamble by another name.
func unwrapInjected(text string) string {
	for _, wrapper := range injectedWrappers {
		if !strings.HasPrefix(text, wrapper.prefix) {
			continue
		}
		_, typed, found := strings.Cut(text, wrapper.seam)
		if !found {
			return ""
		}
		return strings.TrimSpace(typed)
	}
	return text
}

func isInjectedPreamble(text string) bool {
	for _, preamble := range injectedPreambles {
		if strings.HasPrefix(text, preamble) {
			return true
		}
	}
	return false
}

func claudeSessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

const (
	// claudeTranscriptScanLines bounds how far into a transcript the first user
	// entry and the conversation's name are looked for. It is wider than the
	// preamble needs because the name is written after the first exchange, not
	// before it — measured on this machine at lines 18 to 34 — and a transcript
	// that has both is abandoned as soon as it has given them up, so the bound
	// only ever costs the short sessions that were never named.
	claudeTranscriptScanLines = 120
	maxTranscriptLine         = 16 * 1024 * 1024
)

// codexRollouts lists Codex session logs by name, which carries the timestamp
// the session began. Callers that care about last activity reorder the result;
// this only avoids stat-ing every file to answer "which rollouts are there".
func codexRollouts(sessionsDir string) []string {
	var paths []string
	_ = filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	return paths
}

type codexRolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// readCodexRollout returns the session recorded in one rollout file. A non-empty
// folder restricts the answer to that directory and abandons the file as soon as
// its header says otherwise, which is what keeps the hijack picker's scan over
// dozens of rollouts to one decoded line each. Reading stops at the first usable
// title so the model instructions and tool output further down are never decoded.
func readCodexRollout(path, folder string) (recordedSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return recordedSession{}, false
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var record recordedSession
	// The rollout is appended to as the conversation goes on, so its
	// modification time is when it was last spoken in.
	if info, err := file.Stat(); err == nil {
		record.lastActive = info.ModTime()
	}
	for lines := 0; lines < 40; lines++ {
		var line codexRolloutLine
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return recordedSession{}, false
		}
		switch line.Type {
		case "session_meta":
			var meta struct {
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(line.Payload, &meta); err != nil {
				return recordedSession{}, false
			}
			if folder != "" && meta.CWD != folder {
				return recordedSession{}, false
			}
			record.session.id = meta.SessionID
			record.folder = meta.CWD
			if when, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
				record.when = when
			}
		case "response_item":
			if record.session.id == "" {
				continue
			}
			if title := codexUserText(line.Payload); title != "" {
				record.session.title = title
				return record, true
			}
		}
	}
	return record, record.session.id != ""
}

// codexUserText pulls the text of a user message, skipping the preamble Codex
// injects before the first thing the user actually typed.
func codexUserText(payload json.RawMessage) string {
	var message struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &message); err != nil || message.Role != "user" {
		return ""
	}
	for _, part := range message.Content {
		if typed := userText(part.Text); typed != "" {
			return summariseTitle(typed)
		}
	}
	return ""
}

// summariseTitle reduces a message to one short line so it fits a picker row.
func summariseTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
