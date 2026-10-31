package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// OpenCode 2.x keeps what `/connect` and `auth login` store in opencode.db
// (credential, account, control_account), no longer in auth.json. An instance
// starts on an empty store (seedIsolatedInstance), so a provider connected in
// one instance merged back into the archive and never reached the next one:
// every launch came up without it (c194).
//
// The carrying is opencode's own `auth export` piped into `auth import`, for
// the reason the session import uses export and import (opencode_resume.go):
// it is the format opencode reads back across its own schema changes. The
// pipe runs from one opencode process to the other, so the secrets never pass
// through kizuna.
//
// Import stamps every row it writes with the time of the import, so a copy
// the instance never touched would look newer than the archive's row on the
// way back, and the merge's newest-wins rule would let it overwrite a token
// another instance refreshed meanwhile. The receipt therefore records each
// copied row's key and both stamps (never a value), and the merge settles
// copied rows against it first (settleSeededCredentials).

// seededCredentialRow is one credential row an instance received at launch:
// its primary key, the archive's time_updated when it was exported, and the
// instance's time_updated right after the import.
type seededCredentialRow struct {
	Table    string   `json:"table"`
	Key      []string `json:"key"`
	Archive  int64    `json:"archive_updated"`
	Instance int64    `json:"instance_updated"`
}

// credentialStamp is a row's key and time_updated, read without its values.
type credentialStamp struct {
	key     []string
	updated int64
}

// seedOpenCodeCredentials brings the profile's stored credentials into a
// fresh instance store and records what it brought. 1.x keeps its
// credentials in auth.json, which seedIsolatedInstance already copies.
func seedOpenCodeCredentials(profile Profile, workdir, lockDir string) error {
	if !openCodeUsesService(profile.Command) {
		return nil
	}
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	if !storeHoldsCredentials(archive) {
		return nil
	}
	var archiveRows map[string][]credentialStamp
	// The export opens the archive through opencode, which migrates on open,
	// so it is a writer and takes the merge lock like every other one. The
	// stamps are read under the same lock: no merge can land between them
	// and the export.
	_, err := withProfileMergeLockResult(workdir, func() (string, error) {
		rows, err := readCredentialStamps(archive)
		if err != nil {
			return "", err
		}
		archiveRows = rows
		return "", pipeOpenCodeCredentials(profile, workdir, lockDir)
	})
	// Recorded even after a failed import: whatever did land must still be
	// recognised as a copy at merge time, or its fresh stamp would win there.
	instanceRows, readErr := readCredentialStamps(filepath.Join(lockDir, "data", "opencode", "opencode.db"))
	if readErr == nil {
		if recordErr := recordSeededCredentials(lockDir, matchSeededCredentials(archiveRows, instanceRows)); recordErr != nil && err == nil {
			err = recordErr
		}
	} else if err == nil {
		err = readErr
	}
	return err
}

// pipeOpenCodeCredentials runs `auth export` against the archive with its
// output wired straight into `auth import` on the instance store.
func pipeOpenCodeCredentials(profile Profile, workdir, lockDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeToolTimeout)
	defer cancel()
	exporter := exec.CommandContext(ctx, profile.Command, "auth", "export", openCodeStandaloneFlag)
	exporter.Dir = os.TempDir()
	exporter.Env = launchEnvironment(profile, workdir, workdir, os.Environ())
	importer := exec.CommandContext(ctx, profile.Command, "auth", "import", openCodeStandaloneFlag)
	importer.Dir = os.TempDir()
	importer.Env = launchEnvironment(profile, workdir, lockDir, os.Environ())
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	var exportStderr, importStderr strings.Builder
	exporter.Stdout, exporter.Stderr = writer, &exportStderr
	importer.Stdin, importer.Stderr = reader, &importStderr
	if err := exporter.Start(); err != nil {
		writer.Close()
		reader.Close()
		return fmt.Errorf("export: %w", err)
	}
	if err := importer.Start(); err != nil {
		writer.Close()
		reader.Close()
		_ = exporter.Wait()
		return fmt.Errorf("import: %w", err)
	}
	// Only the children hold the pipe now, so the importer sees the end of
	// input when the exporter exits.
	writer.Close()
	reader.Close()
	exportErr := exporter.Wait()
	importErr := importer.Wait()
	if exportErr != nil {
		return fmt.Errorf("export: %w", openCodeToolError(exportErr, exportStderr.String()))
	}
	if importErr != nil {
		return fmt.Errorf("import: %w", openCodeToolError(importErr, importStderr.String()))
	}
	return nil
}

// storeHoldsCredentials reports whether a store has any row in a credential
// table, so a profile that never connected anything pays for no export.
func storeHoldsCredentials(path string) bool {
	if !fileExists(path) {
		return false
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return false
	}
	defer db.Close()
	for _, table := range sortedAuthTables() {
		if !tableExists(db, "main", table) {
			continue
		}
		var one int
		if db.QueryRow(`SELECT 1 FROM `+quoteIdentifier(table)+` LIMIT 1`).Scan(&one) == nil {
			return true
		}
	}
	return false
}

// readCredentialStamps reads every credential row's primary key and
// time_updated, per table, and nothing else. A table the store lacks, or one
// without a key and a time_updated, is left out.
func readCredentialStamps(path string) (map[string][]credentialStamp, error) {
	result := map[string][]credentialStamp{}
	if !fileExists(path) {
		return result, nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	for _, table := range sortedAuthTables() {
		if !tableExists(db, "main", table) {
			continue
		}
		columns, err := tableColumns(db, "main", table)
		if err != nil {
			return nil, err
		}
		if !canOrderByUpdated(columns) {
			continue
		}
		pk := primaryKeyColumns(columns)
		selected := make([]string, 0, len(pk)+1)
		for _, name := range pk {
			selected = append(selected, `CAST(`+quoteIdentifier(name)+` AS TEXT)`)
		}
		selected = append(selected, `"time_updated"`)
		rows, err := db.Query(`SELECT ` + strings.Join(selected, ", ") + ` FROM ` + quoteIdentifier(table))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			key := make([]sql.NullString, len(pk))
			var updated int64
			targets := make([]any, 0, len(pk)+1)
			for index := range key {
				targets = append(targets, &key[index])
			}
			targets = append(targets, &updated)
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				return nil, err
			}
			stamp := credentialStamp{updated: updated}
			for _, part := range key {
				stamp.key = append(stamp.key, part.String)
			}
			result[table] = append(result[table], stamp)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// matchSeededCredentials pairs the archive's rows with the copies the import
// wrote. Only a row on both sides is a copy: one the import skipped must not
// read as "deleted by the instance" when the merge comes.
func matchSeededCredentials(archive, instance map[string][]credentialStamp) []seededCredentialRow {
	var seeded []seededCredentialRow
	for _, table := range sortedAuthTables() {
		copies := map[string]int64{}
		for _, stamp := range instance[table] {
			copies[credentialKey(stamp.key)] = stamp.updated
		}
		for _, stamp := range archive[table] {
			if updated, ok := copies[credentialKey(stamp.key)]; ok {
				seeded = append(seeded, seededCredentialRow{Table: table, Key: stamp.key, Archive: stamp.updated, Instance: updated})
			}
		}
	}
	return seeded
}

// recordSeededCredentials adds the copied rows to the seed receipt. The
// receipt is replaced by a rename so a crash leaves the old one whole.
func recordSeededCredentials(lockDir string, seeded []seededCredentialRow) error {
	if len(seeded) == 0 {
		return nil
	}
	info, err := readIsolatedSeed(lockDir)
	if err != nil {
		return err
	}
	info.Credentials = seeded
	encoded, err := json.Marshal(info)
	if err != nil {
		return err
	}
	path := filepath.Join(lockDir, isolatedSeedFile)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// settleSeededCredentials runs inside the merge, before the newest-wins pass
// over a credential table. For each row this instance received at launch:
//
//   - still there with its import stamp: untouched, so it is dropped from the
//     incoming side and the archive's row stands, whatever its age;
//   - gone: the instance logged out of it, so the archive's row goes too,
//     unless the archive's copy changed since the export (another instance
//     refreshed or re-added it, and that is the newer intent);
//   - restamped: a real change, left for the newest-wins pass.
func settleSeededCredentials(tx *sql.Tx, table string, columns tableColumnsResult, seeded []seededCredentialRow) error {
	pk := primaryKeyColumns(columns)
	if len(pk) == 0 {
		return nil
	}
	conditions := make([]string, len(pk))
	for index, name := range pk {
		conditions[index] = `CAST(` + quoteIdentifier(name) + ` AS TEXT) = ?`
	}
	where := strings.Join(conditions, " AND ")
	quoted := quoteIdentifier(table)
	for _, row := range seeded {
		if row.Table != table || len(row.Key) != len(pk) {
			continue
		}
		args := make([]any, len(row.Key))
		for index, part := range row.Key {
			args[index] = part
		}
		var updated int64
		err := tx.QueryRow(`SELECT "time_updated" FROM incoming.`+quoted+` WHERE `+where, args...).Scan(&updated)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`DELETE FROM main.`+quoted+` WHERE `+where+` AND "time_updated" = ?`, append(args, row.Archive)...); err != nil {
				return err
			}
		case err != nil:
			return err
		case updated == row.Instance:
			if _, err := tx.Exec(`DELETE FROM incoming.`+quoted+` WHERE `+where, args...); err != nil {
				return err
			}
		}
	}
	return nil
}

func primaryKeyColumns(columns tableColumnsResult) []string {
	var pk []string
	for index, name := range columns.names {
		if columns.pk[index] {
			pk = append(pk, name)
		}
	}
	return pk
}

func credentialKey(key []string) string {
	return strings.Join(key, "\x00")
}

func sortedAuthTables() []string {
	tables := make([]string, 0, len(opencodeAuthTables))
	for table := range opencodeAuthTables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}
