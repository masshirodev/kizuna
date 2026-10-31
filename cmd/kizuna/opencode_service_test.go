package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The stand-in opencode binaries throughout the tests log every call, so a
// version probe would show up in their call logs. They play 1.x unless a test
// says otherwise with useOpenCodeService.
func init() {
	openCodeServiceProbe = func(string) bool { return false }
}

// useOpenCodeService makes every binary answer as OpenCode 2.x (or not) for
// one test, and clears the per-binary cache either side of it.
func useOpenCodeService(t *testing.T, answer bool) *int {
	t.Helper()
	probes := new(int)
	previous := openCodeServiceProbe
	resetOpenCodeServiceCache()
	openCodeServiceProbe = func(string) bool {
		*probes++
		return answer
	}
	t.Cleanup(func() {
		openCodeServiceProbe = previous
		resetOpenCodeServiceCache()
	})
	return probes
}

func resetOpenCodeServiceCache() {
	openCodeServiceCache.Lock()
	openCodeServiceCache.usesService = map[string]bool{}
	openCodeServiceCache.Unlock()
}

func writeVersionScript(t *testing.T, body string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return binary
}

func TestProbeOpenCodeServiceReadsTheMajorVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"2.x", `echo "opencode v2.0.32"`, true},
		{"a later major", `echo "opencode v3.1.0"`, true},
		{"1.x", `echo "1.18.29"`, false},
		{"no version", `echo "opencode"`, false},
		{"a failing binary", `echo "v2.0.0"; exit 1`, false},
	} {
		if got := probeOpenCodeService(writeVersionScript(t, tc.body)); got != tc.want {
			t.Errorf("%s: probe = %v, want %v", tc.name, got, tc.want)
		}
	}
	if probeOpenCodeService(filepath.Join(t.TempDir(), "missing")) {
		t.Error("a missing binary answered as 2.x")
	}
}

func TestOpenCodeStandaloneGoesWhereTheServiceWouldBeReached(t *testing.T) {
	useOpenCodeService(t, true)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		// The bare TUI takes the flag first, whatever follows it.
		{nil, []string{"--standalone"}},
		{[]string{"--auto"}, []string{"--standalone", "--auto"}},
		{[]string{"--session", "ses_a"}, []string{"--standalone", "--session", "ses_a"}},
		{[]string{"-c"}, []string{"--standalone", "-c"}},
		{[]string{"/some/folder"}, []string{"--standalone", "/some/folder"}},
		// Subcommands take it after the words that own it.
		{[]string{"run", "--auto", "hello"}, []string{"run", "--standalone", "--auto", "hello"}},
		{[]string{"mini"}, []string{"mini", "--standalone"}},
		{[]string{"models"}, []string{"models", "--standalone"}},
		{[]string{"session", "export", "ses_a"}, []string{"session", "export", "--standalone", "ses_a"}},
		{[]string{"session", "list"}, []string{"session", "list", "--standalone"}},
		{[]string{"auth", "login"}, []string{"auth", "login", "--standalone"}},
		// 2.2.5 sends every auth verb through the service, not only login.
		{[]string{"auth", "list"}, []string{"auth", "list", "--standalone"}},
		{[]string{"auth", "logout", "anthropic"}, []string{"auth", "logout", "--standalone", "anthropic"}},
		{[]string{"auth", "export"}, []string{"auth", "export", "--standalone"}},
		// Nothing that never reaches the service.
		{[]string{"auth"}, []string{"auth"}},
		{[]string{"session"}, []string{"session"}},
		{[]string{"service", "status"}, []string{"service", "status"}},
		{[]string{"upgrade"}, []string{"upgrade"}},
		{[]string{"mcp", "list"}, []string{"mcp", "list"}},
		{[]string{"reload"}, []string{"reload"}},
		// A server already chosen stays chosen.
		{[]string{"--standalone", "-c"}, []string{"--standalone", "-c"}},
		{[]string{"--server", "http://127.0.0.1:4096"}, []string{"--server", "http://127.0.0.1:4096"}},
		{[]string{"run", "--server=http://127.0.0.1:4096", "hi"}, []string{"run", "--server=http://127.0.0.1:4096", "hi"}},
	} {
		if got := withOpenCodeStandalone(profile, profile.Command, tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestOpenCodeStandaloneLeavesEverythingElseAlone(t *testing.T) {
	probes := useOpenCodeService(t, true)
	args := []string{"--auto"}
	for _, profile := range []Profile{
		{Name: "pro", Provider: "claude", Command: "claude"},
		{Name: "cx", Provider: "codex", Command: "codex"},
	} {
		if got := withOpenCodeStandalone(profile, profile.Command, args); !reflect.DeepEqual(got, args) {
			t.Errorf("%s: got %q, want the args untouched", profile.Provider, got)
		}
	}
	// openusage runs under an opencode profile's environment, but is not opencode.
	opencode := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	tool := []string{"integrations", "install", "opencode"}
	if got := withOpenCodeStandalone(opencode, "openusage", tool); !reflect.DeepEqual(got, tool) {
		t.Errorf("openusage: got %q, want the args untouched", got)
	}
	// A subcommand that never reaches the service never pays for a probe.
	withOpenCodeStandalone(opencode, opencode.Command, []string{"upgrade"})
	if *probes != 0 {
		t.Errorf("probed %d times for invocations that never take the flag", *probes)
	}
	// DeepSeek runs the same binary and gets the same treatment.
	deepseek := Profile{Name: "ds", Provider: "deepseek", Command: "opencode"}
	if got := withOpenCodeStandalone(deepseek, deepseek.Command, nil); !reflect.DeepEqual(got, []string{"--standalone"}) {
		t.Errorf("deepseek: got %q, want --standalone", got)
	}
}

func TestOpenCodeStandaloneIsNeverGivenToOneX(t *testing.T) {
	useOpenCodeService(t, false)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	for _, args := range [][]string{nil, {"run", "hi"}, {"auth", "login"}} {
		if got := withOpenCodeStandalone(profile, profile.Command, args); !reflect.DeepEqual(got, args) {
			t.Errorf("%q: got %q, want 1.x args untouched", args, got)
		}
	}
	if got := openCodeSessionToolArgs(profile, "import", "/x.json", "/folder"); !reflect.DeepEqual(got, []string{"import", "/x.json"}) {
		t.Errorf("1.x import = %q, want the top-level spelling", got)
	}
}

func TestOpenCodeServiceIsProbedOncePerBinary(t *testing.T) {
	probes := useOpenCodeService(t, true)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	for range 3 {
		withOpenCodeStandalone(profile, profile.Command, nil)
	}
	openCodeSessionToolArgs(profile, "export", "ses_a", "")
	if *probes != 1 {
		t.Fatalf("probed %d times, want once", *probes)
	}
}

// A 2.x resume must spell export and import under `session`, and keep both
// off the background service: one would start a daemon on the archive, the
// other on the instance store.
func TestResumeImportOnOpenCodeTwoUsesSessionCommandsStandalone(t *testing.T) {
	useOpenCodeService(t, true)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := t.TempDir()
	binary := filepath.Join(dir, "opencode")
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\n" +
		"echo \"$PWD|$XDG_DATA_HOME|$*\" >> " + log + "\n" +
		"[ \"$1 $2\" = 'session export' ] && echo '{\"info\":{\"id\":\"ses_a\"},\"messages\":[]}'\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	profile := Profile{Name: "oc", Provider: "opencode", Command: binary}
	workdir := profileWorkdir(t, root, profile.Name)
	folder := t.TempDir()
	writeSubtreeDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`INSERT INTO session VALUES ('ses_a', 'p1', NULL, 'first', '`+folder+`', 1, 1)`)

	instance, unlock, err := acquireIsolatedInstance(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	var stderr bytes.Buffer
	prepareOpenCodeInstance(profile, workdir, instance, "/elsewhere", []string{"--session", "ses_a"}, &stderr)
	if stderr.Len() > 0 {
		t.Fatalf("import reported: %s", stderr.String())
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want one export and one import", calls)
	}
	if want := "|" + filepath.Join(workdir, "data") + "|session export --standalone ses_a"; !strings.HasSuffix(calls[0], want) {
		t.Fatalf("export call = %q, want %q", calls[0], want)
	}
	importPrefix := folder + "|" + filepath.Join(instance, "data") + "|session import --standalone --directory " + folder + " "
	if !strings.HasPrefix(calls[1], importPrefix) {
		t.Fatalf("import call = %q, want prefix %q", calls[1], importPrefix)
	}
}

// The flag has to survive the real launch paths, not just the helper: the
// concurrent TUI launch (after the profile's defaults) and the exclusive one
// a login takes.
func TestOpenCodeTwoLaunchesRunStandalone(t *testing.T) {
	useOpenCodeService(t, true)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := Profile{Name: "oc", Provider: "opencode", Command: "/bin/echo", DefaultArgs: []string{"--auto"}}
	var stdout, stderr strings.Builder
	if err := launch(profile, nil, false, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "--standalone --auto\n" {
		t.Fatalf("TUI launch = %q, want --standalone in front of the defaults", stdout.String())
	}
	stdout.Reset()
	if err := launchExclusive(profile, loginArgs(profile.Provider), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "auth login --standalone\n" {
		t.Fatalf("login = %q, want it standalone", stdout.String())
	}
}
