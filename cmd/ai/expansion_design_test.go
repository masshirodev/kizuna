package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// expansionDesignModel is the Board expansion handoff's own sample data: the
// accounts and figures of the first handoff, with claude-personal filled in the
// way the expansion mock draws it — three instances, one of them headless,
// eight conversations with two that share a title, a handoff each way, and the
// integrations and installs the facts carry.
//
// HOME and PATH are a temporary tree, so the CLI resolves to
// ~/.local/bin/claude and the state directory to ~/.config/ai/profiles, as in
// the mock, whatever this machine has installed.
func expansionDesignModel(t *testing.T) tuiModel {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv(deepSeekKeyEnv, "sk-test")
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	config := filepath.Join(home, ".config")
	writeProfileFile(t, config, "{}", "claude-personal", "claude", ".credentials.json")
	writeProfileFile(t, config, "{}", "claude-max", "claude", ".credentials.json")
	writeProfileFile(t, config, "{}", "codex-personal", "codex", "auth.json")
	writeProfileFile(t, config, "{}", "codex-work", "codex", "auth.json")

	now := time.Date(2026, 9, 23, 14, 5, 0, 0, time.Local)
	at := func(hh, mm int, days int) time.Time { return time.Date(2026, 9, 23+days, hh, mm, 0, 0, time.Local) }
	w := func(p int, r time.Time) usageWindow { return usageWindow{Known: true, Percent: p, Resets: r} }
	profiles := sortedProfiles([]Profile{
		{Name: "codex-personal", Provider: "codex", Command: "codex"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
		{
			Name: "claude-personal", Provider: "claude", Command: "claude",
			DefaultArgs: []string{"--dangerously-skip-permissions", "--model", "opus"},
			DefaultEnv:  []string{"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1"},
			Notes:       "personal pro plan · you@example.com",
			TmuxShim:    true,
		},
		{Name: "claude-max", Provider: "claude", Command: "claude"},
		{Name: "antigravity-personal", Provider: "antigravity", Command: "agy"},
		{Name: "opencode-go", Provider: "opencode", Command: "opencode"},
		{Name: "deepseek", Provider: "deepseek", Command: "deepseek"},
	})
	ai := filepath.Join(home, "projects/ai-session")
	notes := filepath.Join(home, "notes")
	billing := filepath.Join(home, "work/billing")
	m := tuiModel{profiles: profiles, width: 150, height: 42, loaded: true, workingDir: ai, now: now, update: updateStatus{Known: true, Behind: 3}}
	m.usage = map[string]usageRemaining{
		"codex-personal":  {w(82, at(23, 10, 0)), w(64, at(9, 0, 3))},
		"codex-work":      {w(12, at(19, 55, 0)), w(41, at(9, 0, 2))},
		"claude-personal": {w(7, at(21, 40, 0)), w(22, at(9, 0, 1))},
		"claude-max":      {w(91, at(22, 5, 0)), w(78, at(9, 0, 5))},
	}
	m.live = []profileInstance{
		{profile: "codex-work", pid: 39021, folder: billing, started: now.Add(-185 * time.Minute), session: instanceSession{title: "Migrate billing webhooks"}},
		{profile: "claude-personal", pid: 48213, folder: ai, started: now.Add(-72 * time.Minute), session: instanceSession{id: "b7e2c1f0-4a3d", title: "Refactor cockpit layout folding", name: "brisk-amber-otter"}},
		{profile: "claude-personal", pid: 51877, folder: notes, started: now.Add(-23 * time.Minute), session: instanceSession{id: "e41b77a0-5d19", title: "Draft release notes for 0.9", name: "quiet-linen-heron"}},
		{profile: "claude-personal", pid: 52110, folder: ai, started: now.Add(-4 * time.Minute), headless: true, session: instanceSession{id: "5a0c93d1-88e2", title: "Wave 2: lint the handoff package", name: "tidy-cobalt-wren"}},
	}
	m.recent = []recordedSession{
		{session: instanceSession{id: "b7e2c1f0-4a3d", title: "Refactor cockpit layout folding"}, folder: ai, lastActive: at(14, 2, 0)},
		{session: instanceSession{id: "9f13aa27-01c2", title: "Handoff brief: trim injected blocks"}, folder: ai, lastActive: at(11, 37, 0)},
		{session: instanceSession{id: "71c0e5aa-3f10", title: "What's next"}, folder: ai, lastActive: at(10, 12, 0)},
		{session: instanceSession{id: "0d4f9b62-c7e8", title: "What's next"}, folder: billing, lastActive: at(9, 48, 0)},
		{session: instanceSession{id: "3c9e0d12-7b44", title: "Investigate SQLITE_BUSY on resume"}, folder: ai, lastActive: at(20, 0, -1)},
		{session: instanceSession{id: "e41b77a0-5d19", title: "Draft release notes for 0.9"}, folder: notes, lastActive: at(18, 0, -1)},
		{session: instanceSession{id: "a2b8d3f1-6e05", title: "Mouse movement auto select with click-through"}, folder: filepath.Join(home, "projects/ranma"), lastActive: at(16, 30, -2)},
		{session: instanceSession{id: "c58e1a07-9b3d", title: "Pin the board frame against the handoff"}, folder: ai, lastActive: at(12, 0, -3)},
	}
	m.lineage = map[string]lineageLink{"3c9e0d12-7b44": {TargetProfile: "codex-work"}}
	counts := [24]int{0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 5, 4, 2, 6, 8, 5, 3, 2, 4, 6, 3, 1, 0, 2}
	m.activity = activity{counts: counts, peak: at(14, 0, 0), total: 66}
	m.log = []logEntry{{kind: statusOK, text: "launched claude-personal in ~/projects/ai-session", at: at(14, 2, 0)}}
	m.followSelection("claude-personal")

	env := integrationEnv{
		onPath:     func(string) bool { return true },
		running:    func(Profile) bool { return true },
		statusLine: func(Profile) statusLineOwner { return statusLineOurs },
	}
	selected, _ := m.selectedProfile()
	m.facts = profileFacts{
		profile:      "claude-personal",
		loaded:       true,
		integrations: integrationsFor(selected, env),
		mcp:          []string{"context7", "github", "sqlite"},
		mcpKnown:     true,
		skills:       5,
		skillsKnown:  true,
		apps:         []appMembership{{name: "shiori", active: true}},
		counts:       sessionCounts{interactive: 41, headless: 3, known: true},
		seconds: map[string]string{
			secondPromptKey(m.recent[2]): "after the folder tree lands",
			secondPromptKey(m.recent[3]): "billing retry backoff",
		},
		incoming: map[string]string{"0d4f9b62-c7e8": "codex-work", "c58e1a07-9b3d": "claude-max"},
	}
	return m
}

// The glance at the design's frame, with the design's data, is the mock's
// "Glance: while the cursor moves" frame. Every row is pinned whole: the
// columns are hand-set in the mock (the 57-column running pane, MODEL at 84,
// the keys at 68, the 14-column folder) and a tidy-up that rounded one would
// move everything right of it.
func TestBoardMatchesTheExpansionDesign(t *testing.T) {
	m := expansionDesignModel(t)
	lines := frameText(m)
	want := []string{
		" ai   most headroom claude-max 78%   ·   lowest claude-personal 7%                                    ~/projects/ai-session  ·  ↑ 3 behind main  U",
		"──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────",
		"  ACCOUNT                            5-HOUR LEFT                                    7-DAY LEFT                                     AUTH    LIVE",
		"",
		"  claude-max            claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 91%  22:05    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 78%  mon      ● ok",
		"  codex-personal        codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 82%  23:10    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 64%  sat      ● ok",
		"  codex-work            codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 12%  19:55    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 41%  fri      ● ok    ▶ 1",
		"      └ ▶ Migrate billing webhooks   PID 39021 · ~/work/billing · 3h05m",
		"▌ claude-personal       claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 7%   21:40    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 22%  thu      ● ok    ▶ 3",
		"                                     resets 21:40 today  in 7h35m                   resets Thu 24 Sep 09:00  in 18h55m",
		"    NOTE  personal pro plan · you@example.com                                       MODEL opus         CLI ~/.local/bin/claude",
		"    RUNS  claude --dangerously-skip-permissions --model opus                        ENV   CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1",
		"    WITH  status line ● ai's line   openusage · not read   tmux shim ● inert here   HAS   3 mcp  ·  5 skills  ·  app shiori (active)",
		"",
		"    RUNNING HERE  3                                          RECENT   by last message  ·  3 headless hidden",
		"    ▶ Refactor cockpit layout folding  1h12m                 14:02  Refactor cockpit layout folding                   ai-session",
		"      brisk-amber-otter · PID 48213 · ~/projects/ai-session  11:37  Handoff brief: trim injected blocks               ai-session",
		"    ▶ Draft release notes for 0.9  23m                       10:12  What's next  · after the folder tree lands        ai-session",
		"      quiet-linen-heron · PID 51877 · ~/notes                09:48  What's next  · billing retry backoff              billing       ← codex-work",
		"    ◇ Wave 2: lint the handoff package  4m                   yest.  Investigate SQLITE_BUSY on resume                 ai-session    → codex-work",
		"      headless · tidy-cobalt-wren · PID 52110",
		"",
		"    24h ▁▁▁▁▁▁▁▂▂▃▅▄▂▆█▅▃▂▄▆▃▂▁▂  66 sessions  ·  peak 14:00        R resume   H hand off   h open live   tab sheet",
		"",
		"",
		"  NO LOCAL QUOTA CACHE   these providers keep none this launcher reads",
		"  antigravity-personal  antigravity  · not reported                                                                                ○ login",
		"  deepseek              deepseek     · not reported                                                                                ● key",
		"  opencode-go           opencode     · not reported                                                                                ○ login",
	}
	compareFrame(t, lines, want)
	if row := lines[37]; row != "  ✓ launched claude-personal in ~/projects/ai-session   14:02" {
		t.Errorf("status row moved: %q", row)
	}
}

// The sheet is the mock's "Sheet: tab opens it in place" frame. Two rows
// depart from it on purpose (doc/tui.md, "Where it departs from the
// handoff"): the hour axis labels the hours the bars really are — the window
// ends at the current hour, so at 14:05 the sixes fall at 18, 00, 06 and 12
// rather than at the row's start — and nothing else.
func TestSheetMatchesTheExpansionDesign(t *testing.T) {
	m := expansionDesignModel(t)
	model, _ := m.updateList(tea.KeyMsg{Type: tea.KeyTab})
	m = model.(tuiModel)
	if !m.sheet {
		t.Fatal("tab did not open the sheet")
	}
	lines := frameText(m)
	want := []string{
		" ai   most headroom claude-max 78%   ·   lowest claude-personal 7%                                    ~/projects/ai-session  ·  ↑ 3 behind main  U",
		"──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────",
		"  ACCOUNT                            5-HOUR LEFT                                    7-DAY LEFT                                     AUTH    LIVE",
		"",
		"  claude-max            claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 91%  22:05    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 78%  mon      ● ok",
		"  codex-personal        codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 82%  23:10    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 64%  sat      ● ok",
		"  codex-work            codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 12%  19:55    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 41%  fri      ● ok    ▶ 1",
		"▌ claude-personal       claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 7%   21:40    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 22%  thu      ● ok    ▶ 3",
		"                                     resets 21:40 today  in 7h35m                   resets Thu 24 Sep 09:00  in 18h55m",
		"",
		"    LAUNCH   in the order a launch uses it                          INTEGRATIONS   as I checks them     IN THE PROFILE",
		"    NOTE     personal pro plan · you@example.com                    status line ● ai's line             mcp servers 3  context7 · github · sqlite",
		"    ENV      CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1                 openusage   · not read              skills      5",
		"    COMMAND  claude --dangerously-skip-permissions --model opus     tmux shim   ● on · inert here       app         shiori · active member",
		"    CLI      ~/.local/bin/claude                                                only from a ranma pane  auth        ● ok   file present",
		"    MODEL    opus   from args",
		"    STATE    ~/.config/ai/profiles/claude-personal                              ▶ locked while running",
		"",
		"    RUNNING HERE  3                                          RECENT   by last message  ·  showing 8 of 41  ·  3 headless hidden",
		"    ▶ Refactor cockpit layout folding  1h12m                 14:02   Refactor cockpit layout folding                ~/…/ai-session",
		"      brisk-amber-otter · PID 48213                          11:37   Handoff brief: trim injected blocks            ~/…/ai-session",
		"      ~/projects/ai-session · b7e2c1f0                       10:12   What's next  · after the folder tree lands     ~/…/ai-session",
		"    ▶ Draft release notes for 0.9  23m                       09:48   What's next  · billing retry backoff           ~/work/billing  ← codex-work",
		"      quiet-linen-heron · PID 51877                          yest.   Investigate SQLITE_BUSY on resume              ~/…/ai-session  → codex-work",
		"      ~/notes · e41b77a0                                     yest.   Draft release notes for 0.9                    ~/notes",
		"    ◇ Wave 2: lint the handoff package  4m                   21 Sep  Mouse movement auto select with click-through  ~/projects/ranma",
		"      headless · tidy-cobalt-wren · PID 52110                20 Sep  Pin the board frame against the handoff        ~/…/ai-session  ← claude-max",
		"      ~/projects/ai-session · 5a0c93d1",
		"    24h ▁▁▁▁▁▁▁▂▂▃▅▄▂▆█▅▃▂▄▆▃▂▁▂  66 sessions  ·  peak 14:00        R resume   H hand off   h open live   I integrations   tab less",
		"           18    00    06    12",
		"",
		"  NO LOCAL QUOTA CACHE   antigravity-personal · deepseek · opencode-go  ↓",
	}
	compareFrame(t, lines, want)

	model, _ = m.updateList(tea.KeyMsg{Type: tea.KeyTab})
	if model.(tuiModel).sheet {
		t.Fatal("tab again did not fold the sheet back")
	}
}

func compareFrame(t *testing.T, lines, want []string) {
	t.Helper()
	if len(lines) != boardMaxHeight {
		t.Fatalf("frame is %d rows, want %d", len(lines), boardMaxHeight)
	}
	home, _ := os.UserHomeDir()
	for row, line := range want {
		if got := strings.ReplaceAll(lines[row], home, "~"); got != line {
			t.Errorf("row %d\n got: %q\nwant: %q", row, got, line)
		}
	}
}
