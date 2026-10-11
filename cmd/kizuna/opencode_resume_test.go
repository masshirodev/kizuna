//go:build unix

package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionSubtreeSchema carries the columns the newer-row merge and the resume
// import read: time_updated on the session subtree and on project, and
// parent_id for --continue.
const sessionSubtreeSchema = `
CREATE TABLE session (id text PRIMARY KEY, project_id text, parent_id text, title text NOT NULL, directory text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);
CREATE TABLE project (id text PRIMARY KEY, worktree text NOT NULL, icon_url_override text, time_updated integer NOT NULL);
CREATE TABLE migration (id text PRIMARY KEY);
`

func writeSubtreeDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	writeRawDB(t, path, append([]string{sessionSubtreeSchema}, stmts...)...)
}

func writeRawDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

func markSeeded(t *testing.T, dir string) {
	t.Helper()
	info, _ := jsonMarshal(isolatedSeedInfo{})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), append(info, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// fakeOpenCode writes a stand-in opencode that logs each call as
// "<cwd>|<XDG_DATA_HOME>|<args>" and answers export with a JSON body.
func fakeOpenCode(t *testing.T) (binary, log string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "opencode")
	log = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$PWD|$XDG_DATA_HOME|$*\" >> " + log + "\n" +
		"[ \"$1\" = export ] && echo '{\"info\":{\"id\":\"'\"$2\"'\"},\"messages\":[]}'\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return binary, log
}

func readCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestOpenCodeResumeTargetReadsSessionAndContinueFlags(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		id     string
		latest bool
	}{
		{[]string{"--session", "ses_a"}, "ses_a", false},
		{[]string{"-s", "ses_b", "--fork"}, "ses_b", false},
		{[]string{"run", "--session=ses_c", "go on"}, "ses_c", false},
		{[]string{"--continue"}, "", true},
		{[]string{"-c"}, "", true},
		{[]string{"run", "do the thing"}, "", false},
		{[]string{"--session"}, "", false},
	} {
		id, latest := openCodeResumeTarget(tc.args)
		if id != tc.id || latest != tc.latest {
			t.Fatalf("%q: got (%q, %v), want (%q, %v)", tc.args, id, latest, tc.id, tc.latest)
		}
	}
}

func TestResumeImportsTheSessionThroughOpencodeInItsFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	binary, log := fakeOpenCode(t)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	folder := t.TempDir()
	writeSubtreeDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses_a', 'p1', NULL, 'first', '`+folder+`', 1, 1)`)

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, dir, "/elsewhere", []string{"--session", "ses_a"}, &stderr)
	if stderr.Len() > 0 {
		t.Fatalf("import reported: %s", stderr.String())
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want one export and one import", calls)
	}
	// Export reads the archive; import writes the instance store, run from the
	// session's own folder (import re-homes a session to its working directory).
	if want := "|" + filepath.Join(workdir, "data") + "|export ses_a"; !strings.HasSuffix(calls[0], want) {
		t.Fatalf("export call = %q, want it against the archive (%q)", calls[0], want)
	}
	importPrefix := folder + "|" + filepath.Join(dir, "data") + "|import "
	if !strings.HasPrefix(calls[1], importPrefix) {
		t.Fatalf("import call = %q, want it in %s against the instance store", calls[1], folder)
	}
	if exported := strings.TrimPrefix(calls[1], importPrefix); fileExists(exported) {
		t.Fatal("the exported JSON outlived the import")
	}
}

func TestContinueImportsTheNewestTopLevelSessionOfTheFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	binary, log := fakeOpenCode(t)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	folder := t.TempDir()
	writeSubtreeDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses_old', 'p1', NULL, 'old', '`+folder+`', 1, 10)`,
		`INSERT INTO session VALUES ('ses_new', 'p1', NULL, 'new', '`+folder+`', 2, 20)`,
		`INSERT INTO session VALUES ('ses_child', 'p1', 'ses_new', 'subagent', '`+folder+`', 3, 30)`,
		`INSERT INTO session VALUES ('ses_other', 'p1', NULL, 'other folder', '/nowhere', 4, 40)`)

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	prepareOpenCodeInstance(profile, workdir, dir, folder, []string{"--continue"}, &bytes.Buffer{})
	if calls := readCalls(t, log); len(calls) == 0 || !strings.HasSuffix(calls[0], "export ses_new") {
		t.Fatalf("calls = %q, want the folder's newest top-level session exported", calls)
	}
}

func TestPlainLaunchImportsNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	binary, log := fakeOpenCode(t)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	prepareOpenCodeInstance(profile, workdir, dir, "/work", []string{"run", "headless work"}, &bytes.Buffer{})
	if calls := readCalls(t, log); len(calls) != 0 {
		t.Fatalf("a launch reopening nothing ran %q", calls)
	}
}

func TestMergeCarriesNewerSessionRowsButNeverAProjectRow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeSubtreeDB(t, archive,
		`INSERT INTO session VALUES ('ses_a', 'p1', NULL, 'before', '/work', 1, 100)`,
		`INSERT INTO message VALUES ('msg_1', 'ses_a', 1, 100, 'draft')`,
		`INSERT INTO project VALUES ('p1', '/work', 'my-icon', 50)`)

	// The resumed session moved on in the instance; a fresh store also wrote
	// its own project row, newer and with default fields.
	dir := filepath.Join(workdir, instancesDirectory, "run-resumed")
	writeSubtreeDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses_a', 'p1', NULL, 'after', '/work', 1, 200)`,
		`INSERT INTO message VALUES ('msg_1', 'ses_a', 1, 200, 'final')`,
		`INSERT INTO message VALUES ('msg_2', 'ses_a', 2, 200, 'new')`,
		`INSERT INTO project VALUES ('p1', '/work', NULL, 999)`)
	markSeeded(t, dir)
	retireIsolatedInstance(workdir, profile, dir)

	if got := queryCell(t, archive, `SELECT title FROM session WHERE id='ses_a'`); got != "after" {
		t.Fatalf("archive kept session title %q, want the resumed session's newer row", got)
	}
	if got := queryCell(t, archive, `SELECT data FROM message WHERE id='msg_1'`); got != "final" {
		t.Fatalf("archive kept message %q, want the rewritten one", got)
	}
	if got := countRows(t, archive, "message"); got != 2 {
		t.Fatalf("archive holds %d messages, want the new one added", got)
	}
	if got := queryCell(t, archive, `SELECT icon_url_override FROM project WHERE id='p1'`); got != "my-icon" {
		t.Fatalf("a fresh store's project row overwrote the archive's: icon %q", got)
	}

	// An older copy never rolls the archive back.
	stale := filepath.Join(workdir, instancesDirectory, "run-stale")
	writeSubtreeDB(t, filepath.Join(stale, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses_a', 'p1', NULL, 'stale', '/work', 1, 150)`)
	markSeeded(t, stale)
	retireIsolatedInstance(workdir, profile, stale)
	if got := queryCell(t, archive, `SELECT title FROM session WHERE id='ses_a'`); got != "after" {
		t.Fatalf("an older copy rolled the archive back to %q", got)
	}
}

func TestMergeMigratesAnArchiveBehindTheInstanceFirst(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	binary, log := fakeOpenCode(t)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	writeSubtreeDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO migration VALUES ('0001')`)

	same := filepath.Join(workdir, instancesDirectory, "run-same")
	writeSubtreeDB(t, filepath.Join(same, "data", "opencode", "opencode.db"),
		`INSERT INTO migration VALUES ('0001')`)
	markSeeded(t, same)
	retireIsolatedInstance(workdir, profile, same)
	if calls := readCalls(t, log); len(calls) != 0 {
		t.Fatalf("an archive on the instance's schema was migrated: %q", calls)
	}

	newer := filepath.Join(workdir, instancesDirectory, "run-newer")
	writeSubtreeDB(t, filepath.Join(newer, "data", "opencode", "opencode.db"),
		`INSERT INTO migration VALUES ('0001')`, `INSERT INTO migration VALUES ('0002')`)
	markSeeded(t, newer)
	retireIsolatedInstance(workdir, profile, newer)
	calls := readCalls(t, log)
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "|"+filepath.Join(workdir, "data")+"|db select 1") {
		t.Fatalf("calls = %q, want one migrating open of the archive", calls)
	}
}
