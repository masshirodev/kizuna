package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileUsageRemainingKeepsAccountsSeparate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Unix()

	writeProfileFile(t, root, fmt.Sprintf(`{"payload":{"rate_limits":{"primary":{"used_percent":27,"window_minutes":10080,"resets_at":%d}}}}`, future),
		"work", "codex", "sessions", "2026", "08", "11", "work.jsonl")
	writeProfileFile(t, root, fmt.Sprintf(`{"payload":{"rate_limits":{"primary":{"used_percent":1,"window_minutes":10080,"resets_at":%d}}}}`, future),
		"personal", "codex", "sessions", "2026", "08", "11", "personal.jsonl")

	if got := profileUsageRemaining(Profile{Name: "work", Provider: "codex"}, now).Weekly; !got.Known || got.Percent != 73 {
		t.Fatalf("work remaining = %+v, want 73%%", got)
	}
	if got := profileUsageRemaining(Profile{Name: "personal", Provider: "codex"}, now).Weekly; !got.Known || got.Percent != 99 {
		t.Fatalf("personal remaining = %+v, want 99%%", got)
	}
}

func TestCodexUsageUsesLatestEventAndMostConstrainedWindow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	body := fmt.Sprintf("%s\n%s\n",
		fmt.Sprintf(`{"payload":{"rate_limits":{"primary":{"used_percent":90,"window_minutes":300,"resets_at":%d}}}}`, now.Add(-time.Minute).Unix()),
		fmt.Sprintf(`{"payload":{"rate_limits":{"primary":{"used_percent":20,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":65,"window_minutes":10080,"resets_at":%d}}}}`, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix()),
	)
	writeProfileFile(t, root, body, "cx", "codex", "sessions", "session.jsonl")

	got := profileUsageRemaining(Profile{Name: "cx", Provider: "codex"}, now)
	if !got.FiveHour.Known || got.FiveHour.Percent != 80 || !got.Weekly.Known || got.Weekly.Percent != 35 {
		t.Fatalf("remaining = %+v, want 80%% five-hour and 35%% weekly", got)
	}
}

func TestClaudeUsageIgnoresExpiredLimitsAndIncludesWeeklyWindow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{
		"cachedUsageUtilization":{"utilization":{"limits":[
			{"kind":"session","group":"session","percent":100,"resets_at":%q,"is_active":true},
			{"kind":"weekly_all","group":"weekly","percent":87,"resets_at":%q,"is_active":false}
		]}}
	}`, now.Add(-time.Minute).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	writeProfileFile(t, root, body, "cl", "claude", ".claude.json")

	got := profileUsageRemaining(Profile{Name: "cl", Provider: "claude"}, now)
	if got.FiveHour.Known || !got.Weekly.Known || got.Weekly.Percent != 13 {
		t.Fatalf("remaining = %+v, want expired five-hour and 13%% weekly", got)
	}
}

func TestClaudeUsageReportsFiveHourAndWeeklyWindows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	body := fmt.Sprintf(`{
		"cachedUsageUtilization":{"utilization":{"limits":[
			{"kind":"session","group":"session","percent":44,"resets_at":%q},
			{"kind":"weekly_all","group":"weekly","percent":3,"resets_at":%q}
		]}}
	}`, now.Add(time.Hour).Format(time.RFC3339Nano), now.Add(7*24*time.Hour).Format(time.RFC3339Nano))
	writeProfileFile(t, root, body, "cl", "claude", ".claude.json")

	got := profileUsageRemaining(Profile{Name: "cl", Provider: "claude"}, now)
	if !got.FiveHour.Known || got.FiveHour.Percent != 56 || !got.Weekly.Known || got.Weekly.Percent != 97 {
		t.Fatalf("remaining = %+v, want 56%% five-hour and 97%% weekly", got)
	}
}

func TestProfileUsageUnknownForUnsupportedOrStaleProvider(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	writeProfileFile(t, root, `{"cachedUsageUtilization":{"utilization":{"five_hour":{"utilization":80,"resets_at":"2026-08-11T11:00:00Z"}}}}`,
		"cl", "claude", ".claude.json")

	for _, profile := range []Profile{{Name: "cl", Provider: "claude"}, {Name: "oc", Provider: "opencode"}} {
		if got := profileUsageRemaining(profile, now); got.known() {
			t.Fatalf("%s usage unexpectedly known: %+v", profile.Provider, got)
		}
	}
}

// The percentage alone cannot tell a window about to refill from one that has to
// last the rest of the week, so both providers' reset times are carried through.
func TestUsageCarriesWhenEachWindowResets(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Minute)
	used := 82.0
	windows := []*codexRateLimitWindow{{UsedPercent: &used, WindowMinutes: 300, ResetsAt: reset.Unix()}}
	got := codexWindowsRemaining(windows, now)
	if !got.FiveHour.Resets.Equal(reset) {
		t.Fatalf("codex reset = %s, want %s", got.FiveHour.Resets, reset)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	body := `{"cachedUsageUtilization":{"utilization":{"five_hour":{"percent":45,"resets_at":"` +
		reset.Format(time.RFC3339) + `"}}}}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if got := claudeUsageRemaining(path, now); !got.FiveHour.Resets.Equal(reset) {
		t.Fatalf("claude reset = %s, want %s", got.FiveHour.Resets, reset)
	}
}

func TestFormatResetShortensToTheNearestUsefulUnit(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.Local)
	cases := []struct {
		resets time.Time
		want   string
	}{
		{time.Time{}, ""},
		{now.Add(-time.Hour), ""},
		{now.Add(2 * time.Hour), "resets 14:00"},
		{now.Add(3 * 24 * time.Hour), "resets tue"},
		{now.Add(20 * 24 * time.Hour), "resets 18 Sep"},
	}
	for _, test := range cases {
		if got := formatReset(now, test.resets); got != test.want {
			t.Fatalf("formatReset(%s) = %q, want %q", test.resets, got, test.want)
		}
	}
}
