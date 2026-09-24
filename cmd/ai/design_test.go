package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// designModel is the handoff's own sample data — the accounts, figures, reset
// times, instances and conversations the mock is drawn with — so the board can
// be compared with the mock line for line.
func designModel(t *testing.T) tuiModel {
	t.Helper()
	home, _ := os.UserHomeDir()
	now := time.Date(2026, 9, 23, 14, 5, 0, 0, time.Local)
	at := func(hh, mm int, days int) time.Time { return time.Date(2026, 9, 23+days, hh, mm, 0, 0, time.Local) }
	w := func(p int, r time.Time) usageWindow { return usageWindow{Known: true, Percent: p, Resets: r} }
	profiles := sortedProfiles([]Profile{
		{Name: "codex-personal", Provider: "codex", Command: "codex"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
		{Name: "claude-personal", Provider: "claude", Command: "claude", DefaultArgs: []string{"--model", "opus"}, Notes: "personal pro plan"},
		{Name: "claude-max", Provider: "claude", Command: "claude"},
		{Name: "antigravity-personal", Provider: "antigravity", Command: "agy"},
		{Name: "opencode-go", Provider: "opencode", Command: "opencode"},
		{Name: "deepseek", Provider: "deepseek", Command: "deepseek"},
	})
	ai := filepath.Join(home, "projects/ai-session")
	m := tuiModel{profiles: profiles, width: 150, height: 42, loaded: true, workingDir: ai, now: now, update: updateStatus{Known: true, Behind: 3}}
	m.usage = map[string]usageRemaining{
		"codex-personal":  {w(82, at(23, 10, 0)), w(64, at(9, 0, 3))},
		"codex-work":      {w(12, at(19, 55, 0)), w(41, at(9, 0, 2))},
		"claude-personal": {w(7, at(21, 40, 0)), w(22, at(9, 0, 1))},
		"claude-max":      {w(91, at(22, 5, 0)), w(78, at(9, 0, 5))},
	}
	m.live = []profileInstance{
		{profile: "claude-personal", pid: 48213, folder: ai, started: now.Add(-72 * time.Minute), session: instanceSession{title: "Refactor cockpit layout folding"}},
		{profile: "claude-personal", pid: 51877, folder: filepath.Join(home, "notes"), started: now.Add(-23 * time.Minute), session: instanceSession{title: "Draft release notes for 0.9"}},
		{profile: "codex-work", pid: 39021, folder: filepath.Join(home, "work/billing"), started: now.Add(-185 * time.Minute), session: instanceSession{title: "Migrate billing webhooks"}},
	}
	m.recent = []recordedSession{
		{session: instanceSession{id: "b7e2c1f0-4a3d", title: "Refactor cockpit layout folding"}, folder: ai, lastActive: at(14, 2, 0)},
		{session: instanceSession{id: "9f13aa27-01c2", title: "Handoff brief: trim injected blocks"}, folder: ai, lastActive: at(11, 37, 0)},
		{session: instanceSession{id: "3c9e0d12-7b44", title: "Investigate SQLITE_BUSY on resume"}, folder: ai, lastActive: at(20, 0, -1)},
		{session: instanceSession{id: "e41b77a0-5d19", title: "Draft release notes for 0.9"}, folder: filepath.Join(home, "notes"), lastActive: at(18, 0, -1)},
	}
	m.lineage = map[string]lineageLink{"3c9e0d12-7b44": {TargetProfile: "codex-work"}}
	counts := [24]int{0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 5, 4, 2, 6, 8, 5, 3, 2, 4, 6, 3, 1, 0, 2}
	m.activity = activity{counts: counts, peak: at(14, 0, 0), total: 66}
	m.log = []logEntry{{kind: statusOK, text: "launched claude-personal in ~/projects/ai-session", at: at(14, 2, 0)}}
	m.followSelection("claude-personal")
	return m
}

// frameText is the view without its margin, as plain text: the frame the mock
// draws.
func frameText(m tuiModel) []string {
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	lines = lines[1 : len(lines)-1]
	for index, line := range lines {
		runes := []rune(line)
		lines[index] = strings.TrimRight(string(runes[2:len(runes)-2]), " ")
	}
	return lines
}

// The board at the design's frame, with the design's data, is the mock. These
// lines were checked against the handoff's 1a/2a render cell for cell; the
// differences left are data the mock invents (every account logged in, a
// model read from settings) and the unrated accounts, which are listed by name.
// Hand-set widths — the 22 and 13 account columns, the 32-cell gauges, the
// spaced key hints — are what a tidy-up would round away, which is why the
// whole row is pinned rather than a few figures.
func TestBoardMatchesTheDesignFrame(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(deepSeekKeyEnv, "sk-test")
	home, _ := os.UserHomeDir()
	lines := frameText(designModel(t))
	if len(lines) != boardMaxHeight {
		t.Fatalf("frame is %d rows, want %d", len(lines), boardMaxHeight)
	}
	want := map[int]string{
		0:  " ai   most headroom claude-max 78%   ·   lowest claude-personal 7%                                    ~/projects/ai-session  ·  ↑ 3 behind main  U",
		2:  "  ACCOUNT                            5-HOUR LEFT                                    7-DAY LEFT                                     AUTH    LIVE",
		4:  "  claude-max            claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 91%  22:05    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 78%  mon      ○ login",
		5:  "  codex-personal        codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 82%  23:10    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 64%  sat      ○ login",
		6:  "  codex-work            codex        ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 12%  19:55    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 41%  fri      ○ login ▶ 1",
		7:  "      └ ▶ Migrate billing webhooks   PID 39021 · ~/work/billing · 3h05m",
		8:  "▌ claude-personal       claude       ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 7%   21:40    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ 22%  thu      ○ login ▶ 2",
		11: "    RUNNING HERE                                                   RECENT",
		12: "    ▶ Refactor cockpit layout folding                              14:02   Refactor cockpit layout folding       ~/projects/ai-session",
		13: "      PID 48213 · ~/projects/ai-session · 1h12m                    11:37   Handoff brief: trim injected blocks   ~/projects/ai-session",
		14: "    ▶ Draft release notes for 0.9                                  yest.   Investigate SQLITE_BUSY on resume     ~/projects/ai-session  → codex-wo",
		17: "    24h ▁▁▁▁▁▁▁▂▂▃▅▄▂▆█▅▃▂▄▆▃▂▁▂  peak 14:00                       R resume   H hand off   h open live",
		20: "  NO LOCAL QUOTA CACHE   these providers keep none this launcher reads",
		21: "  antigravity-personal  antigravity  · not reported                                                                                ○ login",
		22: "  deepseek              deepseek     · not reported                                                                                ● key",
		23: "  opencode-go           opencode     · not reported                                                                                ○ login",
		37: "  ✓ launched claude-personal in ~/projects/ai-session   14:02",
		39: "  ↵ run   R resume   H hand off   p args   / find                                                                     space all actions   ? keys",
	}
	for row, line := range want {
		if got := strings.ReplaceAll(lines[row], home, "~"); got != line {
			t.Errorf("row %d\n got: %q\nwant: %q", row, got, line)
		}
	}
}
