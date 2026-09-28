package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// compactCommand drops opencode's sync event log from a profile's session
// archive and vacuums it (`ai compact <profile>`).
//
// The event table is a per-session sync log opencode reads only to move a
// session to a remote workspace. Each message.updated event embeds the
// session's cumulative diff, so it grows roughly with the square of how much a
// session edits: on one profile here it was 4.9 GB of a 5.0 GB store, written
// by headless workers that ran opencode directly on the profile store
// (doc/session-store.md). The conversations themselves are session, message
// and part, and are untouched.
func compactCommand(cfg Config, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: ai compact <profile>")
	}
	profile, err := resolveProfile(cfg, args[0])
	if err != nil {
		return err
	}
	if profile.Provider != "opencode" {
		return fmt.Errorf("%s is a %s profile; only an opencode profile keeps a session store to compact", profile.Name, profile.Provider)
	}
	root, err := profileRoot()
	if err != nil {
		return err
	}
	workdir := filepath.Join(root, profile.Name)
	store := filepath.Join(workdir, "data", "opencode", "opencode.db")
	if !fileExists(store) {
		return fmt.Errorf("%s has no session store yet", profile.Name)
	}
	// The exclusive lock refuses while any instance runs and keeps new ones
	// from starting; the merge lock keeps a stray-instance merge out.
	unlock, err := acquireProfileLock(workdir)
	if err != nil {
		return err
	}
	defer unlock()
	// A process started with the profile's env but not through ai (the way
	// wave workers once ran) holds no lock at all. Vacuuming under it would
	// be the shared-writer corruption the instances exist to prevent.
	if users := directStoreUsers(filepath.Join(workdir, "data")); len(users) > 0 {
		return fmt.Errorf("%s's store is open in process(es) %s, started outside ai; stop them first",
			profile.Name, strings.Join(users, ", "))
	}
	report, err := withProfileMergeLockResult(workdir, func() (string, error) {
		return compactOpenCodeStore(store, time.Now())
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %s\n", profile.Name, report)
	return nil
}

// compactOpenCodeStore backs the store up beside itself, empties the event log
// and vacuums. The backup is taken first and through SQLite, so it is
// consistent, and nothing is deleted unless it succeeded.
func compactOpenCodeStore(store string, now time.Time) (string, error) {
	before := storeSize(store)
	backup := store + "." + now.UTC().Format("20060102T150405Z") + ".bak"
	db, err := sql.Open("sqlite", store)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 15000`); err != nil {
		return "", err
	}
	if _, err := db.Exec(`VACUUM INTO ` + quoteString(backup)); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	// Both tables, explicitly: the cascade from event_sequence needs
	// foreign_keys on, which this connection does not assume.
	for _, table := range []string{"event", "event_sequence"} {
		if !tableExists(db, "main", table) {
			continue
		}
		if _, err := db.Exec(`DELETE FROM ` + quoteIdentifier(table)); err != nil {
			return "", fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		return "", fmt.Errorf("vacuum: %w", err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s → %s; backup %s (%s): delete it once the profile works",
		formatStoreSize(before), formatStoreSize(storeSize(store)), backup, formatStoreSize(storeSize(backup))), nil
}

// directStoreUsers lists the processes whose XDG_DATA_HOME is the profile's
// data directory. Linux only: elsewhere there is no /proc to read, and the
// lock check above is all the guard there is.
func directStoreUsers(dataHome string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	want := "XDG_DATA_HOME=" + filepath.Clean(dataHome)
	self := strconv.Itoa(os.Getpid())
	var users []string
	for _, entry := range entries {
		pid := entry.Name()
		if _, err := strconv.Atoi(pid); err != nil || pid == self {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", pid, "environ"))
		if err != nil {
			continue // another user's process, or one that just exited
		}
		for _, variable := range strings.Split(string(environ), "\x00") {
			if strings.TrimRight(variable, "/") == want {
				users = append(users, pid)
				break
			}
		}
	}
	return users
}

// storeSize is a SQLite store's size on disk, its write-ahead log included.
func storeSize(path string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(path + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

func formatStoreSize(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}
