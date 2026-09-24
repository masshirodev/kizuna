package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func wizardKey(t *testing.T, m tuiModel, msg tea.KeyMsg) tuiModel {
	t.Helper()
	var updated tea.Model
	switch m.mode {
	case tuiHandoff:
		updated, _ = m.updateHandoff(msg)
	case tuiHandoffTo:
		updated, _ = m.updateHandoffTo(msg)
	case tuiHandoffBrief:
		updated, _ = m.updateHandoffBrief(msg)
	case tuiRecent:
		updated, _ = m.updateRecent(msg)
	default:
		t.Fatalf("no wizard step in mode %v", m.mode)
	}
	return updated.(tuiModel)
}

// The wizard can be walked backwards: from where the work is going back to
// which conversation is leaving, and from the brief back to where it is going,
// without losing the row the first step was on.
func TestHandoffWizardStepsBack(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"`+root+`","message":{"content":[{"type":"text","text":"Do the thing"}]}}`)
	m := wideModel(testProfiles())
	m.workingDir = root
	m.recent = []recordedSession{{session: instanceSession{id: "aaa", title: "Do the thing"}, folder: root}}
	m.mode = tuiHandoff

	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiHandoffTo {
		t.Fatalf("enter on the first step opened mode %v (%s)", m.mode, m.status)
	}
	if view := m.View(); !strings.Contains(view, "✓ leaving") || !strings.Contains(view, "● going to") ||
		!strings.Contains(view, "LEAVING") || !strings.Contains(view, "GOING TO") || !strings.Contains(view, "WHAT MOVES") {
		t.Fatalf("the second step does not show where it is:\n%s", view)
	}
	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.mode != tuiHandoff || m.record != 0 {
		t.Fatalf("← from the second step went to mode %v, record %d", m.mode, m.record)
	}

	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiHandoffBrief {
		t.Fatalf("enter on the second step opened mode %v (%s)", m.mode, m.status)
	}
	if m.handoff.size == 0 || len(m.handoff.prompts) == 0 {
		t.Fatalf("the brief step kept nothing of what the brief says: %+v", m.handoff)
	}
	if view := m.View(); !strings.Contains(view, "What was asked") || !strings.Contains(view, "Do the thing") {
		t.Fatalf("the brief step does not show the brief:\n%s", view)
	}
	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.mode != tuiHandoffTo || len(m.handoff.destinations) == 0 {
		t.Fatalf("← from the brief went to mode %v with %d destinations", m.mode, len(m.handoff.destinations))
	}
}

// The resume picker's H hands the row under the cursor off instead: the row
// you would have resumed is the row you are handing over, so the first step is
// already answered.
func TestResumePickerHandsTheRowOff(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.recent = recentTestSessions(t.TempDir())
	m.mode = tuiRecent
	m.record = 1
	m = wizardKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
	if m.mode != tuiHandoffTo {
		t.Fatalf("H in the resume picker opened mode %v (%s)", m.mode, m.status)
	}
	if m.handoff.source.session.id != "bbb" {
		t.Fatalf("H handed off %q, want the row under the cursor", m.handoff.source.session.id)
	}
}

// Destinations name the window holding them back, and warn when it is nearly
// spent; the accounts that cannot be opened on a brief are named rather than
// silently missing.
func TestHandoffDestinationsSayWhatLimitsThem(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles := append(testProfiles(), Profile{Name: "codex-max", Provider: "codex", Command: "codex"},
		Profile{Name: "gemini", Provider: "antigravity", Command: "agy"})
	m := wideModel(profiles)
	m.usage = map[string]usageRemaining{
		"codex-work": {FiveHour: usageWindow{Percent: 12, Known: true}, Weekly: usageWindow{Percent: 41, Known: true}},
		"codex-max":  {FiveHour: usageWindow{Percent: 91, Known: true}, Weekly: usageWindow{Percent: 78, Known: true}},
	}
	m.mode = tuiHandoffTo
	source := profileNamed(profiles, "claude-personal")
	m.handoff = handoffDraft{
		source:       recordedSession{session: instanceSession{id: "aaa", title: "x"}, profile: source.Name},
		destinations: handoffDestinations(profiles, source, m.usage),
	}
	view := m.View()
	for _, want := range []string{"limited by 7d", "5h nearly out", "CAN'T TAKE A BRIEF", "gemini"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the destinations are missing %q:\n%s", want, view)
		}
	}
	if m.handoff.destinations[0].Name != "codex-max" {
		t.Fatalf("destinations = %v, want the most headroom first", m.handoff.destinations)
	}
}

func TestDiffStatReadsTheSummaryLine(t *testing.T) {
	for _, tc := range []struct {
		stat                  string
		files, added, removed int
	}{
		{" a.go | 3 ++-\n 2 files changed, 212 insertions(+), 88 deletions(-)", 2, 212, 88},
		{" 1 file changed, 4 insertions(+)", 1, 4, 0},
		{" 1 file changed, 9 deletions(-)", 1, 0, 9},
		{"", 0, 0, 0},
	} {
		files, added, removed := diffStat(tc.stat)
		if files != tc.files || added != tc.added || removed != tc.removed {
			t.Errorf("diffStat(%q) = %d %d %d, want %d %d %d", tc.stat, files, added, removed, tc.files, tc.added, tc.removed)
		}
	}
}

func TestBriefSizeReadsAsAFileAndATokenCount(t *testing.T) {
	if got := formatSize(5200) + " · " + approxTokens(5200); got != "5 KB · ≈1.3k tokens" {
		t.Fatalf("size = %q", got)
	}
	if got := approxTokens(800); got != "≈200 tokens" {
		t.Fatalf("small brief = %q", got)
	}
}
