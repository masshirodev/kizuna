package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeClaudeSession(t *testing.T, root, profile, encoded, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, appName, "profiles", profile, "claude", "projects", encoded)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// The brief carries what was asked, not what the tools did. A transcript is
// mostly tool traffic, and none of it survives a change of provider.
func TestBriefKeepsTheAskingAndDropsTheToolTraffic(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-09-04T10:00:00.000Z","message":{"content":[{"type":"text","text":"Add a settings page"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"user","cwd":"/work/hub","message":{"content":[{"type":"text","text":"now wire the save button"}]}}`,
	)
	profile := Profile{Name: "claude-personal", Provider: "claude"}
	record := recordedSession{session: instanceSession{id: "aaa"}, folder: "/work/hub"}

	brief, err := buildBrief(profile, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.prompts) != 2 || brief.prompts[0] != "Add a settings page" || brief.prompts[1] != "now wire the save button" {
		t.Fatalf("prompts = %q, want only what the user typed", brief.prompts)
	}
	body := renderBrief(brief, gitState{}, time.Now())
	if strings.Contains(body, "tool_use") || strings.Contains(body, "a.go") {
		t.Fatalf("the brief carried tool traffic:\n%s", body)
	}
	if !strings.Contains(body, "picking up work started in another CLI") {
		t.Fatal("the brief does not tell the next agent whose work this is")
	}
}

// Interrupting a turn records the message twice, and the short copy is
// terminated rather than cut — so a plain prefix test leaves both in.
func TestBriefDropsTheInterruptedCopyOfARequest(t *testing.T) {
	partial := "keep working; they use up one cli and want to continue on another."
	full := "keep working; they use up one cli and want to continue on another; and thats not token effective"
	got := appendPrompt(appendPrompt(nil, partial), full)
	if len(got) != 1 || got[0] != full {
		t.Fatalf("prompts = %q, want only the finished request", got)
	}
	// Two genuinely different requests are both kept.
	got = appendPrompt(appendPrompt(nil, "fix the tests"), "now ship it")
	if len(got) != 2 {
		t.Fatalf("prompts = %q, want both requests", got)
	}
}

// The model's last messages are its shortest — the line between two tool calls.
// Taking the tail by position alone picks the least informative thing it said.
func TestBriefTakesSubstantialNotesNotTheLastOnes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	conclusion := strings.Repeat("This is the decision we reached and why it holds. ", 12)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"/work/hub","message":{"content":[{"type":"text","text":"Do the thing"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"`+conclusion+`"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Now the tests:"}]}}`,
	)
	brief, err := buildBrief(Profile{Name: "claude-personal", Provider: "claude"}, recordedSession{session: instanceSession{id: "aaa"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.notes) != 1 || strings.HasPrefix(brief.notes[0], "Now the tests") {
		t.Fatalf("notes = %q, want the conclusion rather than the narration", brief.notes)
	}
}

// Quota is the whole reason the feature exists, so the account that runs out
// last leads — and the window that runs out soonest is what decides it.
func TestDestinationsAreRankedByTheTightestWindow(t *testing.T) {
	profiles := []Profile{
		{Name: "source", Provider: "claude"},
		{Name: "roomy", Provider: "codex"},
		{Name: "spent", Provider: "claude"},
		{Name: "unknown", Provider: "claude"},
		{Name: "cannot-open", Provider: "opencode"},
	}
	usage := map[string]usageRemaining{
		// A weekly allowance with room is no help once the five-hour one is gone.
		"spent": {FiveHour: usageWindow{Percent: 3, Known: true}, Weekly: usageWindow{Percent: 90, Known: true}},
		"roomy": {FiveHour: usageWindow{Percent: 71, Known: true}, Weekly: usageWindow{Percent: 64, Known: true}},
	}
	got := handoffDestinations(profiles, profiles[0], usage)
	var names []string
	for _, profile := range got {
		names = append(names, profile.Name)
	}
	// The source is not a destination, and a provider that cannot be opened
	// with a prompt is not offered at all.
	want := []string{"roomy", "spent", "unknown"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("destinations = %v, want %v", names, want)
	}
}

// An unknown remainder is not a good one; it must not outrank a measured account.
func TestUnknownQuotaSortsBelowAMeasuredOne(t *testing.T) {
	if headroom(usageRemaining{}) >= headroom(usageRemaining{FiveHour: usageWindow{Percent: 0, Known: true}}) {
		t.Fatal("an unknown quota outranked a measured empty one")
	}
}

// Codex and Claude both reduce to the same pair, which is what lets one hand
// off to the other.
func TestCodexSessionsReduceToTheSameShape(t *testing.T) {
	line := []byte(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"text":"# Context from my IDE setup:\n\n## My request for Codex:\nCache the queue"}]}}`)
	got, ok := decodeHandoffLine("codex", line)
	if !ok || !got.fromUser || got.text != "Cache the queue" {
		t.Fatalf("decoded = %+v, ok=%v, want the unwrapped request", got, ok)
	}
}

func TestBriefIsWrittenOutsideAnyProviderState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := writeBriefFile(sessionBrief{source: recordedSession{session: instanceSession{id: "aaa"}}}, "# brief")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join(appName, "handoffs", "aaa.md")) {
		t.Fatalf("brief written to %q, want ai-session's own directory", path)
	}
	if strings.Contains(path, filepath.Join(appName, "profiles")) {
		t.Fatalf("brief written inside a profile's state: %q", path)
	}
}
