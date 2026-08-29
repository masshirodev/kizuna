package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanEnvironmentRemovesSharedAuthLocations(t *testing.T) {
	got := cleanEnvironment([]string{
		"PATH=/bin",
		"CODEX_HOME=/old/codex",
		"CLAUDE_CONFIG_DIR=/old/claude",
		"XDG_CONFIG_HOME=/old/config",
		"KEEP=value",
	})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "CODEX_HOME") || strings.Contains(joined, "CLAUDE_CONFIG_DIR") || strings.Contains(joined, "XDG_CONFIG_HOME") {
		t.Fatalf("shared auth environment leaked: %v", got)
	}
	if !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "KEEP=value") {
		t.Fatalf("ordinary environment was not preserved: %v", got)
	}
}

func TestBareProfileInvocationUsesProfileAndArguments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{{
		Name:        "ka",
		Provider:    "codex",
		Command:     "/bin/echo",
		DefaultArgs: []string{"from-default"},
	}}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"ka", "from-profile"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "from-default from-profile\n" {
		t.Fatalf("bare profile output = %q, want defaults followed by forwarded argument", stdout.String())
	}
}

func TestUpdateCommandRunsProviderUpdater(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{{
		Name:     "ka",
		Provider: "codex",
		Command:  "/bin/echo",
	}}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"update", "ka"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "update\n" {
		t.Fatalf("update command output = %q, want updater arguments", stdout.String())
	}
}

func TestProfileRunArgsPrependsDefaultsWithoutMutatingInputs(t *testing.T) {
	profile := Profile{DefaultArgs: []string{"--model", "opus"}}
	provided := []string{"--print", "hello"}
	got := profileRunArgs(profile, provided)
	if strings.Join(got, "|") != "--model|opus|--print|hello" {
		t.Fatalf("profileRunArgs = %v", got)
	}
	got[0] = "changed"
	if profile.DefaultArgs[0] != "--model" || provided[0] != "--print" {
		t.Fatalf("profileRunArgs aliased an input slice: profile=%v provided=%v", profile.DefaultArgs, provided)
	}
}

func TestUpdateArgsUsesProviderUpdaters(t *testing.T) {
	cases := map[string]string{
		"claude":   "update",
		"codex":    "update",
		"opencode": "upgrade",
		"deepseek": "upgrade",
	}
	for provider, want := range cases {
		args, err := updateArgs(provider)
		if err != nil || len(args) != 1 || args[0] != want {
			t.Errorf("updateArgs(%q) = %v, %v; want [%s], nil", provider, args, err, want)
		}
	}
	if _, err := updateArgs("unknown"); err == nil {
		t.Fatal("unsupported provider unexpectedly has an updater")
	}
}

func TestParseAndFormatArguments(t *testing.T) {
	input := `--model opus --append-system-prompt "review carefully" 'empty is next' '' path\ with\ spaces`
	want := []string{"--model", "opus", "--append-system-prompt", "review carefully", "empty is next", "", "path with spaces", "it's-safe"}
	input += ` "it's-safe"`
	got, err := parseArguments(input)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseArguments = %#v, want %#v", got, want)
	}
	roundTrip, err := parseArguments(formatArguments(got))
	if err != nil || fmt.Sprint(roundTrip) != fmt.Sprint(want) {
		t.Fatalf("format round trip = %#v, err=%v, formatted=%q", roundTrip, err, formatArguments(got))
	}
	for _, invalid := range []string{`"unfinished`, `trailing\`} {
		if _, err := parseArguments(invalid); err == nil {
			t.Fatalf("parseArguments(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestMissingBareProfileReturnsUsefulError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr strings.Builder
	err := run([]string{"ka"}, &stdout, &stderr)
	if err == nil || err.Error() != `unknown command or profile "ka"; try 'ai help'` {
		t.Fatalf("bare missing profile error = %v", err)
	}
}

func TestSaveConfigUsesPrivateAtomicFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "profiles.json")
	if err := saveConfig(path, Config{Profiles: []Profile{{Name: "personal", Provider: "codex", Command: "codex"}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles[0].Name != "personal" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestProfileEnvironmentIsolatedByName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/shared/config")
	first := profileEnv(Profile{Name: "codex-personal", Provider: "codex"})
	second := profileEnv(Profile{Name: "codex-work", Provider: "codex"})
	if first[0] == second[0] || !strings.Contains(first[0], "codex-personal") || !strings.Contains(second[0], "codex-work") {
		t.Fatalf("profiles are not isolated: %v / %v", first, second)
	}
}

func TestProfileEnvironmentMarksProfileAndProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, profile := range []Profile{
		{Name: "codex-work", Provider: "codex"},
		{Name: "deepseek", Provider: "deepseek"},
		{Name: "custom", Provider: "something-else"},
	} {
		joined := strings.Join(profileEnv(profile), "\n")
		if !strings.Contains(joined, profileNameEnv+"="+profile.Name) {
			t.Errorf("%s is missing %s: %q", profile.Name, profileNameEnv, joined)
		}
		if !strings.Contains(joined, profileProviderEnv+"="+profile.Provider) {
			t.Errorf("%s is missing %s: %q", profile.Name, profileProviderEnv, joined)
		}
	}
}

// A session launched from inside another session must report its own profile,
// not the one it inherited.
func TestCleanEnvironmentRemovesInheritedProfileMarkers(t *testing.T) {
	joined := strings.Join(cleanEnvironment([]string{
		"PATH=/bin",
		profileNameEnv + "=stale",
		profileProviderEnv + "=stale",
	}), "\n")
	if strings.Contains(joined, "stale") {
		t.Fatalf("inherited profile markers leaked: %q", joined)
	}
	if !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("ordinary environment was not preserved: %q", joined)
	}
}

func TestProfileAuthStateDetectsCredentialFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(deepSeekKeyEnv, "")
	cases := []struct {
		profile Profile
		file    []string
		want    authState
	}{
		{Profile{Name: "codex-in", Provider: "codex"}, []string{"codex", "auth.json"}, authPresent},
		{Profile{Name: "codex-out", Provider: "codex"}, nil, authMissing},
		{Profile{Name: "claude-in", Provider: "claude"}, []string{"claude", ".credentials.json"}, authPresent},
		{Profile{Name: "claude-out", Provider: "claude"}, nil, authMissing},
		{Profile{Name: "opencode-in", Provider: "opencode"}, []string{"data", "opencode", "auth.json"}, authPresent},
		{Profile{Name: "opencode-out", Provider: "opencode"}, nil, authMissing},
		{Profile{Name: "deepseek", Provider: "deepseek"}, nil, authMissing},
		{Profile{Name: "custom", Provider: "something-else"}, nil, authUnknown},
	}
	for _, testCase := range cases {
		if testCase.file != nil {
			path := filepath.Join(append([]string{root, appName, "profiles", testCase.profile.Name}, testCase.file...)...)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if got := profileAuthState(testCase.profile); got != testCase.want {
			t.Errorf("profileAuthState(%s/%s) = %v, want %v", testCase.profile.Provider, testCase.profile.Name, got, testCase.want)
		}
	}
}

func TestProfileAuthStateReadsDeepSeekAPIKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := Profile{Name: "deepseek", Provider: "deepseek", Command: "opencode"}
	t.Setenv(deepSeekKeyEnv, "sk-test")
	if got := profileAuthState(profile); got != authAPIKey {
		t.Fatalf("with %s set: got %v, want authAPIKey", deepSeekKeyEnv, got)
	}
	t.Setenv(deepSeekKeyEnv, "")
	if got := profileAuthState(profile); got != authMissing {
		t.Fatalf("without %s: got %v, want authMissing", deepSeekKeyEnv, got)
	}
	// The key is only read to test for emptiness; it must not reach the launch
	// environment as anything other than the value the user already exported.
	t.Setenv(deepSeekKeyEnv, "sk-test")
	for _, value := range profileEnv(profile) {
		if strings.Contains(value, "sk-test") {
			t.Fatalf("profile environment rewrote the API key: %q", value)
		}
	}
}

// An existing profile directory without a credential file must not read as
// authenticated: ensureProfileState creates those directories before login.
func TestProfileAuthStateIgnoresEmptyStateDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := Profile{Name: "fresh", Provider: "codex", Command: "codex"}
	if _, err := ensureProfileState(profile); err != nil {
		t.Fatal(err)
	}
	if got := profileAuthState(profile); got != authMissing {
		t.Fatalf("fresh profile reads as %v, want authMissing", got)
	}
}

func writeProfileFile(t *testing.T, root, body string, parts ...string) {
	t.Helper()
	path := filepath.Join(append([]string{root, appName, "profiles"}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProfileModelReadsEachProviderSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, "model = \"gpt-5.6-luna\"\nmodel_reasoning_effort = \"medium\"\n", "cx", "codex", "config.toml")
	writeProfileFile(t, root, `{"theme":"dark","model":"opus"}`, "cl", "claude", "settings.json")
	writeProfileFile(t, root, `{"recent":[{"providerID":"deepseek","modelID":"deepseek-v4-pro"},{"providerID":"openai","modelID":"gpt-5.6-luna"}]}`,
		"oc", "state", "opencode", "model.json")

	cases := []struct {
		profile Profile
		want    string
	}{
		{Profile{Name: "cx", Provider: "codex"}, "gpt-5.6-luna"},
		{Profile{Name: "cl", Provider: "claude"}, "opus"},
		{Profile{Name: "oc", Provider: "opencode"}, "deepseek/deepseek-v4-pro"},
		{Profile{Name: "missing", Provider: "codex"}, ""},
		{Profile{Name: "ds", Provider: "deepseek"}, ""},
	}
	for _, testCase := range cases {
		if got := profileModel(testCase.profile); got != testCase.want {
			t.Errorf("profileModel(%s/%s) = %q, want %q", testCase.profile.Provider, testCase.profile.Name, got, testCase.want)
		}
	}
}

// The state file wins because OpenCode restores the model last chosen in the
// TUI; the config default only applies before anything has been picked.
func TestProfileModelPrefersOpenCodeStateOverConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode"}
	writeProfileFile(t, root, `{
		// a comment, and a trailing comma below
		"model": "openai/gpt-5.6-terra",
		"mcp": {"url": "https://example.com/mcp"},
	}`, "oc", "config", "opencode", "opencode.jsonc")
	if got := profileModel(profile); got != "openai/gpt-5.6-terra" {
		t.Fatalf("jsonc config model = %q, want openai/gpt-5.6-terra", got)
	}
	writeProfileFile(t, root, `{"recent":[{"providerID":"deepseek","modelID":"deepseek-v4-pro"}]}`, "oc", "state", "opencode", "model.json")
	if got := profileModel(profile); got != "deepseek/deepseek-v4-pro" {
		t.Fatalf("state model = %q, want deepseek/deepseek-v4-pro", got)
	}
}

func TestTomlTopLevelStringIgnoresSectionKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "# a comment\nmodel = \"gpt-5.6-luna\"\n\n[profiles.other]\nmodel = \"should-not-win\"\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if got := tomlTopLevelString(path, "model"); got != "gpt-5.6-luna" {
		t.Fatalf("tomlTopLevelString = %q, want gpt-5.6-luna", got)
	}

	sectionOnly := filepath.Join(dir, "section.toml")
	if err := os.WriteFile(sectionOnly, []byte("[projects.\"/tmp\"]\nmodel = \"nested\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := tomlTopLevelString(sectionOnly, "model"); got != "" {
		t.Fatalf("a key inside a section was reported as global: %q", got)
	}
}

func TestSanitizeJSONCKeepsStringContents(t *testing.T) {
	// The "//" inside a URL must survive: it is data, not a comment.
	input := []byte(`{
		"url": "https://example.com/a//b", // trailing note
		/* block */
		"model": "openai/gpt-5.6-terra",
	}`)
	var parsed struct {
		URL   string `json:"url"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(sanitizeJSONC(input), &parsed); err != nil {
		t.Fatalf("sanitized output is not valid JSON: %v\n%s", err, sanitizeJSONC(input))
	}
	if parsed.URL != "https://example.com/a//b" {
		t.Errorf("URL was mangled: %q", parsed.URL)
	}
	if parsed.Model != "openai/gpt-5.6-terra" {
		t.Errorf("model = %q", parsed.Model)
	}
}

func TestProfileLockPreventsConcurrentLaunches(t *testing.T) {
	dir := t.TempDir()
	unlock, err := acquireProfileLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := acquireProfileLock(dir); err == nil {
		t.Fatal("second lock unexpectedly succeeded")
	}
}

func TestCodexAndClaudeUseIndependentRunLocks(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			workdir := t.TempDir()
			profile := Profile{Name: "personal", Provider: provider, Command: provider}
			firstDir, unlockFirst, err := acquireProfileRunLock(profile, workdir)
			if err != nil {
				t.Fatal(err)
			}
			defer unlockFirst()
			secondDir, unlockSecond, err := acquireProfileRunLock(profile, workdir)
			if err != nil {
				t.Fatalf("second %s instance was refused: %v", provider, err)
			}
			defer unlockSecond()
			if firstDir == secondDir {
				t.Fatalf("concurrent runs shared one lock directory: %s", firstDir)
			}
			for _, dir := range []string{firstDir, secondDir} {
				if filepath.Base(filepath.Dir(dir)) != instancesDirectory {
					t.Fatalf("instance lock %q is not beneath %s", dir, instancesDirectory)
				}
			}
		})
	}
}

func TestOpenCodeKeepsExclusiveRunLock(t *testing.T) {
	workdir := t.TempDir()
	profile := Profile{Name: "open", Provider: "opencode", Command: "opencode"}
	_, unlock, err := acquireProfileRunLock(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, _, err := acquireProfileRunLock(profile, workdir); err == nil {
		t.Fatal("second OpenCode run unexpectedly acquired the profile lock")
	}
}

func TestExclusiveProfileOperationAndInstancesExcludeEachOther(t *testing.T) {
	workdir := t.TempDir()
	profile := Profile{Name: "personal", Provider: "codex", Command: "codex"}
	_, unlockInstance, err := acquireProfileRunLock(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireProfileLock(workdir); err == nil {
		unlockInstance()
		t.Fatal("exclusive operation acquired a profile with a running instance")
	}
	unlockInstance()

	unlockExclusive, err := acquireProfileLock(workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockExclusive()
	if _, _, err := acquireProfileRunLock(profile, workdir); err == nil {
		t.Fatal("instance acquired a profile held by an exclusive operation")
	}
}

func TestInstanceDirectoryIsRemovedAfterRun(t *testing.T) {
	workdir := t.TempDir()
	profile := Profile{Name: "personal", Provider: "claude", Command: "claude"}
	instanceDir, unlock, err := acquireProfileRunLock(profile, workdir)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(instanceDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instance directory survived cleanup: %v", err)
	}
}

func TestProfileLockReclaimsStaleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".active.lock")
	const stalePID = 1 << 30
	if active, err := profileLockIsActiveForTestPID(stalePID); err != nil || active {
		t.Skipf("cannot find a demonstrably unused PID: active=%v err=%v", active, err)
	}
	if err := os.WriteFile(lockPath, []byte("1073741824\n"), 0600); err != nil {
		t.Fatal(err)
	}

	unlock, err := acquireProfileLock(dir)
	if err != nil {
		t.Fatalf("acquireProfileLock did not reclaim stale lock: %v", err)
	}
	defer unlock()
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != fmt.Sprint(os.Getpid()) {
		t.Fatalf("replacement lock = %q, want current PID %d", data, os.Getpid())
	}
}

func profileLockIsActiveForTestPID(pid int) (bool, error) {
	dir, err := os.MkdirTemp("", "ai-session-lock-test-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, ".active.lock")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n", pid)), 0600); err != nil {
		return false, err
	}
	return profileLockIsActive(path)
}

func TestResumeArgsUseEachProviderSpelling(t *testing.T) {
	cases := map[string]string{
		"claude":   "--resume",
		"codex":    "resume",
		"opencode": "--continue",
		"deepseek": "--continue",
	}
	for provider, want := range cases {
		args, err := resumeArgs(provider)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if strings.Join(args, " ") != want {
			t.Fatalf("%s resume args = %v, want %q", provider, args, want)
		}
	}
	if _, err := resumeArgs("unknown"); err == nil {
		t.Fatal("an unknown provider reported a resume command")
	}
}

func TestHijackArgsPreferTheDiscoveredSession(t *testing.T) {
	session := instanceSession{id: "abc123"}
	cases := map[string]struct{ exact, fallback string }{
		"claude":   {"--resume abc123", "--continue"},
		"codex":    {"resume abc123", "resume --last"},
		"opencode": {"--continue", "--continue"},
	}
	for provider, want := range cases {
		args, err := hijackArgs(provider, session)
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if strings.Join(args, " ") != want.exact {
			t.Fatalf("%s hijack args = %v, want %q", provider, args, want.exact)
		}
		args, err = hijackArgs(provider, instanceSession{})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if strings.Join(args, " ") != want.fallback {
			t.Fatalf("%s fallback args = %v, want %q", provider, args, want.fallback)
		}
	}
	if _, err := hijackArgs("unknown", session); err == nil {
		t.Fatal("an unknown provider reported a hijack command")
	}
}

func TestInstanceMetaRecordsTheLaunchFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	workdir := filepath.Join(root, appName, "profiles", profile.Name)
	lockDir := filepath.Join(workdir, instancesDirectory, "run-one")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, ".active.lock"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if err := setProfileInstanceMeta(lockDir, "/work/lattice"); err != nil {
		t.Fatal(err)
	}

	instances, err := activeProfileInstances(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 || instances[0].folder != "/work/lattice" {
		t.Fatalf("instances = %+v, want the recorded launch folder", instances)
	}
}

func TestInstanceMetaIsAbsentUntilWritten(t *testing.T) {
	if meta := readInstanceMeta(t.TempDir()); meta.Folder != "" {
		t.Fatalf("meta = %+v, want an empty folder", meta)
	}
}

func TestExclusiveUnlockRemovesInstanceMetadata(t *testing.T) {
	workdir := t.TempDir()
	_, unlock, err := acquireExclusiveRunLock(workdir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setProfileInstanceMeta(workdir, "/work/hub"); err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, err := os.Stat(filepath.Join(workdir, instanceMetaFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("instance metadata outlived its lock: %v", err)
	}
}
