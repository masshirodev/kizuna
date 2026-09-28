//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const compactTestSchema = `
CREATE TABLE session (id text PRIMARY KEY, title text NOT NULL);
CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, data text NOT NULL);
CREATE TABLE event_sequence (aggregate_id text PRIMARY KEY, seq integer NOT NULL);
CREATE TABLE event (id text PRIMARY KEY, aggregate_id text NOT NULL REFERENCES event_sequence(aggregate_id) ON DELETE CASCADE, data text NOT NULL);
`

func compactFixture(t *testing.T) (Config, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	store := filepath.Join(workdir, "data", "opencode", "opencode.db")
	fat := strings.Repeat("diff ", 20000)
	writeRawDB(t, store, compactTestSchema,
		`INSERT INTO session VALUES ('ses_a', 'kept')`,
		`INSERT INTO message VALUES ('msg_a', 'ses_a', 'hello')`,
		`INSERT INTO event_sequence VALUES ('ses_a', 3)`,
		`INSERT INTO event VALUES ('ev1', 'ses_a', '`+fat+`')`,
		`INSERT INTO event VALUES ('ev2', 'ses_a', '`+fat+`')`,
		`INSERT INTO event VALUES ('ev3', 'ses_a', '`+fat+`')`)
	return Config{Profiles: []Profile{profile}}, store
}

func TestCompactDropsTheEventLogKeepsConversationsAndBacksUp(t *testing.T) {
	_, store := compactFixture(t)
	before := storeSize(store)
	report, err := compactOpenCodeStore(store, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "event"); got != 0 {
		t.Fatalf("%d event rows survived", got)
	}
	if got := countRows(t, store, "event_sequence"); got != 0 {
		t.Fatalf("%d event_sequence rows survived", got)
	}
	if countRows(t, store, "session") != 1 || countRows(t, store, "message") != 1 {
		t.Fatal("compaction touched the conversations")
	}
	if got := queryCell(t, store, `PRAGMA integrity_check`); got != "ok" {
		t.Fatalf("integrity_check = %q", got)
	}
	if after := storeSize(store); after >= before/2 {
		t.Fatalf("store is %d bytes after compaction, from %d: the vacuum did not reclaim", after, before)
	}
	backup := store + ".20260928T120000Z.bak"
	if got := countRows(t, backup, "event"); got != 3 {
		t.Fatalf("backup holds %d events, want the untouched 3", got)
	}
	if !strings.Contains(report, backup) {
		t.Fatalf("report %q does not name the backup", report)
	}
}

func TestCompactRefusesWhileAnInstanceRuns(t *testing.T) {
	cfg, store := compactFixture(t)
	lockDir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(store))), instancesDirectory, "run-live")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, ".active.lock"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compactCommand(cfg, []string{"oc"}, &bytes.Buffer{}); err == nil {
		t.Fatal("compacted a store with a live instance")
	}
	if got := countRows(t, store, "event"); got != 3 {
		t.Fatalf("refused compaction still removed events: %d left", got)
	}
}

// The way the waves ran: opencode started with the profile's XDG paths but
// not through ai, so no lock names it.
func TestCompactRefusesAProcessUsingTheStoreDirectly(t *testing.T) {
	cfg, store := compactFixture(t)
	dataHome := filepath.Dir(filepath.Dir(store))
	direct := exec.Command("sleep", "30")
	direct.Env = append(os.Environ(), "XDG_DATA_HOME="+dataHome)
	if err := direct.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = direct.Process.Kill(); _ = direct.Wait() })

	err := compactCommand(cfg, []string{"oc"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "started outside ai") {
		t.Fatalf("compact with a direct user = %v, want refused", err)
	}
	_ = direct.Process.Kill()
	_ = direct.Wait()
	var out bytes.Buffer
	if err := compactCommand(cfg, []string{"oc"}, &out); err != nil {
		t.Fatalf("compact once the process is gone: %v", err)
	}
	if !strings.Contains(out.String(), "oc: ") {
		t.Fatalf("report = %q", out.String())
	}
}

func TestCompactRefusesAProfileWithoutAnOpenCodeStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	cfg := Config{Profiles: []Profile{{Name: "cl", Provider: "claude", Command: "claude"}}}
	if err := compactCommand(cfg, []string{"cl"}, &bytes.Buffer{}); err == nil {
		t.Fatal("compacted a claude profile")
	}
}
