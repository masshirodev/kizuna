package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRestorableSkipsHeadlessAndManagementLaunches(t *testing.T) {
	claude := Profile{Name: "pro", Provider: "claude"}
	cases := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--resume", "abc"}, true},
		{[]string{"fix the build"}, true},
		{[]string{"-p", "fix the build"}, false},
		{[]string{"mcp", "list"}, false},
	}
	for _, c := range cases {
		if got := restorable(claude, c.args); got != c.want {
			t.Errorf("restorable(%v) = %v, want %v", c.args, got, c.want)
		}
	}
	if restorable(Profile{Name: "oc", Provider: "opencode"}, []string{"run", "x"}) {
		t.Error("opencode run is headless and should not be restored")
	}
}

func TestCutOffKeepsShutdownsAndDropsClosedPanes(t *testing.T) {
	running := func() bool { return false }
	stopping := func() bool { return true }
	if cutOff(nil, stopping) {
		t.Error("a CLI that exited by itself was kept")
	}
	if !cutOff(syscall.SIGTERM, running) {
		t.Error("SIGTERM is a shutdown and should keep the record")
	}
	if cutOff(syscall.SIGHUP, running) {
		t.Error("SIGHUP outside a shutdown is a closed pane and should drop the record")
	}
	if !cutOff(syscall.SIGHUP, stopping) {
		t.Error("SIGHUP while the system stops should keep the record")
	}
}

func TestClaudeSessionOfProcessMatchesThePidOrADescendant(t *testing.T) {
	dir := t.TempDir()
	child := exec.Command("sh", "-c", "sleep 300 & wait")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	write := func(pid int, id, status string) {
		data, _ := json.Marshal(map[string]any{"pid": pid, "sessionId": id, "name": "slug-" + id, "status": status})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(999999, "other", "idle")
	write(child.Process.Pid, "mine", "busy")
	session := claudeSessionOfProcess(dir, child.Process.Pid)
	if session.id != "mine" || session.status != "busy" {
		t.Fatalf("got %+v, want the session of the launched pid", session)
	}

	// The tmux indicator puts a wrapper between the launcher and Claude: the
	// session file then names a grandchild.
	var grandchild int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && grandchild == 0; time.Sleep(20 * time.Millisecond) {
		output, _ := exec.Command("pgrep", "-P", fmt.Sprint(child.Process.Pid)).Output()
		fmt.Sscan(string(output), &grandchild)
	}
	if grandchild == 0 {
		t.Skip("could not find the wrapper's child")
	}
	_ = os.Remove(filepath.Join(dir, fmt.Sprintf("%d.json", child.Process.Pid)))
	write(grandchild, "nested", "idle")
	if session := claudeSessionOfProcess(dir, child.Process.Pid); session.id != "nested" {
		t.Fatalf("got %+v, want the session of the descendant", session)
	}
}

func TestRestoreCandidatesDropsLiveDuplicatesAndOrdersByPlace(t *testing.T) {
	now := time.Now()
	records := []restoreRecord{
		{ID: "pro/run-a", Profile: "pro", Session: "s1", Folder: "/x", RanmaSession: "main", RanmaWorkspace: 3, Updated: now.Add(-time.Minute), path: "a"},
		// The same conversation recorded twice, by two launches: keep the latest.
		{ID: "pro/run-b", Profile: "pro", Session: "s1", Folder: "/x", RanmaSession: "main", RanmaWorkspace: 4, Updated: now, path: "b"},
		// Already reopened: a live launch has the same conversation open.
		{ID: "pro/run-c", Profile: "pro", Session: "s2", Folder: "/y", Updated: now, path: "c"},
		{ID: "pro/run-live", Profile: "pro", Session: "s2", Folder: "/y", Updated: now, path: "live"},
		{ID: "oc/run-d", Profile: "oc", Folder: "/z", RanmaSession: "main", RanmaWorkspace: 1, Updated: now, path: "d"},
	}
	alive := func(record restoreRecord) bool { return record.path == "live" }
	candidates, stale := restoreCandidates(records, alive)
	var got []string
	for _, record := range candidates {
		got = append(got, record.path)
	}
	if strings.Join(got, ",") != "d,b" {
		t.Fatalf("candidates = %v, want d (main:1) then b (main:4)", got)
	}
	var dropped []string
	for _, record := range stale {
		dropped = append(dropped, record.path)
	}
	if strings.Join(dropped, ",") != "a,c" && strings.Join(dropped, ",") != "c,a" {
		t.Fatalf("stale = %v, want a and c", dropped)
	}
}

func TestRecordAliveRefusesAnotherBoot(t *testing.T) {
	self := restoreRecord{PID: os.Getpid(), Boot: "this"}
	if !recordAlive(self, "this") {
		t.Error("a record of a live pid in this boot reads as dead")
	}
	if recordAlive(self, "next") {
		t.Error("a record from another boot reads as alive because its pid was reused")
	}
}

func TestRanmaOpenArgsReturnsToTheOldWorkspace(t *testing.T) {
	record := restoreRecord{Profile: "pro", Provider: "claude", Folder: "/home/u/p", Session: "abc", Title: "Fix it",
		RanmaSession: "main", RanmaWorkspace: 3, RanmaWorkspaceName: "kumiko"}
	command, err := reopenCommand(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(command, "kizuna pro --resume abc; exec ") {
		t.Fatalf("command = %q", command)
	}
	got := strings.Join(ranmaOpenArgs(record, command), " ")
	want := "open --background --cwd /home/u/p --session main --workspace 3 --workspace-name kumiko --name Fix it -- " + command
	if got != want {
		t.Fatalf("args =\n%s\nwant\n%s", got, want)
	}
	record.RanmaSession, record.RanmaWorkspace, record.Title = "", 0, ""
	got = strings.Join(ranmaOpenArgs(record, command), " ")
	if !strings.Contains(got, "--workspace empty") || !strings.Contains(got, "--name p ") {
		t.Fatalf("a launch from outside ranma should land in an empty workspace named after its folder: %s", got)
	}
}

func TestRestoreLedgerKeepsTheRecordOnlyWhenCutOff(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "oc", Provider: "opencode"}
	lockDir := filepath.Join(root, appName, "profiles", "oc", instancesDirectory, "run-1")
	folder := t.TempDir()

	ledger := startRestoreLedger(profile, lockDir, os.Getpid(), folder, nil)
	if ledger == nil {
		t.Fatal("no ledger")
	}
	dir, _ := restoreDir()
	records := readRestoreRecords(dir)
	if len(records) != 1 || records[0].Folder != folder || records[0].ID != "oc/run-1" {
		t.Fatalf("records = %+v", records)
	}
	ledger.finish(true)
	if len(readRestoreRecords(dir)) != 1 {
		t.Fatal("a cut-off launch lost its record")
	}

	ledger = startRestoreLedger(profile, lockDir, os.Getpid(), folder, nil)
	ledger.finish(false)
	if len(readRestoreRecords(dir)) != 0 {
		t.Fatal("a launch that ended on its own kept its record")
	}
}

func TestTrackedLaunchRestoresSessionsButNotLoginsOrHeadlessRuns(t *testing.T) {
	claude := Profile{Name: "pro", Provider: "claude"}
	cmd := exec.Command("true")
	if trackedLaunch(cmd, "", claude, nil, false).restore == nil {
		t.Error("an interactive TUI launch keeps no restore record")
	}
	if trackedLaunch(cmd, "", claude, []string{"-p", "x"}, false).restore != nil {
		t.Error("a headless TUI launch keeps a restore record")
	}
	if trackedLaunch(cmd, "", claude, loginArgs("claude"), true).restore != nil {
		t.Error("a login keeps a restore record")
	}
}

// startTracked runs a TUI launch of `sleep 30` in the background and waits
// until its restore record is on disk, so a test can end it and look at what
// the record became.
func startTracked(t *testing.T) (dir string, child int, result <-chan os.Signal) {
	t.Helper()
	root := isolatePeers(t)
	profile := Profile{Name: "oc", Provider: "opencode"}
	lockDir := filepath.Join(root, appName, "profiles", "oc", instancesDirectory, "run-1")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	cmd.Dir = t.TempDir()
	tracked := trackedLaunch(cmd, lockDir, profile, nil, false)
	done := make(chan os.Signal, 1)
	go func() {
		stoppedBy, _ := tracked.run()
		done <- stoppedBy
	}()
	dir, _ = restoreDir()
	deadline := time.Now().Add(5 * time.Second)
	for len(readRestoreRecords(dir)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a TUI launch wrote no restore record")
		}
		time.Sleep(10 * time.Millisecond)
	}
	records := readRestoreRecords(dir)
	if records[0].Folder != cmd.Dir || records[0].PID != os.Getpid() {
		t.Fatalf("record = %+v, want the launch's folder and the TUI's pid", records[0])
	}
	// The child's pid comes off the lock, written before the record, rather
	// than off cmd.Process, which belongs to the goroutine running it.
	lock, err := os.ReadFile(filepath.Join(lockDir, ".active.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(lock))
	child, err = strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	return dir, child, done
}

func awaitTracked(t *testing.T, done <-chan os.Signal) os.Signal {
	t.Helper()
	select {
	case stoppedBy := <-done:
		return stoppedBy
	case <-time.After(10 * time.Second):
		t.Fatal("the TUI launch never returned")
		return nil
	}
}

func TestTrackedLaunchDropsTheRecordOfASessionThatEnded(t *testing.T) {
	dir, child, done := startTracked(t)
	_ = syscall.Kill(child, syscall.SIGINT)
	if stoppedBy := awaitTracked(t, done); stoppedBy != nil {
		t.Errorf("stoppedBy = %v, want none", stoppedBy)
	}
	if records := readRestoreRecords(dir); len(records) != 0 {
		t.Fatalf("a session that ended on its own kept its record: %+v", records)
	}
}

func TestTrackedLaunchKeepsTheRecordThroughAShutdown(t *testing.T) {
	dir, _, done := startTracked(t)
	// What systemd sends at shutdown. The forwarder holds it for the child,
	// so the test process survives it.
	_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	if stoppedBy := awaitTracked(t, done); stoppedBy != syscall.SIGTERM {
		t.Errorf("stoppedBy = %v, want SIGTERM", stoppedBy)
	}
	if records := readRestoreRecords(dir); len(records) != 1 {
		t.Fatalf("a session cut off by a shutdown lost its record: %+v", records)
	}
}

func TestResumeAllDryRunListsDeadSessionsWithoutTouchingThem(t *testing.T) {
	isolatePeers(t)
	dir, _ := restoreDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	dead := restoreRecord{ID: "pro/run-x", Profile: "pro", Provider: "claude", Folder: folder, Session: "abc",
		State: "busy", Boot: "a-previous-boot", PID: 1, Updated: time.Now(), path: filepath.Join(dir, "pro_run-x.json")}
	if err := writeRestoreRecord(dead); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Profiles: []Profile{{Name: "pro", Provider: "claude"}}}
	var out bytes.Buffer
	if err := resumeAllCommand(cfg, []string{"-n"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "kizuna pro --resume abc") || !strings.Contains(out.String(), "busy") {
		t.Fatalf("output:\n%s", out.String())
	}
	if len(readRestoreRecords(dir)) != 1 {
		t.Fatal("a dry run removed a record")
	}

	out.Reset()
	if err := resumeAllCommand(cfg, []string{"--forget"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(readRestoreRecords(dir)) != 0 {
		t.Fatalf("--forget kept the record: %s", out.String())
	}
}
