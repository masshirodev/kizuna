//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const credentialSchema = `
CREATE TABLE credential (id text PRIMARY KEY, integration_id text, label text NOT NULL, value text NOT NULL, active integer, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE control_account (email text NOT NULL, url text NOT NULL, access_token text NOT NULL, time_updated integer NOT NULL, CONSTRAINT control_account_pk PRIMARY KEY(email, url));
`

func writeCredentialDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	writeRawDB(t, path, append([]string{credentialSchema}, stmts...)...)
}

// fakeOpenCodeAuth writes a stand-in 2.x opencode that logs each call as
// "<XDG_DATA_HOME>|<args>". Its export prints a marker; its import saves what
// it read on stdin beside the store and puts the prepared instance store in
// place, which is what the real import leaves behind. exportStatus is the
// exit status export ends with.
func fakeOpenCodeAuth(t *testing.T, instanceStore string, exportStatus int) (binary, log string) {
	t.Helper()
	dir := t.TempDir()
	binary = filepath.Join(dir, "opencode")
	log = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$XDG_DATA_HOME|$*\" >> " + log + "\n" +
		"if [ \"$1 $2\" = 'auth export' ]; then echo exported-from-archive; echo 'archive locked' >&2; exit " + strconv.Itoa(exportStatus) + "; fi\n" +
		"if [ \"$1 $2\" = 'auth import' ]; then mkdir -p \"$XDG_DATA_HOME/opencode\"; cat > \"$XDG_DATA_HOME/opencode/imported\"; cp " + instanceStore + " \"$XDG_DATA_HOME/opencode/opencode.db\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return binary, log
}

func TestLaunchCarriesTheProfilesCredentialsIntoTheInstance(t *testing.T) {
	useOpenCodeService(t, true)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	// The store the import leaves: both copies, stamped with the import time.
	// ("cred_skipped" is in the archive but the import did not write it.)
	instanceStore := filepath.Join(t.TempDir(), "instance.db")
	writeCredentialDB(t, instanceStore,
		`INSERT INTO credential VALUES ('cred_a', 'anthropic', 'Anthropic', 'k', 1, 5000, 5000)`,
		`INSERT INTO control_account VALUES ('me@example.com', 'https://x', 't', 5000)`)
	binary, log := fakeOpenCodeAuth(t, instanceStore, 0)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	writeCredentialDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO credential VALUES ('cred_a', 'anthropic', 'Anthropic', 'k', 1, 100, 100)`,
		`INSERT INTO credential VALUES ('cred_skipped', 'other', 'Other', 'k', 1, 200, 200)`,
		`INSERT INTO control_account VALUES ('me@example.com', 'https://x', 't', 300)`)

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, dir, "/work", nil, &stderr)
	if stderr.Len() > 0 {
		t.Fatalf("seeding reported: %s", stderr.String())
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want one export and one import", calls)
	}
	if want := filepath.Join(workdir, "data") + "|auth export --standalone"; !hasCall(calls, want) {
		t.Fatalf("calls = %q, want the export against the archive (%q)", calls, want)
	}
	if want := filepath.Join(dir, "data") + "|auth import --standalone"; !hasCall(calls, want) {
		t.Fatalf("calls = %q, want the import against the instance (%q)", calls, want)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "data", "opencode", "imported")); string(got) != "exported-from-archive\n" {
		t.Fatalf("import read %q, want the export's output piped to it", got)
	}

	seed, err := readIsolatedSeed(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []seededCredentialRow{
		{Table: "control_account", Key: []string{"me@example.com", "https://x"}, Archive: 300, Instance: 5000},
		{Table: "credential", Key: []string{"cred_a"}, Archive: 100, Instance: 5000},
	}
	if got, _ := json.Marshal(seed.Credentials); string(got) != string(mustJSON(t, want)) {
		t.Fatalf("receipt credentials = %s, want %s", got, mustJSON(t, want))
	}
}

// The TUI builds its launches apart from launchProfileCommand, and once left
// prepareOpenCodeInstance out: the fix shipped and every instance opened from
// the TUI still came up without the profile's providers.
func TestTUILaunchCarriesTheProfilesCredentialsIntoTheInstance(t *testing.T) {
	useOpenCodeService(t, true)
	root := isolatePeers(t)
	t.Setenv(ranmaSocketEnv, "")
	t.Setenv(ranmaPaneEnv, "")
	instanceStore := filepath.Join(t.TempDir(), "instance.db")
	writeCredentialDB(t, instanceStore,
		`INSERT INTO credential VALUES ('cred_a', 'anthropic', 'Anthropic', 'k', 1, 5000, 5000)`)
	binary, log := fakeOpenCodeAuth(t, instanceStore, 0)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	writeCredentialDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO credential VALUES ('cred_a', 'anthropic', 'Anthropic', 'k', 1, 100, 100)`)

	tracked, unlock, err := tuiLaunch(profile, nil, nil, false, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if calls := readCalls(t, log); len(calls) != 0 {
		t.Fatalf("calls = %q before the terminal was handed over, want none", calls)
	}
	var stderr bytes.Buffer
	tracked.cmd.Stderr = &stderr
	if _, err := tracked.run(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() > 0 {
		t.Fatalf("seeding reported: %s", stderr.String())
	}

	instanceData := filepath.Join(tracked.workdir, "data")
	calls := readCalls(t, log)
	want := []string{
		filepath.Join(workdir, "data") + "|auth export --standalone",
		instanceData + "|auth import --standalone",
	}
	if len(calls) != 3 || !hasCall(calls[:2], want[0]) || !hasCall(calls[:2], want[1]) ||
		!strings.HasPrefix(calls[2], instanceData+"|") {
		t.Fatalf("calls = %q, want %q and then the session on the instance", calls, want)
	}
	seed, err := readIsolatedSeed(tracked.workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(seed.Credentials) != 1 || seed.Credentials[0].Key[0] != "cred_a" {
		t.Fatalf("receipt credentials = %+v, want cred_a recorded", seed.Credentials)
	}
}

func TestTUILoginPreparesNoInstance(t *testing.T) {
	useOpenCodeService(t, true)
	root := isolatePeers(t)
	t.Setenv(ranmaSocketEnv, "")
	t.Setenv(ranmaPaneEnv, "")
	profile := Profile{Name: "oc", Provider: "opencode", Command: "true"}
	profileWorkdir(t, root, profile.Name)
	tracked, unlock, err := tuiLaunch(profile, loginArgs("opencode"), nil, true, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if tracked.prepare != nil {
		t.Fatal("a login under the exclusive lock prepares an instance it does not have")
	}
}

func TestLaunchWithoutStoredCredentialsRunsNoExport(t *testing.T) {
	useOpenCodeService(t, true)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	binary, log := fakeOpenCodeAuth(t, "/nonexistent", 0)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	writeCredentialDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"))

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	prepareOpenCodeInstance(profile, workdir, dir, "/work", nil, &bytes.Buffer{})
	if calls := readCalls(t, log); len(calls) != 0 {
		t.Fatalf("a profile with nothing stored ran %q", calls)
	}
}

func TestAFailedCredentialExportIsReportedAndTheLaunchGoesOn(t *testing.T) {
	useOpenCodeService(t, true)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	instanceStore := filepath.Join(t.TempDir(), "instance.db")
	writeCredentialDB(t, instanceStore)
	binary, _ := fakeOpenCodeAuth(t, instanceStore, 3)
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	writeCredentialDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO credential VALUES ('cred_a', 'anthropic', 'Anthropic', 'k', 1, 100, 100)`)

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, dir, "/work", nil, &stderr)
	if got := stderr.String(); !strings.Contains(got, "could not bring oc's credentials") || !strings.Contains(got, "archive locked") {
		t.Fatalf("stderr = %q, want the export's failure and its last word", got)
	}
}

func TestPipeReportsABinaryThatWillNotStart(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: filepath.Join(root, "missing-opencode")}
	workdir := profileWorkdir(t, root, profile.Name)
	if err := pipeOpenCodeCredentials(profile, workdir, t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "export:") {
		t.Fatalf("err = %v, want the export's start failure", err)
	}
}

// The merge settles the rows an instance received at launch before the
// newest-wins pass, because import stamped them all with the launch time.
func TestMergeSettlesCopiedCredentialsAgainstTheReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeCredentialDB(t, archive,
		// Another instance refreshed it after this one launched.
		`INSERT INTO credential VALUES ('cred_untouched', 'a', 'A', 'refreshed-elsewhere', 1, 100, 300)`,
		`INSERT INTO credential VALUES ('cred_refreshed', 'b', 'B', 'old', 1, 100, 100)`,
		`INSERT INTO credential VALUES ('cred_logged_out', 'c', 'C', 'v', 1, 100, 100)`,
		// Logged out here, but re-added elsewhere since: the newer intent stands.
		`INSERT INTO credential VALUES ('cred_readded', 'd', 'D', 'readded', 1, 100, 400)`,
		`INSERT INTO control_account VALUES ('me@example.com', 'https://x', 'old', 100)`)

	dir := filepath.Join(workdir, instancesDirectory, "run-settle")
	writeCredentialDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
		`INSERT INTO credential VALUES ('cred_untouched', 'a', 'A', 'stale-copy', 1, 5000, 5000)`,
		`INSERT INTO credential VALUES ('cred_refreshed', 'b', 'B', 'new', 1, 5000, 6000)`,
		`INSERT INTO credential VALUES ('cred_new', 'e', 'E', 'connected-here', 1, 5500, 5500)`)
	info, _ := json.Marshal(isolatedSeedInfo{Credentials: []seededCredentialRow{
		{Table: "credential", Key: []string{"cred_untouched"}, Archive: 100, Instance: 5000},
		{Table: "credential", Key: []string{"cred_refreshed"}, Archive: 100, Instance: 5000},
		{Table: "credential", Key: []string{"cred_logged_out"}, Archive: 100, Instance: 5000},
		{Table: "credential", Key: []string{"cred_readded"}, Archive: 100, Instance: 5000},
		{Table: "control_account", Key: []string{"me@example.com", "https://x"}, Archive: 100, Instance: 5000},
		{Table: "credential", Key: []string{"wrong", "arity"}, Archive: 1, Instance: 1},
	}})
	if err := os.WriteFile(filepath.Join(dir, isolatedSeedFile), info, 0600); err != nil {
		t.Fatal(err)
	}
	if note := retireIsolatedInstance(workdir, profile, dir); strings.Contains(note, "skipped") || strings.Contains(note, "failed") {
		t.Fatalf("merge note: %q", note)
	}

	for id, want := range map[string]string{
		"cred_untouched": "refreshed-elsewhere",
		"cred_refreshed": "new",
		"cred_readded":   "readded",
		"cred_new":       "connected-here",
	} {
		if got := queryCell(t, archive, `SELECT value FROM credential WHERE id='`+id+`'`); got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
	if got := queryCell(t, archive, `SELECT COUNT(*) FROM credential WHERE id='cred_logged_out'`); got != "0" {
		t.Error("a logout in the instance left the credential in the archive")
	}
	// The control account was logged out too, by its composite key.
	if got := countRows(t, archive, "control_account"); got != 0 {
		t.Errorf("control_account holds %d rows, want the logged-out one gone", got)
	}
}

// A receipt from before c194 lists no credentials: its rows merge newest-wins,
// as they always did.
func TestMergeWithoutCopiedCredentialsKeepsNewestWins(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	writeCredentialDB(t, archive, `INSERT INTO credential VALUES ('cred_a', 'a', 'A', 'old', 1, 100, 100)`)
	dir := filepath.Join(workdir, instancesDirectory, "run-legacy")
	writeCredentialDB(t, filepath.Join(dir, "data", "opencode", "opencode.db"),
		`INSERT INTO credential VALUES ('cred_a', 'a', 'A', 'new', 1, 100, 200)`)
	markSeeded(t, dir)
	retireIsolatedInstance(workdir, profile, dir)
	if got := queryCell(t, archive, `SELECT value FROM credential WHERE id='cred_a'`); got != "new" {
		t.Fatalf("value = %q, want the newer row", got)
	}
}

func TestCredentialStampsSkipTablesItCannotOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	writeRawDB(t, path,
		`CREATE TABLE credential (id text, value text)`,
		`INSERT INTO credential VALUES ('x', 'y')`)
	rows, err := readCredentialStamps(path)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows = %v, %v; want nothing from a table without a key and time_updated", rows, err)
	}
	if !storeHoldsCredentials(path) {
		t.Fatal("a store with a credential row reads as empty")
	}
	if storeHoldsCredentials(filepath.Join(t.TempDir(), "absent.db")) {
		t.Fatal("a missing store reads as holding credentials")
	}
	if rows, err := readCredentialStamps(filepath.Join(t.TempDir(), "absent.db")); err != nil || len(rows) != 0 {
		t.Fatalf("missing store: %v, %v", rows, err)
	}
}

// Against the real binary, on stores it creates itself (no operator data):
// a credential stored in the profile reaches a new instance, an instance that
// leaves it alone does not restamp the archive, and a logout in the instance
// reaches the archive. Skipped without an opencode 2.x on PATH.
func TestRealOpenCodeCarriesCredentialsAndLogouts(t *testing.T) {
	binary, err := exec.LookPath("opencode")
	if err != nil || !probeOpenCodeService(binary) {
		t.Skip("no opencode 2.x on PATH")
	}
	previous := openCodeServiceProbe
	openCodeServiceProbe = probeOpenCodeService
	resetOpenCodeServiceCache()
	t.Cleanup(func() {
		openCodeServiceProbe = previous
		resetOpenCodeServiceCache()
	})
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)

	// Two made-up keys stored in the profile the way opencode stores them.
	exported := filepath.Join(root, "seed.json")
	if err := os.WriteFile(exported, []byte(`[
 {"id":"cred_kizunatest0001","integrationID":"anthropic","label":"Anthropic","active":true,"value":{"type":"key","key":"not-a-real-key-1"}},
 {"id":"cred_kizunatest0002","integrationID":"deepseek","label":"DeepSeek","active":true,"value":{"type":"key","key":"not-a-real-key-2"}}
]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runOpenCodeTool(profile, workdir, workdir, os.TempDir(), "auth", "import", openCodeStandaloneFlag, exported); err != nil {
		t.Fatal("storing the test credentials:", err)
	}
	archive := filepath.Join(workdir, "data", "opencode", "opencode.db")
	stamp := func(id string) string {
		return queryCell(t, archive, `SELECT time_updated FROM credential WHERE id='`+id+`'`)
	}
	kept := stamp("cred_kizunatest0002")

	dir, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, dir, root, nil, &stderr)
	if stderr.Len() > 0 {
		t.Fatalf("seeding reported: %s", stderr.String())
	}
	instanceDB := filepath.Join(dir, "data", "opencode", "opencode.db")
	if got := countRows(t, instanceDB, "credential"); got != 2 {
		t.Fatalf("instance holds %d credentials, want the profile's 2", got)
	}
	if _, err := runOpenCodeTool(profile, workdir, dir, os.TempDir(), "auth", "logout", openCodeStandaloneFlag, "anthropic", "cred_kizunatest0001"); err != nil {
		t.Fatal("logout in the instance:", err)
	}
	if note := unlock(); strings.Contains(note, "failed") {
		t.Fatalf("merge note: %q", note)
	}

	if got := queryCell(t, archive, `SELECT COUNT(*) FROM credential WHERE id='cred_kizunatest0001'`); got != "0" {
		t.Fatal("the instance's logout did not reach the archive")
	}
	if got := stamp("cred_kizunatest0002"); got != kept {
		t.Fatalf("an untouched copy restamped the archive's row: %s, want %s", got, kept)
	}
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
