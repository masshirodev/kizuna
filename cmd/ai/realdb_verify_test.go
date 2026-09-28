package main

import (
	"bytes"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Exercises a fresh instance, a resume import and the merge back against a
// copy of the operator's real opencode.db and the real opencode binary, which
// is the only way to prove the import lands the session under the right
// project and that the generic table walk handles the true schema. Skipped
// wherever either is missing.
func TestRealDBResumeImportAndMerge(t *testing.T) {
	realDB := filepath.Join(os.Getenv("HOME"), ".config", "ai", "profiles", "opencode", "data", "opencode", "opencode.db")
	if _, err := os.Stat(realDB); err != nil {
		t.Skip("no real opencode.db present")
	}
	binary, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("no opencode binary on PATH")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyFileBytes(realDB, archive, 0600); err != nil {
		t.Fatal(err)
	}

	// A small top-level session whose folder still exists: import runs there.
	db, err := sql.Open("sqlite", archive)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT s.id, s.directory, s.project_id, (SELECT COUNT(*) FROM part p WHERE p.session_id = s.id)
		FROM session s WHERE s.parent_id IS NULL ORDER BY 4 ASC`)
	if err != nil {
		t.Fatal(err)
	}
	var id, folder, project string
	var parts int
	for rows.Next() {
		if err := rows.Scan(&id, &folder, &project, &parts); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(folder); err == nil && info.IsDir() && parts > 0 {
			break
		}
		id = ""
	}
	rows.Close()
	db.Close()
	if id == "" {
		t.Skip("no session in the real store whose folder still exists")
	}

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	instanceDB := filepath.Join(dir, "data", "opencode", "opencode.db")
	if fileExists(instanceDB) {
		t.Fatal("instance was seeded with a copy of the archive")
	}

	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, dir, "/", []string{"--session", id}, &stderr)
	if stderr.Len() > 0 {
		t.Fatalf("import reported: %s", stderr.String())
	}
	if got := queryCell(t, instanceDB, `SELECT project_id FROM session WHERE id = '`+id+`'`); got != project {
		t.Fatalf("imported session is under project %q, want the archive's %q", got, project)
	}
	if got := countRows(t, instanceDB, "part"); got != parts {
		t.Fatalf("instance holds %d parts, want the session's %d", got, parts)
	}
	if got := countRows(t, instanceDB, "event"); got != 0 {
		t.Fatalf("import wrote %d event rows into the instance", got)
	}

	// The session continues in the instance: its row moves forward in time.
	idb, err := sql.Open("sqlite", instanceDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := idb.Exec(`UPDATE session SET title = 'continued', time_updated = time_updated + 1000 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	idb.Close()

	note := unlock()
	t.Logf("merge note: %q", note)
	if got := queryCell(t, archive, `SELECT title FROM session WHERE id = '`+id+`'`); got != "continued" {
		t.Fatalf("archive kept title %q after the resumed session moved on", got)
	}
}
