package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// An OpenCode instance starts on an empty store (seedIsolatedInstance), so a
// launch that reopens a conversation has to bring that one conversation in
// first: `opencode --session <id>` only finds sessions in the store it opens.
// The profile's opencode.db is the archive every instance merges into; this
// carries one session the other way.
//
// The carrying is opencode's own `export` and `import`, not a row copy. Their
// JSON is the one format opencode promises to read back across its own schema
// changes, and a row copy would have to follow every migration they ship.

// openCodeToolTimeout bounds each helper run of the opencode binary. An export
// of a long session and a first-open migration both finish in seconds; this
// only stops a wedged process from holding the merge lock forever.
const openCodeToolTimeout = 2 * time.Minute

// openCodeResumeTarget reads which conversation an opencode command line
// reopens: an explicit -s/--session id, or -c/--continue for the newest one
// in the launch folder. The flags mean the same in the TUI and in `run`.
func openCodeResumeTarget(args []string) (id string, latest bool) {
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "-s" || arg == "--session":
			if index+1 < len(args) {
				return args[index+1], false
			}
		case strings.HasPrefix(arg, "--session="):
			return strings.TrimPrefix(arg, "--session="), false
		case arg == "-c" || arg == "--continue":
			latest = true
		}
	}
	return "", latest
}

// prepareOpenCodeInstance imports the conversation a launch reopens into its
// fresh instance store. It never fails the launch: a session that cannot be
// brought in is reported, and opencode then says itself that it found none.
func prepareOpenCodeInstance(profile Profile, workdir, lockDir, folder string, args []string, stderr io.Writer) {
	if !usesIsolatedDataDir(profile) || !isIsolatedInstanceDir(lockDir) {
		return
	}
	id, latest := openCodeResumeTarget(args)
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	if id == "" && latest {
		id = newestOpenCodeSessionIn(archive, folder)
	}
	if id == "" {
		return
	}
	if err := importOpenCodeSession(profile, workdir, lockDir, id); err != nil {
		fmt.Fprintf(stderr, "could not bring session %s into this instance: %s\n", id, err)
	}
}

// importOpenCodeSession exports one session from the archive and imports it
// into an instance store. The export opens the archive through opencode, which
// is a writer (it migrates on open), so it runs under the profile's merge lock
// like every other writer the archive has.
func importOpenCodeSession(profile Profile, workdir, lockDir, id string) error {
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	directory, ok := openCodeSessionDirectory(archive, id)
	if !ok {
		return errors.New("not in the archive")
	}
	exported := filepath.Join(lockDir, "resume-"+sanitizeFileName(id)+".json")
	defer os.Remove(exported)
	_, err := withProfileMergeLockResult(workdir, func() (string, error) {
		output, err := runOpenCodeTool(profile, workdir, workdir, os.TempDir(), "export", id)
		if err != nil {
			return "", err
		}
		return "", os.WriteFile(exported, output, 0600)
	})
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	// Import re-homes a session to the folder it runs in: project_id and
	// directory both come from the import's working directory, not from the
	// JSON. Run anywhere else and the session lands under "global" and /tmp.
	if _, err := os.Stat(directory); err != nil {
		return fmt.Errorf("its folder %s is gone", directory)
	}
	if _, err := runOpenCodeTool(profile, workdir, lockDir, directory, "import", exported); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	return nil
}

// runOpenCodeTool runs one non-interactive opencode subcommand against either
// the archive (lockDir == workdir) or an instance store (lockDir is the
// instance), and returns its stdout.
func runOpenCodeTool(profile Profile, workdir, lockDir, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeToolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, profile.Command, args...)
	cmd.Dir = dir
	cmd.Env = launchEnvironment(profile, workdir, lockDir, os.Environ())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if index := strings.LastIndexByte(detail, '\n'); index >= 0 {
			detail = detail[index+1:]
		}
		if detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	return output, nil
}

// archiveBehindInstance reports whether an instance store was migrated by an
// opencode newer than the one that last migrated the archive: it has applied
// migrations the archive has not. Two stores without migration bookkeeping
// (the test fixtures, or an archive that never existed) compare equal.
func archiveBehindInstance(profileDB, instanceDB string) bool {
	if !fileExists(profileDB) || !fileExists(instanceDB) {
		return false
	}
	db, err := sql.Open("sqlite", profileDB)
	if err != nil {
		return false
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`ATTACH DATABASE ` + quoteString(instanceDB) + ` AS incoming`); err != nil {
		return false
	}
	defer db.Exec(`DETACH DATABASE incoming`) //nolint:errcheck
	if !tableExists(db, "main", "migration") || !tableExists(db, "incoming", "migration") {
		return false
	}
	var missing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM incoming.migration
		WHERE id NOT IN (SELECT id FROM main.migration)`).Scan(&missing); err != nil {
		return false
	}
	return missing > 0
}

// migrateOpenCodeArchive lets opencode migrate the archive by opening it for
// a trivial query. opencode runs its migrations on every open, and it is the
// only thing that knows them.
func migrateOpenCodeArchive(profile Profile, workdir string) error {
	_, err := runOpenCodeTool(profile, workdir, workdir, os.TempDir(), "db", "select 1")
	return err
}

// newestOpenCodeSessionIn names the most recently active top-level session
// recorded for a folder, which is the one `opencode --continue` would have
// reopened had the instance started from a full copy of the archive.
func newestOpenCodeSessionIn(archive, folder string) string {
	if !fileExists(archive) {
		return ""
	}
	db, err := sql.Open("sqlite", archive)
	if err != nil {
		return ""
	}
	defer db.Close()
	var id string
	if err := db.QueryRow(`SELECT id FROM session WHERE directory = ? AND parent_id IS NULL
		ORDER BY time_updated DESC LIMIT 1`, folder).Scan(&id); err != nil {
		return ""
	}
	return id
}

// openCodeSessionDirectory reads the folder a session belongs to.
func openCodeSessionDirectory(archive, id string) (string, bool) {
	if !fileExists(archive) {
		return "", false
	}
	db, err := sql.Open("sqlite", archive)
	if err != nil {
		return "", false
	}
	defer db.Close()
	var directory string
	if err := db.QueryRow(`SELECT directory FROM session WHERE id = ?`, id).Scan(&directory); err != nil {
		return "", false
	}
	return directory, true
}

// sanitizeFileName keeps an id usable as one path element whatever it holds.
func sanitizeFileName(name string) string {
	return strings.Map(func(char rune) rune {
		if char == '/' || char == os.PathSeparator || char == 0 {
			return '_'
		}
		return char
	}, name)
}
