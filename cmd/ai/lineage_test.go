package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLineageRecordsThePassAndKeepsTheNewest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	older := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	for _, link := range []lineageLink{
		{When: older, SourceSessionID: "aaa", SourceProfile: "claude-personal", TargetProfile: "codex-work"},
		{When: older.Add(time.Hour), SourceSessionID: "aaa", SourceProfile: "claude-personal", TargetProfile: "ka"},
		{When: older, SourceSessionID: "bbb", SourceProfile: "claude-personal", TargetProfile: "codex-work"},
	} {
		if err := appendLineage(link); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(readLineage()); got != 3 {
		t.Fatalf("lineage has %d links, want every pass recorded", got)
	}
	// Handing the same work over again supersedes the earlier attempt.
	passed := handedOff(readLineage())
	if passed["aaa"].TargetProfile != "ka" || passed["bbb"].TargetProfile != "codex-work" {
		t.Fatalf("handedOff = %+v", passed)
	}
	path, _ := lineagePath()
	if strings.Contains(path, filepath.Join(appName, "profiles")) {
		t.Fatalf("lineage written inside a profile's state: %q", path)
	}
}

// Auto-swap is off unless it was turned on, and turning it on has to survive
// the launcher closing — a preference re-asserted every session is nagging.
func TestAutoSwapIsOffUntilToggledAndThenPersists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, appName, "profiles.json")
	if err := saveConfig(path, Config{Profiles: testProfiles()}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.AutoSwap {
		t.Fatal("auto-swap defaulted to on")
	}

	m := tuiModel{configPath: path, profiles: testProfiles()}
	m.toggleAutoSwap()
	if !m.autoSwap {
		t.Fatalf("toggle did not turn it on: %q", m.status)
	}
	reread, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reread.Settings.AutoSwap {
		t.Fatal("auto-swap was not written back to the config")
	}
	m.toggleAutoSwap()
	if m.autoSwap {
		t.Fatal("toggling twice did not turn it off again")
	}
}

// With auto-swap off the destination is asked for. With it on the question is
// skipped — but the brief is still shown, because writing a file and starting
// a process are different promises.
func TestAutoSwapSkipsTheDestinationQuestionOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeClaudeSession(t, root, "claude-personal", "-work-hub", "aaa",
		`{"type":"user","cwd":"`+root+`","message":{"content":[{"type":"text","text":"Do the thing"}]}}`)

	base := wideModel(testProfiles())
	base.workingDir = root
	base.recent = []recordedSession{{session: instanceSession{id: "aaa"}, folder: root}}
	base.usage = map[string]usageRemaining{"codex-work": {FiveHour: usageWindow{Percent: 80, Known: true}}}

	asked, _ := base.updateHandoff(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asked.(tuiModel); got.mode != tuiHandoffTo {
		t.Fatalf("mode = %v, want the destination question with auto-swap off", got.mode)
	}

	base.autoSwap = true
	auto, _ := base.updateHandoff(tea.KeyMsg{Type: tea.KeyEnter})
	got := auto.(tuiModel)
	if got.mode != tuiHandoffBrief {
		t.Fatalf("mode = %v, want the brief straight away with auto-swap on: %q", got.mode, got.status)
	}
	if got.handoff.path == "" || len(got.handoff.preview) == 0 {
		t.Fatalf("auto-swap produced no brief: %+v", got.handoff)
	}
	// The account with the most quota left is the one it chose.
	if got.handoff.destinations[got.handoff.target].Name != "codex-work" {
		t.Fatalf("auto-swap chose %q", got.handoff.destinations[got.handoff.target].Name)
	}
}

// Which session is leaving is never inferred, auto-swap or not: it is the one
// thing only the user knows.
func TestHandoffAlwaysAsksWhichSessionIsLeaving(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.autoSwap = true
	m.recent = []recordedSession{{session: instanceSession{id: "aaa"}, folder: "/work/hub"}}
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
	if got := updated.(tuiModel); got.mode != tuiHandoff {
		t.Fatalf("mode = %v, want the source picker even with auto-swap on", got.mode)
	}
}

func TestHandoffRefusesWithNothingRecorded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	updated, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("H")})
	got := updated.(tuiModel)
	if got.mode != tuiList || cmd != nil || !strings.Contains(got.status, "nothing recorded") {
		t.Fatalf("mode = %v, status = %q", got.mode, got.status)
	}
}
