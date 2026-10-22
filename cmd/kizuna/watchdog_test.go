package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWatchdog answers a check the way claude, ranma and the process table
// would: state is what the listing reports for the agent ("" leaves it out of
// the listing), alive whether its lock is still held, and typed collects every
// line the check typed.
type fakeWatchdog struct {
	state  string
	alive  bool
	now    time.Time
	typed  []string
	failTo error
	// during runs inside the listing, to land a change mid-check.
	during func()
}

func (f *fakeWatchdog) probe() watchdogProbe {
	return watchdogProbe{
		peers: func(Config) []peer {
			if f.during != nil {
				f.during()
			}
			if f.state == "" {
				return nil
			}
			return []peer{{ID: "max/run-1", Profile: "max", Provider: "claude", PID: 42, State: f.state, Pane: "7"}}
		},
		alive: func(string) bool { return f.alive },
		typeLine: func(p peer, line string) error {
			if f.failTo != nil {
				return f.failTo
			}
			f.typed = append(f.typed, line)
			return nil
		},
		now: func() time.Time { return f.now },
	}
}

var watchdogCfg = Config{Profiles: []Profile{{Name: "max", Provider: "claude", Command: "claude"}}}

func armTestWatchdog(t *testing.T, entry watchdogEntry) {
	t.Helper()
	if entry.ID == "" {
		entry.ID = "max/run-1"
	}
	if entry.PID == 0 {
		entry.PID = 42
	}
	if entry.Idle == "" {
		entry.Idle = "30m"
	}
	if entry.Max == 0 {
		entry.Max = watchdogDefaultMax
	}
	if entry.ArmedAt.IsZero() {
		entry.ArmedAt = time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	}
	if entry.Text == "" {
		entry.Text = "check the background job"
	}
	if err := updateWatchdogState(func(state *watchdogState) error {
		state.Agents[entry.ID] = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func watchdogEntryNamed(t *testing.T, id string) (watchdogEntry, bool) {
	t.Helper()
	state, err := readWatchdogState()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := state.Agents[id]
	return entry, ok
}

func runWatchdogCheck(t *testing.T, fake *fakeWatchdog) string {
	t.Helper()
	var out bytes.Buffer
	if err := watchdogCheck(watchdogCfg, fake.probe(), &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestWatchdogWaitsForTheThresholdThenNudgesOnce(t *testing.T) {
	isolatePeers(t)
	armTestWatchdog(t, watchdogEntry{})
	start := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	fake := &fakeWatchdog{state: "idle", alive: true, now: start}

	// The first check to see it idle starts the clock; nothing is typed.
	runWatchdogCheck(t, fake)
	fake.now = start.Add(29 * time.Minute)
	runWatchdogCheck(t, fake)
	if len(fake.typed) != 0 {
		t.Fatalf("typed %q before the threshold, want nothing", fake.typed)
	}
	entry, _ := watchdogEntryNamed(t, "max/run-1")
	if !entry.IdleSince.Equal(start) || entry.LastState != "idle" {
		t.Fatalf("entry = %+v, want idle since the first idle check", entry)
	}

	fake.now = start.Add(31 * time.Minute)
	out := runWatchdogCheck(t, fake)
	if len(fake.typed) != 1 || !strings.HasPrefix(fake.typed[0], watchdogLinePrefix) || !strings.Contains(fake.typed[0], "check the background job") {
		t.Fatalf("typed %q, want one watchdog line carrying the armed text", fake.typed)
	}
	if !strings.Contains(out, "nudged (1 of 3)") {
		t.Fatalf("check said %q, want it to report the nudge", out)
	}
	entry, _ = watchdogEntryNamed(t, "max/run-1")
	if entry.Nudges != 1 || entry.Consecutive != 1 || !entry.LastNudge.Equal(fake.now) || !entry.IdleSince.Equal(fake.now) {
		t.Fatalf("entry = %+v, want one nudge recorded and the idle clock restarted from it", entry)
	}

	// The clock restarted at the nudge, so the next check does not nudge again.
	fake.now = start.Add(33 * time.Minute)
	runWatchdogCheck(t, fake)
	if len(fake.typed) != 1 {
		t.Fatalf("typed %d lines, want still one: the clock restarts from the nudge", len(fake.typed))
	}
}

func TestWatchdogNeverTypesIntoABusyAgentAndBusyResetsTheCount(t *testing.T) {
	isolatePeers(t)
	long := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	armTestWatchdog(t, watchdogEntry{IdleSince: long, Consecutive: 2, Nudges: 2, Exhausted: false})
	fake := &fakeWatchdog{state: "busy", alive: true, now: long.Add(6 * time.Hour)}

	runWatchdogCheck(t, fake)
	if len(fake.typed) != 0 {
		t.Fatalf("typed %q into a busy agent", fake.typed)
	}
	entry, _ := watchdogEntryNamed(t, "max/run-1")
	if entry.Consecutive != 0 || !entry.IdleSince.IsZero() || entry.LastState != "busy" || entry.Nudges != 2 {
		t.Fatalf("entry = %+v, want the run of nudges reset, the idle clock stopped, and the total kept", entry)
	}
}

func TestWatchdogNeverTypesWhenTheStateIsUnknown(t *testing.T) {
	isolatePeers(t)
	long := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	for _, state := range []string{"unknown-to-kizuna", "waiting"} {
		armTestWatchdog(t, watchdogEntry{IdleSince: long})
		fake := &fakeWatchdog{state: state, alive: true, now: long.Add(6 * time.Hour)}
		runWatchdogCheck(t, fake)
		if len(fake.typed) != 0 {
			t.Fatalf("state %q: typed %q, want nothing", state, fake.typed)
		}
		entry, _ := watchdogEntryNamed(t, "max/run-1")
		if !entry.IdleSince.IsZero() || entry.LastState != "unknown" {
			t.Fatalf("state %q: entry = %+v, want the idle stretch broken", state, entry)
		}
	}
	// Live, but missing from the listing (claude agents timed out): unknown too.
	armTestWatchdog(t, watchdogEntry{IdleSince: long})
	fake := &fakeWatchdog{state: "", alive: true, now: long.Add(6 * time.Hour)}
	runWatchdogCheck(t, fake)
	if entry, ok := watchdogEntryNamed(t, "max/run-1"); len(fake.typed) != 0 || !ok || entry.LastState != "unknown" {
		t.Fatalf("an agent the listing missed: typed %q, entry %+v (kept %v); want it kept, untouched", fake.typed, entry, ok)
	}
}

func TestWatchdogStopsAtTheCapUntilTheAgentIsSeenBusy(t *testing.T) {
	isolatePeers(t)
	start := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	armTestWatchdog(t, watchdogEntry{Idle: "10m", Max: 2, IdleSince: start})
	fake := &fakeWatchdog{state: "idle", alive: true, now: start}

	var out string
	for step := 1; step <= 5; step++ {
		fake.now = start.Add(time.Duration(step) * 11 * time.Minute)
		out += runWatchdogCheck(t, fake)
	}
	if len(fake.typed) != 2 {
		t.Fatalf("typed %d lines over five overdue checks, want the cap of 2", len(fake.typed))
	}
	entry, _ := watchdogEntryNamed(t, "max/run-1")
	if !entry.Exhausted || entry.Consecutive != 2 {
		t.Fatalf("entry = %+v, want it exhausted at 2", entry)
	}
	if !strings.Contains(out, "no more until it is seen busy") {
		t.Fatalf("check said %q, want it to say it gave up", out)
	}
	var list bytes.Buffer
	if err := watchdogList(&list, false, fake.now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), "exhausted") || !strings.Contains(list.String(), "2/2 (2 total)") {
		t.Fatalf("--list = %q, want the entry shown exhausted with its count", list.String())
	}

	// One busy observation re-arms it.
	fake.state = "busy"
	runWatchdogCheck(t, fake)
	fake.state = "idle"
	fake.now = fake.now.Add(time.Minute)
	runWatchdogCheck(t, fake)
	fake.now = fake.now.Add(11 * time.Minute)
	runWatchdogCheck(t, fake)
	if len(fake.typed) != 3 {
		t.Fatalf("typed %d lines, want a third after a busy period reset the cap", len(fake.typed))
	}
}

func TestWatchdogDisarmsAnAgentThatIsGone(t *testing.T) {
	isolatePeers(t)
	armTestWatchdog(t, watchdogEntry{})
	fake := &fakeWatchdog{state: "", alive: false, now: time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)}
	out := runWatchdogCheck(t, fake)
	if _, ok := watchdogEntryNamed(t, "max/run-1"); ok {
		t.Fatal("the entry of an agent that is gone is still armed")
	}
	if !strings.Contains(out, "is gone") {
		t.Fatalf("check said %q, want it to say why it disarmed", out)
	}

	// The same id now held by another process is not the agent it was armed on.
	armTestWatchdog(t, watchdogEntry{PID: 41})
	fake = &fakeWatchdog{state: "idle", alive: true, now: fake.now}
	runWatchdogCheck(t, fake)
	if _, ok := watchdogEntryNamed(t, "max/run-1"); ok {
		t.Fatal("an id held by a different process kept the old watchdog")
	}
}

func TestWatchdogCheckKeepsAnEntryReArmedWhileItRan(t *testing.T) {
	isolatePeers(t)
	long := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	armTestWatchdog(t, watchdogEntry{IdleSince: long})
	rearmed := long.Add(5*time.Hour + 30*time.Minute)
	fake := &fakeWatchdog{state: "idle", alive: true, now: long.Add(6 * time.Hour)}
	fake.during = func() {
		fake.during = nil
		armTestWatchdog(t, watchdogEntry{ArmedAt: rearmed, Text: "the new wait"})
	}
	runWatchdogCheck(t, fake)
	entry, _ := watchdogEntryNamed(t, "max/run-1")
	if entry.Text != "the new wait" || entry.Nudges != 0 || !entry.IdleSince.IsZero() {
		t.Fatalf("entry = %+v, want the re-arm kept and the stale round's result dropped", entry)
	}
}

func TestWatchdogAFailedTypeIsNotCounted(t *testing.T) {
	isolatePeers(t)
	long := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	armTestWatchdog(t, watchdogEntry{IdleSince: long})
	fake := &fakeWatchdog{state: "idle", alive: true, now: long.Add(time.Hour), failTo: fmt.Errorf("no such pane")}
	runWatchdogCheck(t, fake)
	entry, _ := watchdogEntryNamed(t, "max/run-1")
	if entry.Nudges != 0 || entry.LastError == "" || !entry.IdleSince.Equal(long) {
		t.Fatalf("entry = %+v, want no nudge counted, the error kept, and the clock left to retry", entry)
	}
}

func TestWatchdogCheckWithNothingArmedAsksNobody(t *testing.T) {
	isolatePeers(t)
	probe := watchdogProbe{peers: func(Config) []peer { t.Fatal("listed peers with nothing armed"); return nil }}
	var out bytes.Buffer
	if err := watchdogCheck(watchdogCfg, probe, &out); err != nil || out.Len() != 0 {
		t.Fatalf("check = %q, %v; want silence", out.String(), err)
	}
}

func TestWatchdogSelfIsTheInstanceThisRunsIn(t *testing.T) {
	root := isolatePeers(t)
	max := Profile{Name: "max", Provider: "claude", Command: "claude"}
	cfg := Config{Profiles: []Profile{max}}
	lockDir := filepath.Join(root, appName, "profiles", "max", instancesDirectory, "run-1")
	other := filepath.Join(root, appName, "profiles", "max", instancesDirectory, "run-2")
	peers := []peer{
		{ID: "max/run-1", Profile: "max", Provider: "claude", PID: 42, Pane: "7", lockDir: lockDir},
		{ID: "max/run-2", Profile: "max", Provider: "claude", PID: 43, Pane: "8", lockDir: other},
	}
	now := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)

	if _, err := armWatchdog(cfg, peers, watchdogArm{target: "self", idle: 30 * time.Minute}, "", now); err == nil || !strings.Contains(err.Error(), "not running inside") {
		t.Fatalf("self outside any session = %v, want a refusal naming why", err)
	}

	t.Setenv(instanceDirEnv, lockDir)
	t.Setenv(profileNameEnv, "max")
	entry, err := armWatchdog(cfg, peers, watchdogArm{target: "self", idle: 30 * time.Minute, text: "line one\nline two"}, "max/run-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "max/run-1" || entry.LockDir != lockDir || entry.PID != 42 || entry.Max != watchdogDefaultMax {
		t.Fatalf("armed %+v, want this instance with the default cap", entry)
	}
	if entry.Text != "line one line two" {
		t.Fatalf("text = %q, want it folded to one line", entry.Text)
	}
	if summary, err := disarmWatchdog(cfg, func() []peer { return peers }, "self"); err != nil || !strings.Contains(summary, "disarmed for max/run-1") {
		t.Fatalf("disarm self = %q, %v", summary, err)
	}
}

func TestWatchdogRefusesAgentsItCouldNeverNudge(t *testing.T) {
	isolatePeers(t)
	peers := []peer{
		{ID: "opencode3/run-1", Profile: "opencode3", Provider: "opencode", Pane: "3"},
		{ID: "max/run-2", Profile: "max", Provider: "claude", Headless: true},
		{ID: "max/run-3", Profile: "max", Provider: "claude"},
	}
	for target, want := range map[string]string{"opencode3/run-1": "does not report busy", "max/run-2": "headless", "max/run-3": "pane is unknown"} {
		_, err := armWatchdog(Config{}, peers, watchdogArm{target: target, idle: time.Hour}, "", time.Now())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("arm %s = %v, want a refusal saying %q", target, err, want)
		}
	}
	if _, err := armWatchdog(Config{}, peers, watchdogArm{target: "max/run-3", idle: 10 * time.Second}, "", time.Now()); err == nil {
		t.Fatal("a threshold under a minute was accepted")
	}
}

func TestWatchdogOffTakesAStoredIdOfAnAgentThatIsGone(t *testing.T) {
	isolatePeers(t)
	armTestWatchdog(t, watchdogEntry{ID: "max/run-9"})
	summary, err := disarmWatchdog(Config{}, func() []peer { t.Fatal("listed peers for a stored id"); return nil }, "max/run-9")
	if err != nil || !strings.Contains(summary, "disarmed") {
		t.Fatalf("disarm = %q, %v", summary, err)
	}
	if _, ok := watchdogEntryNamed(t, "max/run-9"); ok {
		t.Fatal("still armed")
	}
}

func TestParseWatchdogArm(t *testing.T) {
	arm, err := parseWatchdogArm([]string{"self", "--idle", "1h30m", "--max=5", "check", "the", "job"})
	if err != nil || arm.target != "self" || arm.idle != 90*time.Minute || arm.max != 5 || arm.text != "check the job" {
		t.Fatalf("parsed %+v, %v", arm, err)
	}
	for _, bad := range [][]string{{"self"}, {"--idle", "30m"}, {"self", "--idle", "soon"}, {"self", "--idle", "30m", "--max", "0"}, {"self", "--idle"}} {
		if _, err := parseWatchdogArm(bad); err == nil {
			t.Fatalf("parseWatchdogArm(%q) accepted", bad)
		}
	}
}

func TestWatchdogUnitsCarryThePathAndEscapePercent(t *testing.T) {
	service, timer := watchdogUnits("/home/u/go/bin/kizuna", "/home/u/.local/bin:/odd%dir", "/home/u/.config")
	for _, want := range []string{`ExecStart="/home/u/go/bin/kizuna" watchdog --check`, `Environment="PATH=/home/u/.local/bin:/odd%%dir"`, `Environment="XDG_CONFIG_HOME=/home/u/.config"`, "Type=oneshot"} {
		if !strings.Contains(service, want) {
			t.Fatalf("service lacks %q:\n%s", want, service)
		}
	}
	for _, want := range []string{"OnUnitActiveSec=120s", "WantedBy=timers.target"} {
		if !strings.Contains(timer, want) {
			t.Fatalf("timer lacks %q:\n%s", want, timer)
		}
	}
}

func TestWatchdogInstallWritesTheUnitsAndEnablesTheTimer(t *testing.T) {
	root := isolatePeers(t)
	var calls []string
	original := systemctlUser
	systemctlUser = func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	t.Cleanup(func() { systemctlUser = original })

	if watchdogTimerInstalled() {
		t.Fatal("installed before --install")
	}
	var out bytes.Buffer
	if err := installWatchdogTimer(&out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kizuna-watchdog.service", "kizuna-watchdog.timer"} {
		if _, err := os.Stat(filepath.Join(root, "systemd", "user", name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}
	if strings.Join(calls, "; ") != "daemon-reload; enable --now kizuna-watchdog.timer" || !watchdogTimerInstalled() {
		t.Fatalf("systemctl calls = %q", calls)
	}

	calls = nil
	if err := uninstallWatchdogTimer(&out); err != nil {
		t.Fatal(err)
	}
	if watchdogTimerInstalled() || strings.Join(calls, "; ") != "disable --now kizuna-watchdog.timer; daemon-reload" {
		t.Fatalf("after uninstall: installed=%v calls=%q", watchdogTimerInstalled(), calls)
	}
}

// TestWatchdogCheckTypesThroughTheSendPath runs a check against the real
// collectPeers and typeIntoPane, with a fake `claude agents` reporting the
// instance idle and a fake ranma recording what was typed into which pane.
func TestWatchdogCheckTypesThroughTheSendPath(t *testing.T) {
	root := isolatePeers(t)
	bin := t.TempDir()
	typed := filepath.Join(bin, "typed")
	if err := os.WriteFile(filepath.Join(bin, "ranma"), []byte("#!/bin/sh\necho \"$RANMA_SOCKET $*\" >> "+typed+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	claude := filepath.Join(bin, "fake-claude")
	max := Profile{Name: "max", Provider: "claude", Command: claude}
	lockDir := livePeerInstance(t, root, max, "run-1", "/work/kumiko")
	env := []string{ranmaSocketEnv + "=/run/test/ranma.sock", ranmaPaneEnv + "=14"}
	if err := setProfileInstanceMeta(lockDir, "/work/kumiko", false, env, ""); err != nil {
		t.Fatal(err)
	}
	pid, err := profileLockPID(lockDir)
	if err != nil {
		t.Fatal(err)
	}
	setState := func(state string) {
		script := fmt.Sprintf("#!/bin/sh\necho '[{\"pid\":%d,\"cwd\":\"/work/kumiko\",\"sessionId\":\"s1\",\"name\":\"kumiko-1\",\"status\":\"%s\"}]'\n", pid, state)
		if err := os.WriteFile(claude, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Profiles: []Profile{max}}
	entry, err := armWatchdog(cfg, collectPeers(cfg), watchdogArm{target: "max/run-1", idle: time.Minute, text: "is the tier still running?", max: 3}, "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	probe := liveWatchdogProbe()
	probe.now = func() time.Time { return start }
	check := func() {
		var out bytes.Buffer
		if err := watchdogCheck(cfg, probe, &out); err != nil {
			t.Fatal(err)
		}
	}

	setState("busy")
	check()
	setState("idle")
	check()
	start = start.Add(61 * time.Second)
	check()
	data, err := os.ReadFile(typed)
	if err != nil {
		t.Fatalf("nothing was typed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "/run/test/ranma.sock send -p 14 -e "+watchdogLinePrefix) || !strings.Contains(lines[0], "is the tier still running?") {
		t.Fatalf("ranma was asked %q, want one send into pane 14 on its socket", lines)
	}
	if got, _ := watchdogEntryNamed(t, entry.ID); got.Nudges != 1 {
		t.Fatalf("entry = %+v, want one nudge recorded", got)
	}
}

func TestWatchdogSpanReadsLikeTheFlag(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "1m", 90 * time.Minute: "1h30m", 2 * time.Hour: "2h", 20 * time.Second: "<1m", 61 * time.Second: "1m"} {
		if got := watchdogSpan(d); got != want {
			t.Fatalf("watchdogSpan(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestMCPListsTheWatchdogTools(t *testing.T) {
	isolatePeers(t)
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"watchdog_list","arguments":{}}}` + "\n" +
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"watchdog_arm","arguments":{"idle":"soon"}}}` + "\n"
	var out bytes.Buffer
	if err := mcpServeCommand(Config{}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, want := range []string{`"watchdog_arm"`, `"watchdog_disarm"`, `"watchdog_list"`} {
		if !strings.Contains(lines[0], want) {
			t.Fatalf("tools/list lacks %s", want)
		}
	}
	if !strings.Contains(lines[1], `[]`) || !strings.Contains(lines[2], `"isError":true`) {
		t.Fatalf("responses = %s", out.String())
	}
}
