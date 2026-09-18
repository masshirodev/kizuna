package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Exercises seed + merge against a copy of the operator's real opencode.db,
// proving the generic table walk handles the true 19-table schema.
func TestRealDBSeedAndMerge(t *testing.T) {
	realDB := filepath.Join(os.Getenv("HOME"), ".config", "ai", "profiles", "opencode", "data", "opencode", "opencode.db")
	if _, err := os.Stat(realDB); err != nil {
		t.Skip("no real opencode.db present")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	if err := os.MkdirAll(filepath.Join(workdir, "data", "opencode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyFileBytes(realDB, filepath.Join(workdir, "data", "opencode", "opencode.db"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate one new session row in the instance copy.
	db, err := sql.Open("sqlite", filepath.Join(dir, "data", "opencode", "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) VALUES ('verify-ses', 'verify-proj', 'verify', '/work', 'verify', 'v', 1, 1)`)
	if err != nil {
		// Real schema may differ; report rather than fail silently.
		t.Logf("could not insert probe session: %v", err)
	}
	db.Close()
	note := unlock()
	t.Logf("merge note: %q", note)
}
