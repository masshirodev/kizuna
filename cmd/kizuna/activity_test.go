package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSessionLog puts a transcript on disk with a chosen modification time,
// which is the only thing profileActivity reads.
func writeSessionLog(t *testing.T, path string, written time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
}

func TestProfileActivityBucketsSessionsByHour(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Now().Truncate(time.Hour)
	sessions := filepath.Join(root, appName, "profiles", "codex-work", "codex", "sessions", "2026", "08", "10")
	// Two sessions three hours ago, one five hours ago, one well outside the day.
	writeSessionLog(t, filepath.Join(sessions, "rollout-a.jsonl"), now.Add(-3*time.Hour))
	writeSessionLog(t, filepath.Join(sessions, "rollout-b.jsonl"), now.Add(-3*time.Hour))
	writeSessionLog(t, filepath.Join(sessions, "rollout-c.jsonl"), now.Add(-5*time.Hour))
	writeSessionLog(t, filepath.Join(sessions, "rollout-d.jsonl"), now.Add(-40*time.Hour))

	got := profileActivity(Profile{Name: "codex-work", Provider: "codex"}, now)
	if got.total != 3 {
		t.Fatalf("total = %d, want the three sessions inside the day", got.total)
	}
	// Bucket 23 is the hour in progress, so three hours ago is bucket 20.
	if got.counts[20] != 2 || got.counts[18] != 1 {
		t.Fatalf("counts = %v, want two at bucket 20 and one at bucket 18", got.counts)
	}
	if !got.peak.Equal(now.Add(-3 * time.Hour)) {
		t.Fatalf("peak = %s, want the busiest hour", got.peak)
	}
}

func TestProfileActivityReadsClaudeTranscripts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	now := time.Now().Truncate(time.Hour)
	projects := filepath.Join(root, appName, "profiles", "claude-personal", "claude", "projects", "-work-hub")
	writeSessionLog(t, filepath.Join(projects, "aaa.jsonl"), now.Add(-time.Hour))

	got := profileActivity(Profile{Name: "claude-personal", Provider: "claude"}, now)
	if got.total != 1 || got.counts[22] != 1 {
		t.Fatalf("activity = %+v, want one session in the previous hour", got)
	}
}

func TestProfileActivityIsUnknownForProvidersWithoutLogs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := profileActivity(Profile{Name: "opencode-go", Provider: "opencode"}, time.Now()); got.known() {
		t.Fatalf("activity = %+v, want nothing for a provider that records no sessions", got)
	}
}

// A single session in an otherwise quiet hour has to clear the baseline, or a
// day with one busy hour reads as a day with no activity at all.
func TestSparklineLiftsEveryNonZeroHourOffTheBaseline(t *testing.T) {
	got := sparkline([]int{0, 1, 5, 10})
	runes := []rune(got)
	if len(runes) != 4 {
		t.Fatalf("sparkline = %q, want one cell per hour", got)
	}
	levels := []rune(sparkLevels)
	if runes[0] != levels[0] {
		t.Fatalf("an empty hour drew %q, want the baseline tick", string(runes[0]))
	}
	if runes[1] == levels[0] {
		t.Fatalf("a busy hour drew the baseline tick: %q", got)
	}
	if runes[3] != levels[len(levels)-1] {
		t.Fatalf("the peak hour drew %q, want a full cell", string(runes[3]))
	}
	if !(runes[1] < runes[2] && runes[2] < runes[3]) {
		t.Fatalf("sparkline is not monotonic in the counts: %q", got)
	}
}

func TestSparklineDrawsAQuietDayAsABaseline(t *testing.T) {
	got := sparkline([]int{0, 0, 0})
	if got != strings.Repeat(string([]rune(sparkLevels)[0]), 3) {
		t.Fatalf("sparkline = %q, want a flat baseline", got)
	}
}
