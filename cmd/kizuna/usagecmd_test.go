package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var usageNow = time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)

// usageFixture lays out one profile tree holding a current Claude reading, a
// stale one, one stale only by age, a Codex reading and an OpenCode profile,
// which keeps no quota cache at all.
func usageFixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	stamp := func(at time.Time) string { return at.Format(time.RFC3339Nano) }

	writeProfileFile(t, root, fmt.Sprintf(`{"cachedUsageUtilization":{"fetchedAtMs":%d,"utilization":{"limits":[
		{"kind":"session","group":"session","percent":40,"resets_at":%q},
		{"kind":"weekly_all","group":"weekly","percent":10,"resets_at":%q}
	]}}}`, usageNow.Add(-10*time.Minute).UnixMilli(), stamp(usageNow.Add(2*time.Hour)), stamp(usageNow.Add(72*time.Hour))),
		"fresh", "claude", ".claude.json")

	// The reading max showed on 2026-11-23: 70% used against a reset that had
	// passed thirteen days before.
	writeProfileFile(t, root, fmt.Sprintf(`{"cachedUsageUtilization":{"fetchedAtMs":%d,"utilization":{"limits":[
		{"kind":"weekly_all","group":"weekly","percent":70,"resets_at":%q}
	]}}}`, usageNow.Add(-17*24*time.Hour).UnixMilli(), stamp(usageNow.Add(-13*24*time.Hour))),
		"stale", "claude", ".claude.json")

	// A five-hour window with no reset time, read a month ago: it has rolled
	// over since, whatever its reset was.
	writeProfileFile(t, root, fmt.Sprintf(`{"cachedUsageUtilization":{"fetchedAtMs":%d,"utilization":{"limits":[
		{"kind":"session","group":"session","percent":0,"resets_at":null}
	]}}}`, usageNow.Add(-30*24*time.Hour).UnixMilli()),
		"aged", "claude", ".claude.json")

	writeProfileFile(t, root, fmt.Sprintf(`{"timestamp":%q,"payload":{"rate_limits":{"primary":{"used_percent":25,"window_minutes":300,"resets_at":%d}}}}`,
		stamp(usageNow.Add(-time.Hour)), usageNow.Add(time.Hour).Unix()),
		"cx", "codex", "sessions", "s.jsonl")

	return Config{
		Profiles: []Profile{
			{Name: "fresh", Provider: "claude"},
			{Name: "stale", Provider: "claude"},
			{Name: "aged", Provider: "claude"},
			{Name: "cx", Provider: "codex"},
			{Name: "oc", Provider: "opencode"},
		},
		Apps: []App{{Name: "chat", Members: []string{"oc"}, Active: "oc"}},
	}
}

func runUsage(t *testing.T, cfg Config, args ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	err := usageCommand(cfg, args, usageNow, &out)
	if err == nil {
		return out.String(), 0
	}
	var status exitStatus
	if !errors.As(err, &status) {
		t.Fatalf("usage %v: %v", args, err)
	}
	return out.String(), status.code
}

// The JSON is what Kantoku reads before every launch, so its keys, its nulls
// and its exit codes are pinned exactly, not just "contains".
func TestUsageJSONIsTheContract(t *testing.T) {
	cfg := usageFixture(t)
	cases := []struct {
		profile string
		code    int
		want    string
	}{
		{"fresh", usageExitKnown, `{"profile":"fresh","provider":"claude","fetched_at":"2026-11-23T11:50:00Z",` +
			`"five_hour":{"remaining":60,"resets_at":"2026-11-23T14:00:00Z"},` +
			`"weekly":{"remaining":90,"resets_at":"2026-11-26T12:00:00Z"},"stale":false}`},
		{"stale", usageExitStale, `{"profile":"stale","provider":"claude","fetched_at":"2026-11-06T12:00:00Z",` +
			`"five_hour":null,"weekly":{"remaining":null,"resets_at":"2026-11-10T12:00:00Z"},"stale":true}`},
		{"aged", usageExitStale, `{"profile":"aged","provider":"claude","fetched_at":"2026-10-24T12:00:00Z",` +
			`"five_hour":{"remaining":null,"resets_at":null},"weekly":null,"stale":true}`},
		{"cx", usageExitKnown, `{"profile":"cx","provider":"codex","fetched_at":"2026-11-23T11:00:00Z",` +
			`"five_hour":{"remaining":75,"resets_at":"2026-11-23T13:00:00Z"},"weekly":null,"stale":false}`},
		{"oc", usageExitUnknown, `{"profile":"oc","provider":"opencode","fetched_at":null,` +
			`"five_hour":null,"weekly":null,"stale":false}`},
		// An app name answers for its active member, under the member's name.
		{"chat", usageExitUnknown, `{"profile":"oc","provider":"opencode","fetched_at":null,` +
			`"five_hour":null,"weekly":null,"stale":false}`},
	}
	for _, test := range cases {
		out, code := runUsage(t, cfg, test.profile, "--json")
		if code != test.code {
			t.Errorf("%s: exit %d, want %d", test.profile, code, test.code)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(out)); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", test.profile, err, out)
		}
		if compact.String() != test.want {
			t.Errorf("%s:\n got %s\nwant %s", test.profile, compact.String(), test.want)
		}
	}
}

func TestUsageUnknownProfileHasItsOwnExitCode(t *testing.T) {
	cfg := usageFixture(t)
	out, code := runUsage(t, cfg, "nobody", "--json")
	if code != usageExitUnknownProfile || out != "" {
		t.Fatalf("exit %d, output %q; want %d and nothing on stdout", code, out, usageExitUnknownProfile)
	}
	if err := usageCommand(cfg, []string{"a", "b"}, usageNow, &bytes.Buffer{}); err == nil || errors.As(err, new(exitStatus)) {
		t.Fatalf("two names = %v, want a plain usage error (exit 1)", err)
	}
}

func TestUsageWithoutAProfileListsEveryProfile(t *testing.T) {
	cfg := usageFixture(t)
	out, code := runUsage(t, cfg, "--json")
	if code != 0 {
		t.Fatalf("exit %d, want 0: listing every profile is not itself a verdict", code)
	}
	var reports []usageReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, report := range reports {
		names = append(names, report.Profile)
	}
	if got := strings.Join(names, ","); got != "aged,cx,fresh,oc,stale" {
		t.Fatalf("profiles = %s, want every profile sorted", got)
	}

	table, _ := runUsage(t, cfg)
	for _, want := range []string{
		"fresh    claude    60% left, " + formatReset(usageNow, usageNow.Add(2*time.Hour)),
		"stale (reset 10 Nov)",
		"stale (rolled over)",
		"oc       opencode  unknown",
		"17d ago",
		"never",
	} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}
}

func TestUsageTableForOneProfile(t *testing.T) {
	cfg := usageFixture(t)
	out, code := runUsage(t, cfg, "stale")
	if code != usageExitStale || !strings.Contains(out, "stale (reset 10 Nov)") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// A current figure for a window always wins over a stale one, and a stale one
// over nothing, so an out-of-date window reads as stale rather than unknown.
func TestMostConstrainedPrefersCurrentThenStale(t *testing.T) {
	fresh := usageWindow{Known: true, Percent: 40}
	tighter := usageWindow{Known: true, Percent: 10}
	stale := usageWindow{Stale: true, Resets: usageNow}
	cases := []struct {
		current, candidate, want usageWindow
	}{
		{usageWindow{}, stale, stale},
		{stale, usageWindow{}, stale},
		{stale, fresh, fresh},
		{fresh, stale, fresh},
		{fresh, tighter, tighter},
		{tighter, fresh, tighter},
	}
	for _, test := range cases {
		if got := mostConstrained(test.current, test.candidate); got != test.want {
			t.Errorf("mostConstrained(%+v, %+v) = %+v, want %+v", test.current, test.candidate, got, test.want)
		}
	}
}

func TestCodexStaleReadingIsReportedNotDropped(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	passed := usageNow.Add(-2 * time.Hour)
	writeProfileFile(t, root, fmt.Sprintf(`{"payload":{"rate_limits":{"secondary":{"used_percent":80,"window_minutes":10080,"resets_at":%d}}}}`, passed.Unix()),
		"cx", "codex", "sessions", "old.jsonl")

	got := profileUsageRemaining(Profile{Name: "cx", Provider: "codex"}, usageNow)
	if got.Weekly.Known || !got.Weekly.Stale || !got.Weekly.Resets.Equal(passed) {
		t.Fatalf("weekly = %+v, want stale since %s", got.Weekly, passed)
	}
	// An event without a timestamp is dated by its file, the last moment Codex
	// wrote to it.
	if got.FetchedAt.IsZero() {
		t.Fatal("fetched_at is unknown; want the file's modification time")
	}
}

func TestWantsJSON(t *testing.T) {
	if !wantsJSON([]string{"usage", "max", "--json"}) || wantsJSON([]string{"usage", "max"}) {
		t.Fatal("wantsJSON misreads the command line")
	}
}

func TestExitStatusReadsAsItsError(t *testing.T) {
	inner := errors.New("profile or app \"x\" not found")
	if got := (exitStatus{code: 2, err: inner}).Error(); got != inner.Error() {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(exitStatus{code: 2, err: inner}, inner) {
		t.Fatal("exitStatus does not unwrap")
	}
	if got := (exitStatus{code: 3}).Error(); got != "exit status 3" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[time.Duration]string{
		10 * time.Second:    "just now",
		9 * time.Minute:     "9m",
		34 * time.Hour:      "34h",
		17 * 24 * time.Hour: "17d",
	}
	for age, want := range cases {
		if got := formatAge(age); got != want {
			t.Errorf("formatAge(%s) = %q, want %q", age, got, want)
		}
	}
}
