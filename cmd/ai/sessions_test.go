package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
