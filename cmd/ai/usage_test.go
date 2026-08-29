package main

import (
	"fmt"
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
