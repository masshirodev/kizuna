package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// jsonMarshal is encoding/json.Marshal under a name short enough for the
// seed-receipt writes that repeat in nearly every test here.
func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

// openCodeTestSchema is the subset of the real opencode.db schema the merge
// touches: the session subtree, one project stub, the auth tables with their
// time_updated ordering, and one denied table.
const openCodeTestSchema = `
CREATE TABLE session (id text PRIMARY KEY, title text NOT NULL, directory text NOT NULL, time_created integer NOT NULL);
CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL);
CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, data text NOT NULL);
CREATE TABLE project (id text PRIMARY KEY, worktree text NOT NULL);
CREATE TABLE account (id text PRIMARY KEY, email text NOT NULL, time_updated integer NOT NULL);
CREATE TABLE account_state (id integer PRIMARY KEY, active_account_id text);
CREATE TABLE event (id text PRIMARY KEY, aggregate_id text NOT NULL);
`

func writeTestDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(openCodeTestSchema); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

func countRows(t *testing.T, dbPath, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM "` + table + `"`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func queryCell(t *testing.T, dbPath, query string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cell string
	if err := db.QueryRow(query).Scan(&cell); err != nil {
		t.Fatal(err)
	}
	return cell
}

func profileWorkdir(t *testing.T, root, name string) string {
	t.Helper()
	workdir := filepath.Join(root, appName, "profiles", name)
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	return workdir
}

func TestIsolatedSeedCopiesStoreAndMarksReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	writeTestDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`)
	if err := os.MkdirAll(filepath.Join(workdir, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "data", "opencode", "auth.json"), []byte(`{"tok":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}

	instanceDir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if !isIsolatedInstanceDir(instanceDir) {
		t.Fatal("instance carries no seed receipt")
	}
	if countRows(t, filepath.Join(instanceDir, "data", "opencode", "opencode.db"), "session") != 1 {
		t.Fatal("instance store was not seeded from the profile")
	}
	if _, err := os.Stat(filepath.Join(instanceDir, "data", "opencode", "auth.json")); err != nil {
		t.Fatal("auth.json was not seeded:", err)
	}

	joined := strings.Join(launchEnvironment(profile, workdir, instanceDir, []string{"PATH=/bin"}), "\n")
	for _, want := range []string{
		"XDG_CONFIG_HOME=" + filepath.Join(workdir, "config"),
		"XDG_DATA_HOME=" + filepath.Join(instanceDir, "data"),
		"XDG_STATE_HOME=" + filepath.Join(instanceDir, "state"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("isolated environment is missing %q:\n%s", want, joined)
		}
	}

	if note := unlock(); note != "" {
		t.Fatalf("clean merge reported %q, want silence", note)
	}
	if _, err := os.Stat(instanceDir); !os.IsNotExist(err) {
		t.Fatal("instance directory survived its merge")
	}
}

func TestMergeBringsNewSessionsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	profileDB := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeTestDB(t, profileDB,
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`,
		`INSERT INTO message VALUES ('msg-a', 'ses-a', 1000)`)

	seed := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, "data", "opencode"), 0700); err != nil {
			t.Fatal(err)
		}
		writeTestDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
			`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`,
			`INSERT INTO session VALUES ('ses-b', 'second', '/work', 2000)`,
			`INSERT INTO message VALUES ('msg-a', 'ses-a', 1000)`,
			`INSERT INTO message VALUES ('msg-b', 'ses-b', 2000)`,
			`INSERT INTO project VALUES ('proj-1', '/work')`)
		info, _ := jsonMarshal(isolatedSeedInfo{})
		if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	first := filepath.Join(workdir, instancesDirectory, "run-first")
	seed(first)
	if note := retireIsolatedInstance(workdir, profile, first); !strings.Contains(note, "1 session") {
		t.Fatalf("merge note = %q, want the new session announced", note)
	}
	if got := countRows(t, profileDB, "session"); got != 2 {
		t.Fatalf("profile holds %d sessions, want 2", got)
	}
	if got := countRows(t, profileDB, "message"); got != 2 {
		t.Fatalf("profile holds %d messages, want 2", got)
	}
	if got := countRows(t, profileDB, "project"); got != 1 {
		t.Fatalf("profile holds %d projects, want the new stub", got)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("merged instance directory survived")
	}

	// A retry over the same content merges nothing and changes nothing.
	second := filepath.Join(workdir, instancesDirectory, "run-second")
	seed(second)
	if note := retireIsolatedInstance(workdir, profile, second); note != "" {
		t.Fatalf("second merge reported %q, want silence", note)
	}
	if got := countRows(t, profileDB, "session"); got != 2 {
		t.Fatalf("profile holds %d sessions after re-merge, want 2", got)
	}
}

func TestMergeSkipsDriftedSessionTableWithoutAnnouncing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	writeTestDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`)

	dir := filepath.Join(workdir, instancesDirectory, "run-drift")
	if err := os.MkdirAll(filepath.Join(dir, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "data", "opencode", "opencode.db")
	writeTestDB(t, dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// A newer opencode that added a column: same table, different shape.
	if _, err := db.Exec(`ALTER TABLE session ADD COLUMN extra text`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session VALUES ('ses-b', 'second', '/work', 2000, 'x')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	note := retireIsolatedInstance(workdir, profile, dir)
	if strings.Contains(note, "session") && !strings.Contains(note, "skipped") {
		t.Fatalf("merge announced sessions it did not merge: %q", note)
	}
	if got := countRows(t, filepath.Join(workdir, "data", "opencode", "opencode.db"), "session"); got != 1 {
		t.Fatalf("profile holds %d sessions, want only its own", got)
	}
}

func TestMergeAuthTablesPreferNewerAndNeverTouchDenied(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	profileDB := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeTestDB(t, profileDB,
		`INSERT INTO account VALUES ('acc-1', 'new@example.com', 200)`,
		`INSERT INTO account_state VALUES (1, 'acc-1')`,
		`INSERT INTO event VALUES ('ev-1', 'agg-1')`)

	dir := filepath.Join(workdir, instancesDirectory, "run-auth")
	if err := os.MkdirAll(filepath.Join(dir, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	// Older credential, switched pointer, and a consumed event: none cross.
	writeTestDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
		`INSERT INTO account VALUES ('acc-1', 'old@example.com', 100)`,
		`INSERT INTO account_state VALUES (1, 'acc-2')`,
		`INSERT INTO event VALUES ('ev-2', 'agg-2')`)
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	retireIsolatedInstance(workdir, profile, dir)

	if got := queryCell(t, profileDB, `SELECT email FROM account WHERE id='acc-1'`); got != "new@example.com" {
		t.Fatalf("older credential overwrote the profile's: %q", got)
	}
	if got := queryCell(t, profileDB, `SELECT active_account_id FROM account_state WHERE id=1`); got != "acc-1" {
		t.Fatalf("account pointer crossed: %q", got)
	}
	if got := countRows(t, profileDB, "event"); got != 1 {
		t.Fatalf("consumed event crossed: %d rows", got)
	}

	// A newer refresh does cross.
	dir2 := filepath.Join(workdir, instancesDirectory, "run-auth2")
	if err := os.MkdirAll(filepath.Join(dir2, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestDB(t, filepath.Join(dir2, "data", "opencode", "opencode.db"),
		`INSERT INTO account VALUES ('acc-1', 'refreshed@example.com', 300)`)
	if err := os.WriteFile(filepath.Join(dir2, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	retireIsolatedInstance(workdir, profile, dir2)
	if got := queryCell(t, profileDB, `SELECT email FROM account WHERE id='acc-1'`); got != "refreshed@example.com" {
		t.Fatalf("newer credential did not cross: %q", got)
	}
}

func TestMergeAdoptsStoreWhenProfileNeverRan(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)

	dir := filepath.Join(workdir, instancesDirectory, "run-fresh")
	if err := os.MkdirAll(filepath.Join(dir, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`,
		`INSERT INTO session VALUES ('ses-b', 'second', '/work', 2000)`)
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	if note := retireIsolatedInstance(workdir, profile, dir); !strings.Contains(note, "2 sessions") {
		t.Fatalf("adopt note = %q, want both sessions announced", note)
	}
	if got := countRows(t, filepath.Join(workdir, "data", "opencode", "opencode.db"), "session"); got != 2 {
		t.Fatalf("adopted profile holds %d sessions, want 2", got)
	}
}

func TestMergeCarriesChangedFilesOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	for _, path := range []string{"data/opencode/auth.json", "state/opencode/model.json", "state/opencode/prompt-history.jsonl"} {
		full := filepath.Join(workdir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("seed-"+filepath.Base(path)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	// The instance refreshes auth and appends history; the model pick is
	// untouched, so the profile's newer-or-equal copy must stand.
	if err := os.WriteFile(filepath.Join(dir, "data", "opencode", "auth.json"), []byte("refreshed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	handle, err := os.OpenFile(filepath.Join(dir, "state", "opencode", "prompt-history.jsonl"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString("added line\n"); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	if note := unlock(); !strings.Contains(note, "auth refreshed") {
		t.Fatalf("merge note = %q, want the auth refresh announced", note)
	}
	if got, _ := os.ReadFile(filepath.Join(workdir, "data", "opencode", "auth.json")); string(got) != "refreshed\n" {
		t.Fatalf("profile auth = %q, want the refresh", got)
	}
	if backups, _ := filepath.Glob(filepath.Join(workdir, "data", "opencode", "auth.json.*.bak")); len(backups) != 1 {
		t.Fatalf("auth backups = %v, want the replaced copy kept", backups)
	}
	if got, _ := os.ReadFile(filepath.Join(workdir, "state", "opencode", "prompt-history.jsonl")); !strings.HasSuffix(string(got), "added line\n") {
		t.Fatalf("profile history = %q, want the appended line", got)
	}
	if got, _ := os.ReadFile(filepath.Join(workdir, "state", "opencode", "model.json")); string(got) != "seed-model.json\n" {
		t.Fatalf("profile model = %q, want the seed copy untouched", got)
	}
}

func TestStaleAuthCopyNeverRegressesProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	authPath := filepath.Join(workdir, "data", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte("seed\n"), 0600); err != nil {
		t.Fatal(err)
	}

	dir, _, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	// Another instance refreshes while this one idles on its seed copy.
	if err := os.WriteFile(authPath, []byte("newer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if note := retireIsolatedInstance(workdir, profile, dir); strings.Contains(note, "auth") {
		t.Fatalf("idle instance announced an auth change: %q", note)
	}
	if got, _ := os.ReadFile(authPath); string(got) != "newer\n" {
		t.Fatalf("profile auth = %q, want the newer copy kept", got)
	}
}

func deadPIDLock(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = cmd.Process.Wait()
	return fmt.Sprintf("%d\n", pid)
}

func TestReclaimMergesStaleAndLeavesLive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	writeTestDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`)
	cfg := Config{Profiles: []Profile{profile}}

	stale := filepath.Join(workdir, instancesDirectory, "run-stale")
	if err := os.MkdirAll(filepath.Join(stale, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestDB(t, filepath.Join(stale, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`,
		`INSERT INTO session VALUES ('ses-b', 'second', '/work', 2000)`)
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(stale, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, ".active.lock"), []byte(deadPIDLock(t)), 0600); err != nil {
		t.Fatal(err)
	}

	live := filepath.Join(workdir, instancesDirectory, "run-live")
	if err := os.MkdirAll(filepath.Join(live, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestDB(t, filepath.Join(live, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-live', 'live', '/work', 3000)`)
	if err := os.WriteFile(filepath.Join(live, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, ".active.lock"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}

	notes := reclaimStrayIsolatedInstances(cfg)
	if len(notes) != 1 || !strings.Contains(notes[0], "1 session") {
		t.Fatalf("reclaim notes = %v, want the stale store merged", notes)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale store survived its merge")
	}
	if _, err := os.Stat(filepath.Join(live, "data", "opencode", "opencode.db")); err != nil {
		t.Fatal("live store was touched by the reclaim")
	}
	if got := countRows(t, filepath.Join(workdir, "data", "opencode", "opencode.db"), "session"); got != 2 {
		t.Fatalf("profile holds %d sessions, want ses-a and ses-b", got)
	}
}

func TestActiveInstanceLocksKeepsSeededStaleDirs(t *testing.T) {
	workdir := t.TempDir()
	dir := filepath.Join(workdir, instancesDirectory, "run-stale")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte(deadPIDLock(t)), 0600); err != nil {
		t.Fatal(err)
	}

	locks, err := activeProfileInstanceLocks(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("stale locks = %v, want none listed", locks)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("stale seeded store was reclaimed by the scanner; only the merger may take it")
	}
}

func TestUnionReaderSeesLiveInstancesOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	writeTestDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`)

	live := filepath.Join(workdir, instancesDirectory, "run-live")
	if err := os.MkdirAll(filepath.Join(live, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestDB(t, filepath.Join(live, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses-a', 'first', '/work', 1000)`,
		`INSERT INTO session VALUES ('ses-b', 'second', '/work', 2000)`)
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(live, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, ".active.lock"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}

	got := recentSessions(profile, 0)
	if len(got) != 2 {
		t.Fatalf("recent = %+v, want the profile session and the live one", got)
	}
	if got[0].session.id != "ses-b" || got[1].session.id != "ses-a" {
		t.Fatalf("recent is not newest-first without duplicates: %+v", got)
	}
	if got := recentSessions(profile, 1); len(got) != 1 || got[0].session.id != "ses-b" {
		t.Fatalf("limited recent = %+v, want only the newest", got)
	}
	if !opencodeSessionInProfileDB(profile, "ses-a") {
		t.Fatal("ses-a should read as merged")
	}
	if opencodeSessionInProfileDB(profile, "ses-b") {
		t.Fatal("ses-b lives only in the running instance and must read as unmerged")
	}
}

func TestFailedSeedIsRemovedWithoutMerging(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	cfg := Config{Profiles: []Profile{profile}}

	// A seed that died before writing its receipt: lock stale, no marker.
	failed := filepath.Join(workdir, instancesDirectory, "run-failed")
	if err := os.MkdirAll(filepath.Join(failed, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failed, ".active.lock"), []byte(deadPIDLock(t)), 0600); err != nil {
		t.Fatal(err)
	}

	if notes := reclaimStrayIsolatedInstances(cfg); len(notes) != 0 {
		t.Fatalf("reclaim notes = %v, want nothing merged", notes)
	}
	if _, err := os.Stat(failed); !os.IsNotExist(err) {
		t.Fatal("failed seed survived the reclaim")
	}
}

func TestProfileOfInstanceDirResolvesTwoLevelsUp(t *testing.T) {
	workdir, profile, ok := profileOfInstanceDir(filepath.Join("/root", appName, "profiles", "oc", instancesDirectory, "run-1"))
	if !ok || workdir != filepath.Join("/root", appName, "profiles", "oc") || profile.Name != "oc" || profile.Provider != "opencode" {
		t.Fatalf("resolved = %q %+v %v", workdir, profile, ok)
	}
	if _, _, ok := profileOfInstanceDir(filepath.Join("/root", appName, "profiles", "oc")); ok {
		t.Fatal("a profile directory is not an instance directory")
	}
}
