package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// integrationsDesignModel is the board the handoff drew the box over, with
// the handoff's sample machine: tmux, ranma and openusage on PATH, the TUI not
// in a ranma pane, codex-personal on the tmux bar, claude-max with the shim
// set, claude-personal with ai's status line, antigravity-personal with one
// of its own.
func integrationsDesignModel(t *testing.T) tuiModel {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lines := map[string]statusLineOwner{"claude-personal": statusLineOurs, "antigravity-personal": statusLineTheirs}
	previous := machineIntegrationEnv
	machineIntegrationEnv = func() integrationEnv {
		return testIntegrationEnv("tmux ranma openusage", false, nil, lines)
	}
	t.Cleanup(func() { machineIntegrationEnv = previous })
	m := designModel(t)
	m.width, m.height = 150, 44
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	m.configPath = path
	for index := range m.profiles {
		switch m.profiles[index].Name {
		case "codex-personal":
			m.profiles[index].Indicator = tmuxIndicator
		case "claude-max":
			m.profiles[index].TmuxShim = true
		}
	}
	return m
}

func keys(t *testing.T, m tuiModel, pressed ...string) tuiModel {
	t.Helper()
	for _, key := range pressed {
		var msg tea.KeyMsg
		switch key {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEscape}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		model, _ := m.Update(msg)
		m = model.(tuiModel)
	}
	return m
}

func frameContains(t *testing.T, m tuiModel, want ...string) {
	t.Helper()
	frame := strings.Join(frameText(m), "\n")
	for _, text := range want {
		if !strings.Contains(frame, text) {
			t.Errorf("frame is missing %q:\n%s", text, frame)
		}
	}
}

func TestIntegrationsBoxRendersThePerProfileScope(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), "I")
	if m.mode != tuiIntegrations {
		t.Fatalf("I opened mode %d", m.mode)
	}
	frameContains(t, m, `writes     "statusLine"  in the profile's claude/settings.json`)
	m = keys(t, m, "down")
	if testing.Verbose() {
		t.Log("\n" + strings.Join(frameText(m), "\n"))
	}
	frameContains(t, m,
		"INTEGRATIONS  for  claude-personal", "checked the way a launch checks",
		"running: openusage is locked until claude-personal stops",
		"status line        ● ai's line",
		"openusage          ▶ locked",
		"▌ ranma tmux shim    ○ off",
		"RANMA TMUX SHIM   ○ off",
		`writes     "tmux_shim": true  on claude-personal in profiles.json`,
		"launch     adds CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1",
		"undo       ↵ again  or ai integrate ranma … --off",
		"applies only when launched from ranma",
		"✓ ranma on PATH",
		"ai integrate ranma claude-personal",
		"↵ turn on", "esc close")
}

// The list is 40 columns and the pane beside it takes the rest, as the handoff
// sets them; the divider's place is what a rounded width would move.
func TestIntegrationsListKeepsTheHandoffWidth(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), "I")
	for _, line := range frameText(m) {
		start := strings.Index(line, "INTEGRATION ")
		if start < 0 {
			continue
		}
		runes := []rune(line[start:])
		if len(runes) < integrationListWidth+2 || string(runes[integrationListWidth+1]) != "│" {
			t.Fatalf("divider is not at column %d: %q", integrationListWidth+1, string(runes))
		}
		if integrationListWidth != 40 {
			t.Fatalf("integrationListWidth = %d, the handoff sets 40", integrationListWidth)
		}
		return
	}
	t.Fatal("no INTEGRATION heading in the frame")
}

// The cursor opens on the first row it can act on and passes the locked one.
func TestIntegrationsCursorSkipsALockedRow(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), "I")
	_, status, _ := m.currentIntegration()
	if status.kind != integrationStatusLine {
		t.Fatalf("cursor opened on %s", status.kind.name())
	}
	m = keys(t, m, "down", "down", "down")
	if _, status, _ := m.currentIntegration(); status.kind != integrationRanma {
		t.Fatalf("cursor went past the shim onto %s", status.kind.name())
	}
}

func TestIntegrationsGridExplainsTheCellUnderTheCursor(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), "I", "a")
	for m.integrations.profiles[m.integrations.account].Name != "codex-personal" {
		m = keys(t, m, "up")
	}
	m = keys(t, m, "right", "right")
	if testing.Verbose() {
		t.Log("\n" + strings.Join(frameText(m), "\n"))
	}
	frameContains(t, m,
		"INTEGRATIONS   every profile", "this TUI is not in a ranma pane",
		"ACCOUNT                            STATUS LINE                OPENUSAGE                  RANMA SHIM",
		// The rows are pinned whole: the 22/13 account columns and the 27-cell
		// integrations are hand-set in the handoff, and a tidy-up that rounds
		// them redraws the grid.
		"  claude-max            claude        ○ off       own line       · not read                 ● on        inert here",
		"▌ codex-personal        codex         ● on        tmux bar       · not read                ›✗ refused   tmux bar",
		"  codex-work            codex         ○ off       tmux bar       ▶ locked    running        ○ off",
		"  claude-personal       claude        ● ai's line                ▶ locked    running        ○ off",
		"  antigravity-personal  antigravity   ● another   not ai's       — n/a       antigravity    ○ off",
		"  ● on   ○ off   · not read   ▶ locked while running   — not for this provider   ✗ refused",
		"CODEX-PERSONAL  ·  RANMA TMUX SHIM",
		"✗ refused  codex-personal shows its profile in a tmux status bar, and the shim is refused beside it",
		`the status bar has no undo yet: remove "indicator": "tmux" from profiles.json by hand, then turn the shim on`,
		"same as    ai integrate ranma codex-personal  → refused",
		"a this profile only")
}

// Flipping back from the grid opens the profile the grid was on, and the
// board follows it.
func TestIntegrationsScopeFlipFollowsTheGridCursor(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), "I", "a", "up")
	name := m.integrations.profiles[m.integrations.account].Name
	m = keys(t, m, "a")
	if selected, _ := m.selectedProfile(); selected.Name != name {
		t.Fatalf("board is on %s, grid was on %s", selected.Name, name)
	}
	frameContains(t, m, "INTEGRATIONS  for  "+name)
}

func TestIntegrationsTogglesTheShimInPlace(t *testing.T) {
	m := integrationsDesignModel(t)
	if err := saveConfig(m.configPath, Config{Profiles: m.profiles}); err != nil {
		t.Fatal(err)
	}
	m = keys(t, m, "I", "down", "enter")
	if _, status, _ := m.currentIntegration(); status.kind != integrationRanma {
		t.Fatalf("cursor on %s", status.kind.name())
	}
	if m.statusKind == statusErr {
		t.Fatalf("toggle failed: %s", m.status)
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := findProfile(cfg, "claude-personal")
	if !profile.TmuxShim {
		t.Fatal("↵ on the shim row did not turn it on")
	}
	frameContains(t, m, "ranma tmux shim    ● on", "↵ turn off")
	m = keys(t, m, "enter")
	if cfg, _ = loadConfig(m.configPath); func() bool { p, _ := findProfile(cfg, "claude-personal"); return p.TmuxShim }() {
		t.Fatal("↵ again did not turn it off")
	}
}

func TestIntegrationsConfirmsOpenUsageBeforeRunning(t *testing.T) {
	m := integrationsDesignModel(t)
	m.followSelection("claude-max")
	m = keys(t, m, "I", "down")
	if _, status, _ := m.currentIntegration(); status.kind != integrationOpenUsage {
		t.Fatalf("cursor on %s", status.kind.name())
	}
	m = keys(t, m, "enter")
	if m.mode != tuiConfirmOpenUsage {
		t.Fatalf("↵ on openusage went to mode %d", m.mode)
	}
	if testing.Verbose() {
		t.Log("\n" + strings.Join(frameText(m), "\n"))
	}
	frameContains(t, m, "INSTALL OPENUSAGE HOOKS   claude-max",
		"runs        openusage integrations install claude_code",
		"with        claude-max's environment  CLAUDE_CONFIG_DIR, AI_PROFILE",
		"not read after it finishes. There is no undo from ai yet.",
		"y install", "n cancel")
	if m = keys(t, m, "n"); m.mode != tuiIntegrations {
		t.Fatalf("n went to mode %d, want back to the box", m.mode)
	}
}

func TestPaletteListsIntegrationsWithALiveNote(t *testing.T) {
	m := keys(t, integrationsDesignModel(t), " ")
	// Cut where the palette's column ends, as the handoff draws it.
	frameContains(t, m, "I      integrations                1 on · openusage loc…")
}
