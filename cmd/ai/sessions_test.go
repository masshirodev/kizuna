package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedOpenCodeSession writes one row into a profile's OpenCode session store,
// creating the database and table on first use. time_created is unix
// milliseconds, matching the real schema (confirmed by inspecting an actual
// opencode.db directly) rather than the RFC3339 strings every other reader
// in this file parses.
func seedOpenCodeSession(t *testing.T, root, profile, id, title, directory string, createdMillis int64) {
	t.Helper()
	dir := filepath.Join(root, appName, "profiles", profile, "data", "opencode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS session (
		id text PRIMARY KEY,
		title text NOT NULL,
		directory text NOT NULL,
		time_created integer NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session (id, title, directory, time_created) VALUES (?, ?, ?, ?)`,
		id, title, directory, createdMillis); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeSessionsOrdersNewestFirstAndParsesMillisecondTimestamps(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	older := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 8, 10, 11, 0, 0, 0, time.UTC)
	seedOpenCodeSession(t, root, "opencode-go", "ses_old", "edit contacts", "/work/lattice", older.UnixMilli())
	seedOpenCodeSession(t, root, "opencode-go", "ses_new", "fix build", "/work/wayfarer", newer.UnixMilli())

	got := recentSessions(Profile{Name: "opencode-go", Provider: "opencode"}, 0)
	if len(got) != 2 {
		t.Fatalf("recent = %+v, want 2 records", got)
	}
	if got[0].session.id != "ses_new" || got[0].session.title != "fix build" || got[0].folder != "/work/wayfarer" {
		t.Fatalf("newest record = %+v", got[0])
	}
	if !got[0].when.Equal(newer) {
		t.Fatalf("newest when = %v, want %v (millisecond epoch parsed correctly)", got[0].when, newer)
	}
	if got[1].session.id != "ses_old" {
		t.Fatalf("second record = %+v, want the older session", got[1])
	}
}

func TestOpenCodeSessionsRespectsLimit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedOpenCodeSession(t, root, "opencode-go", "ses"+string(rune('a'+i)), "session", "/work",
			base.Add(time.Duration(i)*time.Hour).UnixMilli())
	}
	got := recentSessions(Profile{Name: "opencode-go", Provider: "opencode"}, 2)
	if len(got) != 2 {
		t.Fatalf("recent = %+v, want exactly 2 records for a limit of 2", got)
	}
}

func writeRollout(t *testing.T, root, name, cwd string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, appName, "profiles", "codex-work", "codex", "sessions", "2026", "08", "10")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Codex names a rollout with dashes in the clock time and repeats the real
	// timestamp inside the header, which is what the reader parses.
	started := name[:11] + strings.ReplaceAll(name[11:], "-", ":") + "Z"
	body := []string{`{"type":"session_meta","payload":{"session_id":"` + name + `-id","cwd":"` + cwd + `","timestamp":"` + started + `"}}`}
	body = append(body, lines...)
	path := filepath.Join(dir, "rollout-"+name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(body, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCodexSessionUsesFirstRealUserMessage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeRollout(t, root, "2026-08-10T10-00-00", "/work/lattice",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/work/lattice</cwd>\n</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"# Edit contacts\n\nfix the form"}]}}`,
	)

	session := codexSessionInFolder(Profile{Name: "codex-work", Provider: "codex"}, "/work/lattice")
	if session.id != "2026-08-10T10-00-00-id" {
		t.Fatalf("session id = %q", session.id)
	}
	if session.title != "Edit contacts" {
		t.Fatalf("title = %q, want the first thing the user typed", session.title)
	}
}

func TestCodexSessionIgnoresOtherFolders(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeRollout(t, root, "2026-08-10T09-00-00", "/work/wanted",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"older but right folder"}]}}`)
	writeRollout(t, root, "2026-08-10T11-00-00", "/work/other",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"newer but wrong folder"}]}}`)

	session := codexSessionInFolder(Profile{Name: "codex-work", Provider: "codex"}, "/work/wanted")
	if session.title != "older but right folder" {
		t.Fatalf("title = %q, want the newest session recorded for the folder", session.title)
	}
}

func TestCodexSessionIsEmptyWithoutAFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if session := codexSessionInFolder(Profile{Name: "codex-work", Provider: "codex"}, ""); session != (instanceSession{}) {
		t.Fatalf("session = %+v, want nothing for an unrecorded folder", session)
	}
}

func TestMatchClaudeSessionPrefersRecordedPID(t *testing.T) {
	agents := []claudeAgent{
		{PID: 10, CWD: "/work/hub", SessionID: "aaa", Name: "hub work"},
		{PID: 20, CWD: "/work/hub", SessionID: "bbb", Name: "hub review"},
	}
	got := matchClaudeSession(agents, profileInstance{pid: 20, folder: "/work/hub"})
	if got.id != "bbb" || got.title != "hub review" {
		t.Fatalf("session = %+v, want the agent with the matching PID", got)
	}
}

func TestMatchClaudeSessionFallsBackToFolder(t *testing.T) {
	agents := []claudeAgent{{PID: 10, CWD: "/work/hub", SessionID: "aaa", Name: "hub work"}}
	if got := matchClaudeSession(agents, profileInstance{pid: 99, folder: "/work/hub"}); got.id != "aaa" {
		t.Fatalf("session = %+v, want the folder match", got)
	}
	if got := matchClaudeSession(agents, profileInstance{pid: 99}); got != (instanceSession{}) {
		t.Fatalf("session = %+v, want nothing without a PID or folder match", got)
	}
}

func TestDescribeInstancesLeavesUnsupportedProviders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	instances := []profileInstance{{pid: 1, folder: "/work/hub"}}
	got := describeInstances(Profile{Name: "opencode-go", Provider: "opencode"}, instances)
	if len(got) != 1 || got[0].session != (instanceSession{}) {
		t.Fatalf("described = %+v, want OpenCode left undescribed", got)
	}
}

func TestSummariseTitleTakesTheFirstMeaningfulLine(t *testing.T) {
	if got := summariseTitle("\n\n## Board\nLattice"); got != "Board" {
		t.Fatalf("summary = %q", got)
	}
	if got := summariseTitle("   "); got != "" {
		t.Fatalf("summary = %q, want an empty title", got)
	}
}

// writeTranscript puts one Claude conversation log where the profile's own
// CLAUDE_CONFIG_DIR would.
func writeTranscript(t *testing.T, root, profile, project, session, cwd, timestamp, text string) {
	t.Helper()
	dir := filepath.Join(root, appName, "profiles", profile, "claude", "projects", project)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"mode","sessionId":"` + session + `"}`,
		`{"type":"user","cwd":"` + cwd + `","timestamp":"` + timestamp + `","message":{"content":[{"type":"text","text":"` + text + `"}]}}`,
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRecentSessionsReadsCodexRollouts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeRollout(t, root, "2026-08-10T09-00-00", "/work/hub",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"older session"}]}}`)
	writeRollout(t, root, "2026-08-10T11-00-00", "/work/lattice",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"newer session"}]}}`)

	got := recentSessions(Profile{Name: "codex-work", Provider: "codex"}, 0)
	if len(got) != 2 {
		t.Fatalf("recent = %+v, want both recorded sessions", got)
	}
	if got[0].session.title != "newer session" || got[0].folder != "/work/lattice" {
		t.Fatalf("recent[0] = %+v, want the newest session first", got[0])
	}
	if got[0].when.IsZero() {
		t.Fatalf("recent[0] carries no timestamp: %+v", got[0])
	}
}

// The recent list covers every folder, while the hijack picker still asks only
// about the one an instance was launched in.
func TestRecentSessionsIsNotLimitedToOneFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeRollout(t, root, "2026-08-10T09-00-00", "/work/hub",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"hub work"}]}}`)

	profile := Profile{Name: "codex-work", Provider: "codex"}
	if got := recentSessions(profile, 0); len(got) != 1 || got[0].folder != "/work/hub" {
		t.Fatalf("recent = %+v, want the session in any folder", got)
	}
	if session := codexSessionInFolder(profile, "/work/elsewhere"); session != (instanceSession{}) {
		t.Fatalf("the folder lookup answered for the wrong folder: %+v", session)
	}
}

func TestRecentSessionsReadsClaudeTranscripts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeTranscript(t, root, "claude-personal", "-work-hub", "aaa", "/work/hub",
		"2026-08-29T14:02:00.000Z", "# Draft the release notes\\n\\nstart with the summary")

	got := recentSessions(Profile{Name: "claude-personal", Provider: "claude"}, 0)
	if len(got) != 1 {
		t.Fatalf("recent = %+v, want the recorded conversation", got)
	}
	if got[0].session.title != "Draft the release notes" {
		t.Fatalf("title = %q, want the first thing the user typed", got[0].session.title)
	}
	if got[0].folder != "/work/hub" || got[0].session.id != "aaa" {
		t.Fatalf("recent[0] = %+v, want the folder and session id from the log", got[0])
	}
	if got[0].when.IsZero() {
		t.Fatalf("recent[0] carries no timestamp: %+v", got[0])
	}
}

func TestRecentSessionsIgnoresProvidersThatRecordNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := recentSessions(Profile{Name: "opencode-go", Provider: "opencode"}, 0); got != nil {
		t.Fatalf("recent = %+v, want nothing for a provider with no transcripts", got)
	}
}

// The live panel refreshes on a timer, so it names sessions from the logs
// already on disk rather than spending a subprocess per profile on every tick.
func TestDescribeLiveInstancesMatchesOnTheLaunchFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeRollout(t, root, "2026-08-10T11-00-00", "/work/lattice",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"edit contacts"}]}}`)

	profiles := []Profile{{Name: "codex-work", Provider: "codex", Command: "codex"}}
	got := describeLiveInstances(profiles, []profileInstance{
		{profile: "codex-work", pid: 11, folder: "/work/lattice"},
		{profile: "codex-work", pid: 12, folder: "/work/elsewhere"},
	})
	if got[0].session.title != "edit contacts" {
		t.Fatalf("instance in a recorded folder was not described: %+v", got[0])
	}
	if got[1].session != (instanceSession{}) {
		t.Fatalf("instance in an unrecorded folder was given a session: %+v", got[1])
	}
}

// Both CLIs open a conversation with blocks they wrote themselves. A reader
// that takes the first user message titles half the list "Caveat: The messages
// below…" rather than naming what the session was about.
func TestRecentSessionsSkipsTheCLIsOwnPreamble(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, appName, "profiles", "claude-personal", "claude", "projects", "-work-hub")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","isMeta":true,"cwd":"/work/hub","timestamp":"2026-08-29T14:00:00.000Z","message":{"content":[{"type":"text","text":"<local-command-caveat>Caveat: The messages below were generated by the user"}]}}`,
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-08-29T14:01:00.000Z","message":{"content":[{"type":"text","text":"<command-name>/clear</command-name>"}]}}`,
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-08-29T14:01:30.000Z","message":{"content":[{"type":"text","text":"<local-command-stdout>Set effort level to high</local-command-stdout>"}]}}`,
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-08-29T14:02:00.000Z","message":{"content":[{"type":"text","text":"<system-reminder>be nice</system-reminder>"},{"type":"text","text":"Fix the contacts form"}]}}`,
	}
	path := filepath.Join(dir, "aaa.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got := recentSessions(Profile{Name: "claude-personal", Provider: "claude"}, 0)
	if len(got) != 1 {
		t.Fatalf("recent = %+v, want the conversation", got)
	}
	if got[0].session.title != "Fix the contacts form" {
		t.Fatalf("title = %q, want the first thing the user actually typed", got[0].session.title)
	}
	// The folder and time still come from the first entry, preamble or not.
	if got[0].folder != "/work/hub" || got[0].when.IsZero() {
		t.Fatalf("recent[0] = %+v, want it placed and dated from the opening entry", got[0])
	}
}

// A conversation that is nothing but preamble is still a session that happened;
// it is listed without a title rather than dropped.
func TestRecentSessionsKeepsAConversationWithNoUsableTitle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeTranscript(t, root, "claude-personal", "-work-hub", "aaa", "/work/hub",
		"2026-08-29T14:00:00.000Z", "<system-reminder>nothing to see</system-reminder>")

	got := recentSessions(Profile{Name: "claude-personal", Provider: "claude"}, 0)
	if len(got) != 1 || got[0].session.title != "" || got[0].folder != "/work/hub" {
		t.Fatalf("recent = %+v, want an untitled but placed session", got)
	}
}

// Codex opens practically every rollout with its project-instruction dump, so a
// filter that only knows Claude's shapes titles the whole Codex list
// "# AGENTS.md instructions for …" — the same string for every row, in the one
// panel whose job is telling sessions apart.
func TestCodexPreamblesAreSkipped(t *testing.T) {
	for _, preamble := range []string{
		"# AGENTS.md instructions for /home/masshiro/projects/lattice",
		"# AGENTS.md instructions",
		"<turn_aborted>",
		"<environment_context>\n  <cwd>/work/hub</cwd>\n</environment_context>",
	} {
		if got := userText(preamble); got != "" {
			t.Errorf("userText(%.40q) = %q, want it skipped", preamble, got)
		}
	}
}

// The IDE integration is the shape a prefix filter gets wrong in the other
// direction: the prompt is inside the block, so skipping the message loses it
// and keeping the message titles the session with the wrapper.
func TestIDEContextIsUnwrappedRatherThanSkipped(t *testing.T) {
	wrapped := "# Context from my IDE setup:\n\n## Open tabs:\n- main.go: cmd/ai/main.go\n\n" +
		"## My request for Codex:\nCan we cache the rotation queue?"
	if got := userText(wrapped); got != "Can we cache the rotation queue?" {
		t.Fatalf("userText = %q, want only what the user typed", got)
	}
	// All context and no request is a preamble by another name.
	if got := userText("# Context from my IDE setup:\n\n## Open tabs:\n- main.go: cmd/ai/main.go"); got != "" {
		t.Fatalf("a wrapper with no request = %q, want it skipped", got)
	}
	// A message that merely mentions the seam is not a wrapper.
	if got := userText("why does codex print ## My request for Codex: in the log?"); got == "" {
		t.Fatal("a normal message containing the seam was skipped")
	}
}

// The end-to-end version: a Codex rollout shaped like the real ones, whose
// title has to come from inside the IDE wrapper rather than from either block
// the CLI wrote.
func TestCodexRolloutTitleSurvivesTheInjectedBlocks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, appName, "profiles", "codex-work", "codex", "sessions", "2026", "09", "04")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"session_meta","payload":{"session_id":"abc","cwd":"/work/hub","timestamp":"2026-09-04T10:00:00.000Z"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"# AGENTS.md instructions for /work/hub\n\n<INSTRUCTIONS>\nbe good\n</INSTRUCTIONS>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"<environment_context>\n  <cwd>/work/hub</cwd>\n</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"# Context from my IDE setup:\n\n## Open tabs:\n- a.go: a.go\n\n## My request for Codex:\nAdd the opener button"}]}}`,
	}
	path := filepath.Join(dir, "rollout-2026-09-04T10-00-00-abc.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got := recentSessions(Profile{Name: "codex-work", Provider: "codex"}, 0)
	if len(got) != 1 {
		t.Fatalf("recent = %+v, want the rollout", got)
	}
	if got[0].session.title != "Add the opener button" {
		t.Fatalf("title = %q, want what the user typed inside the wrapper", got[0].session.title)
	}
}
