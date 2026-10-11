package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMoveLegacyConfigMovesTheDirectoryAndLeavesALink(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "ai", "profiles.json"), `{"profiles":[]}`)
	writeTestFile(t, filepath.Join(base, "ai", "profiles", "max", "claude", "settings.json"), `{}`)

	moved, err := moveLegacyConfig(base)
	if err != nil || !moved {
		t.Fatalf("moveLegacyConfig = %v, %v; want moved", moved, err)
	}
	if _, err := os.Stat(filepath.Join(base, "kizuna", "profiles", "max", "claude", "settings.json")); err != nil {
		t.Fatalf("the profiles did not move: %v", err)
	}
	target, err := os.Readlink(filepath.Join(base, "ai"))
	if err != nil || target != "kizuna" {
		t.Fatalf("old path = %q, %v; want a link to kizuna", target, err)
	}
	// A session an older binary launched still reaches its files.
	if _, err := os.Stat(filepath.Join(base, "ai", "profiles", "max", "claude", "settings.json")); err != nil {
		t.Fatalf("the old path no longer resolves: %v", err)
	}

	moved, err = moveLegacyConfig(base)
	if err != nil || moved {
		t.Fatalf("second run = %v, %v; want nothing to do", moved, err)
	}
}

func TestMoveLegacyConfigLeavesAnotherToolsDirectoryAlone(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "ai", "config.toml"), "someone else's")
	if moved, err := moveLegacyConfig(base); err != nil || moved {
		t.Fatalf("moveLegacyConfig = %v, %v; want a directory without profiles.json left alone", moved, err)
	}
	if _, err := os.Stat(filepath.Join(base, "kizuna")); !os.IsNotExist(err) {
		t.Fatalf("kizuna's directory was created: %v", err)
	}
}

func TestMoveLegacyConfigNeverOverwritesKizunasOwnDirectory(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "ai", "profiles.json"), `{"profiles":[]}`)
	writeTestFile(t, filepath.Join(base, "kizuna", "profiles.json"), `{"profiles":[{"name":"new"}]}`)
	if moved, err := moveLegacyConfig(base); err != nil || moved {
		t.Fatalf("moveLegacyConfig = %v, %v; want both left as they are", moved, err)
	}
	if info, err := os.Lstat(filepath.Join(base, "ai")); err != nil || !info.IsDir() {
		t.Fatalf("the old directory was touched: %v", err)
	}
}

func TestReplaceLegacyStatusLineRewritesOnlyOurs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	ours := Profile{Name: "max", Provider: "claude"}
	theirs := Profile{Name: "pro", Provider: "claude"}
	oursPath := filepath.Join(root, appName, "profiles", "max", "claude", "settings.json")
	theirsPath := filepath.Join(root, appName, "profiles", "pro", "claude", "settings.json")
	writeTestFile(t, oursPath, `{"model":"opus","statusLine":{"type":"command","command":"printf '[%s] %s' \"${AI_PROFILE:-no profile}\" \"${PWD##*/}\""}}`)
	writeTestFile(t, theirsPath, `{"statusLine":{"type":"command","command":"echo $AI_PROFILE"}}`)

	if changed, err := replaceLegacyStatusLine(ours); err != nil || !changed {
		t.Fatalf("replaceLegacyStatusLine(ours) = %v, %v", changed, err)
	}
	data, _ := os.ReadFile(oursPath)
	if !strings.Contains(string(data), "KIZUNA_PROFILE") || strings.Contains(string(data), "AI_PROFILE") || !strings.Contains(string(data), `"opus"`) {
		t.Fatalf("settings after the rewrite: %s", data)
	}
	if changed, err := replaceLegacyStatusLine(theirs); err != nil || changed {
		t.Fatalf("replaceLegacyStatusLine(theirs) = %v, %v; want it left alone", changed, err)
	}
}

func TestLegacyConfigPathSpellsAPathThroughTheOldDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(configHomeEnv, "")
	t.Setenv(legacyEnvName(configHomeEnv), "")
	in := filepath.Join(root, "kizuna", "profiles", "oc", "data")
	if got, want := legacyConfigPath(in), filepath.Join(root, "ai", "profiles", "oc", "data"); got != want {
		t.Fatalf("legacyConfigPath = %q, want %q", got, want)
	}
	if got := legacyConfigPath("/somewhere/else"); got != "" {
		t.Fatalf("legacyConfigPath(outside) = %q, want empty", got)
	}
}

func TestSessionEnvFallsBackToTheVariableAnOlderBinarySet(t *testing.T) {
	t.Setenv(profileNameEnv, "")
	t.Setenv("AI_PROFILE", "max")
	if got := sessionEnv(profileNameEnv); got != "max" {
		t.Fatalf("sessionEnv = %q, want the legacy value", got)
	}
	t.Setenv(profileNameEnv, "pro")
	if got := sessionEnv(profileNameEnv); got != "pro" {
		t.Fatalf("sessionEnv = %q, want the current name to win", got)
	}
}

func TestCleanEnvironmentDropsTheLegacyMarkersToo(t *testing.T) {
	got := cleanEnvironment([]string{"AI_PROFILE=max", "AI_INSTANCE_DIR=/x", "KIZUNA_PROFILE=pro", "PATH=/bin"})
	if !slices.Equal(got, []string{"PATH=/bin"}) {
		t.Fatalf("cleanEnvironment kept %v", got)
	}
}

func TestRemoveMessagingServersKeepsSomeoneElsesServerCalledAi(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "max", Provider: "claude"}
	if err := mergeMCPServers(profile, []mcpServer{
		{Name: "ai", Transport: mcpStdio, Command: "/bin/ai", Args: []string{"mcp", "serve"}},
		{Name: messagingMCPName, Transport: mcpStdio, Command: "/bin/kizuna", Args: []string{"mcp", "serve"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := removeMessagingServers(profile); err != nil {
		t.Fatal(err)
	}
	servers, err := readMCPServers(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Fatalf("left %v, want both of ours gone", servers)
	}

	if err := mergeMCPServers(profile, []mcpServer{{Name: "ai", Transport: mcpStdio, Command: "/usr/bin/some-ai-tool"}}); err != nil {
		t.Fatal(err)
	}
	if err := removeMessagingServers(profile); err != nil {
		t.Fatal(err)
	}
	if servers, _ := readMCPServers(profile); len(servers) != 1 {
		t.Fatalf("a server called ai that is not ours was removed: %v", servers)
	}
}

func TestVersionLineNamesTheReleaseAndWhereTheBinaryCameFrom(t *testing.T) {
	got := versionLine(buildStamp{revision: "0123456789abcdef", time: "2026-10-11T00:00:00Z"}, "the AUR (kizuna-git)")
	want := "kizuna " + version + " (0123456, built 2026-10-11T00:00:00Z, from the AUR (kizuna-git))"
	if got != want {
		t.Fatalf("versionLine = %q, want %q", got, want)
	}
	if got := versionLine(buildStamp{}, ""); got != "kizuna "+version+" (no build revision)" {
		t.Fatalf("versionLine without a stamp = %q", got)
	}
}

func TestAPackagedBuildRefusesToUpdateItself(t *testing.T) {
	old := packaged
	t.Cleanup(func() { packaged = old })
	packaged = "the AUR (kizuna-bin)"
	if err := selfUpdateCommand(nil, &strings.Builder{}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "the AUR (kizuna-bin)") {
		t.Fatalf("selfUpdateCommand = %v, want a refusal naming the package", err)
	}
	if status := checkForUpdate(true, time.Time{}); status.Known || !strings.Contains(status.Reason, "package manager") {
		t.Fatalf("checkForUpdate = %+v, want no check", status)
	}
}

func TestRenameNoticeTellsAnAiUserTheNewName(t *testing.T) {
	if got := renameNotice("/home/u/go/bin/ai", false); !strings.Contains(got, "type `kizuna`") {
		t.Fatalf("started as ai: %q", got)
	}
	if got := renameNotice("/usr/bin/kizuna", true); !strings.Contains(got, "ai-session is kizuna now") {
		t.Fatalf("the migrating run: %q", got)
	}
	if got := renameNotice("/usr/bin/kizuna", false); got != "" {
		t.Fatalf("started as kizuna after the move: %q, want nothing", got)
	}
}
