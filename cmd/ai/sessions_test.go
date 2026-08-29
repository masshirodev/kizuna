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
	body := []string{`{"type":"session_meta","payload":{"session_id":"` + name + `-id","cwd":"` + cwd + `"}}`}
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
