package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseProcLocksKeepsHeldFlocksOnly(t *testing.T) {
	text := `1: POSIX  ADVISORY  WRITE 507258 103:02:18350448 0 EOF
2: FLOCK  ADVISORY  WRITE 1322229 103:02:9001 0 EOF
2: -> FLOCK  ADVISORY  WRITE 1322300 103:02:9001 0 EOF
3: FLOCK  ADVISORY  READ 41 00:1d:77 0 EOF
`
	holders := parseProcLocks(text)
	if len(holders) != 2 || holders["103:02:9001"] != 1322229 || holders["00:1d:77"] != 41 {
		t.Fatalf("holders = %v, want the two flocks and not the waiter or the POSIX lock", holders)
	}
}

// antigravityPresenceFile makes a presence lock in an isolated Antigravity
// profile's home, as agy lays them out.
func antigravityPresenceFile(t *testing.T, root string, profile Profile, id string) string {
	t.Helper()
	dir := filepath.Join(root, appName, "profiles", profile.Name, "home", ".gemini", "antigravity-cli", "presence")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHeldAntigravityConversationIsReadFromTheKernel takes a real flock the
// way agy does and reads it back through /proc/locks.
func TestHeldAntigravityConversationIsReadFromTheKernel(t *testing.T) {
	if _, err := os.Stat("/proc/locks"); err != nil {
		t.Skip("no /proc/locks on this system")
	}
	root := isolatePeers(t)
	profile := Profile{Name: "gemini", Provider: "antigravity", Command: "agy"}
	held := antigravityPresenceFile(t, root, profile, "conv-open")
	antigravityPresenceFile(t, root, profile, "conv-closed")

	file, err := os.Open(held)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	if pid := antigravityConversationHolder(profile, "conv-open"); pid != os.Getpid() {
		t.Fatalf("holder of the locked conversation = %d, want this process %d", pid, os.Getpid())
	}
	if pid := antigravityConversationHolder(profile, "conv-closed"); pid != 0 {
		t.Fatalf("a conversation nobody has open reads as held by %d", pid)
	}
	if err := errAntigravityConversationOpen(os.Getpid()); !strings.Contains(err.Error(), "close it there first") {
		t.Fatalf("refusal %q should say what to do", err)
	}

	// The test's parent stands for an outer instance (a Claude session whose
	// shell started agy): the nearer instance, this process, gets it.
	parent, _ := parentPID(os.Getpid())
	instances := []profileInstance{{pid: parent}, {pid: os.Getpid()}}
	describeAntigravityInstances(profile, instances)
	if instances[1].session.id != "conv-open" || instances[0].session.id != "" {
		t.Fatalf("instances = %+v, want the conversation on the nearest instance up the holder's chain", instances)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if pid := antigravityConversationHolder(profile, "conv-open"); pid != 0 {
		t.Fatalf("a released lock still reads as held by %d", pid)
	}
}

func TestAntigravityInstanceIsNotHijacked(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{
		profiles:  []Profile{{Name: "gemini", Provider: "antigravity", Command: "agy"}},
		mode:      tuiHijack,
		instances: []profileInstance{{pid: 11, folder: "/work/kata", session: instanceSession{id: "conv-open"}}},
	}
	next, cmd := m.updateHijack(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("hijacking an Antigravity instance should launch nothing")
	}
	if got := next.(tuiModel); got.mode != tuiList || !strings.Contains(got.status, "cannot be hijacked") {
		t.Fatalf("mode = %v, status = %q, want the list with the refusal", got.mode, got.status)
	}
}
