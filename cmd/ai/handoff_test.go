package main

import (
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	// The source is not a destination; every other account is, including the
	// one whose provider cannot be opened on a prompt — it is handed over by
	// clipboard, so it is offered rather than dropped.
	want := []string{"roomy", "spent", "unknown", "cannot-open"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("destinations = %v, want %v", names, want)
	}
}

// A provider is only a manual destination if its CLI really cannot take an
// opening prompt; the ones that can are opened on the brief as before.
func TestOnlyProvidersWithoutPromptSyntaxAreHandedOverByClipboard(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		if !opensOnPrompt(provider) {
			t.Errorf("%s can be opened on a prompt but is treated as manual", provider)
		}
	}
	for _, provider := range []string{"opencode", "antigravity", "deepseek"} {
		if opensOnPrompt(provider) {
			t.Errorf("%s cannot be opened on a prompt but is treated as launchable", provider)
		}
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

// The brief keeps the end of the conversation as it was said, separately from
// the reduction it writes into the file. The confirmation screen asks a
// different question than the brief does — is this the work I meant to move —
// and the answer is in the last thing said rather than in the longest.
func TestBriefKeepsHowTheConversationEnded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	lines := []string{
		`{"type":"user","cwd":"/work/hub","message":{"content":[{"type":"text","text":"Add a settings page"}]}}`,
	}
	for index := range briefClosingTurns {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"step `+
			string(rune('a'+index))+`"}]}}`)
	}
	lines = append(lines, `{"type":"user","cwd":"/work/hub","message":{"content":[{"type":"text","text":"ship it"}]}}`)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa", lines...)

	brief, err := buildBrief(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "aaa"}, folder: "/work/hub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.closing) != briefClosingTurns {
		t.Fatalf("kept %d closing turns, want the bound of %d", len(brief.closing), briefClosingTurns)
	}
	last := brief.closing[len(brief.closing)-1]
	if !last.fromUser || last.text != "ship it" {
		t.Fatalf("last turn = %+v, want the end of the conversation", last)
	}
	if !brief.earlier {
		t.Fatal("a conversation that ran on before what was kept was not marked as such")
	}
	// The file is the reduction it always was: short model turns are narration,
	// and none of them belongs in the brief.
	if body := renderBrief(brief, gitState{}, time.Now()); strings.Contains(body, "step a") {
		t.Fatalf("the closing turns leaked into the brief itself:\n%s", body)
	}
}

// A conversation short enough to show whole is not marked as cut.
func TestABriefShorterThanTheBoundIsNotMarkedAsCut(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"/work/hub","message":{"content":[{"type":"text","text":"Add a settings page"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`)

	brief, err := buildBrief(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "aaa"}, folder: "/work/hub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(brief.closing) != 2 || brief.earlier {
		t.Fatalf("closing = %+v, earlier = %v, want the whole exchange unmarked", brief.closing, brief.earlier)
	}
}

// The clipboard escape carries the text as base64 under OSC 52, and is wrapped
// for tmux when the handoff is run inside a session — otherwise tmux drops it
// and the brief lands nowhere.
func TestClipboardEscapeIsOSC52AndWrapsForTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	plain := osc52Clipboard("# brief")
	if !strings.HasPrefix(plain, "\x1b]52;c;") || !strings.HasSuffix(plain, "\a") {
		t.Fatalf("escape = %q, want an OSC 52 write", plain)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(plain, "\x1b]52;c;"), "\a")
	if decoded, err := base64.StdEncoding.DecodeString(encoded); err != nil || string(decoded) != "# brief" {
		t.Fatalf("clipboard payload = %q, err = %v", decoded, err)
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,123,0")
	wrapped := osc52Clipboard("# brief")
	if !strings.HasPrefix(wrapped, "\x1bPtmux;\x1b") || !strings.HasSuffix(wrapped, "\x1b\\") {
		t.Fatalf("wrapped escape = %q, want tmux pass-through", wrapped)
	}
}

// A destination whose CLI cannot be opened on a prompt is handed over by
// clipboard: the brief is written as always, its exact bytes go to the
// terminal, the pass is recorded, and no process is started.
func TestManualDestinationCopiesTheBriefInsteadOfLaunching(t *testing.T) {
	t.Setenv("TMUX", "")
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"`+root+`","message":{"content":[{"type":"text","text":"Add a settings page"}]}}`)
	profiles := []Profile{
		{Name: "claude-personal", Provider: "claude", Command: "claude"},
		{Name: "opencode-one", Provider: "opencode", Command: "opencode"},
	}
	m := wideModel(profiles)
	m.workingDir = root
	m.recent = []recordedSession{{session: instanceSession{id: "aaa", title: "Add a settings page"}, folder: root}}
	m.mode = tuiHandoff

	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiHandoffTo || len(m.handoff.destinations) != 1 || m.handoff.destinations[0].Name != "opencode-one" {
		t.Fatalf("destinations = %+v, want the manual account offered", m.handoff.destinations)
	}
	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiHandoffBrief {
		t.Fatalf("enter on the destination opened mode %v (%s)", m.mode, m.status)
	}

	updated, cmd := m.updateHandoffBrief(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.mode != tuiList || cmd == nil {
		t.Fatalf("mode = %v, cmd = %v, want a clipboard command and the list back", got.mode, cmd != nil)
	}
	if got.handoff.path != "" {
		t.Fatalf("the draft survived the copy: %+v", got.handoff)
	}
	if _, passed := got.lineage["aaa"]; !passed {
		t.Fatalf("the pass was not recorded: %+v", got.lineage)
	}

	// The command hands the brief's own bytes to the terminal. Read what it
	// wrote rather than trusting the closure.
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = write
	msg := cmd()
	write.Close()
	os.Stdout = saved
	out, _ := io.ReadAll(read)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(string(out), "\x1b]52;c;"), "\a"))
	if err != nil {
		t.Fatalf("clipboard payload is not base64: %q", out)
	}
	if !strings.Contains(string(decoded), "Add a settings page") || !strings.Contains(string(decoded), "# Handoff") {
		t.Fatalf("the clipboard did not carry the brief:\n%s", decoded)
	}
	if copied, ok := msg.(clipboardMsg); !ok || copied.err != nil || copied.profile != "opencode-one" {
		t.Fatalf("copy reported %+v", msg)
	}
}
