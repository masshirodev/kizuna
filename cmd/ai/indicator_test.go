package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallStatusLineMergesWithoutLosingSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "claude-personal", Provider: "claude", Command: "claude"}
	settings := filepath.Join(root, appName, "profiles", profile.Name, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"opus[1m]","theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installStatusLine(profile); err != nil {
		t.Fatal(err)
	}
	var merged map[string]any
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &merged); err != nil {
		t.Fatal(err)
	}
	if merged["model"] != "opus[1m]" || merged["theme"] != "dark" {
		t.Fatalf("existing settings were lost: %v", merged)
	}
	if _, ok := merged["statusLine"]; !ok {
		t.Fatalf("statusLine was not written: %v", merged)
	}
}

// The statusLine is the user's; replacing one would silently drop whatever it
// showed, so an existing setting is an error rather than an overwrite.
func TestInstallStatusLineRefusesToReplaceAnExistingOne(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "claude-personal", Provider: "claude"}
	settings := filepath.Join(root, appName, "profiles", profile.Name, "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"mine"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installStatusLine(profile); err == nil {
		t.Fatal("an existing statusLine was replaced")
	}
	data, _ := os.ReadFile(settings)
	if !strings.Contains(string(data), `"mine"`) {
		t.Fatalf("the existing statusLine was modified: %s", data)
	}
}

func TestInstallIndicatorMarksProvidersWithoutANativeLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	path := filepath.Join(root, appName, "profiles.json")
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	cfg := Config{Profiles: []Profile{profile}}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := installIndicator(profile, &cfg, path, &out); err != nil {
		t.Fatal(err)
	}
	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Profiles[0].Indicator != tmuxIndicator {
		t.Fatalf("indicator = %q, want %q", saved.Profiles[0].Indicator, tmuxIndicator)
	}
}

// A Unix socket path is capped near 108 bytes, and the per-instance lock
// directory under the profile root is long enough to blow that on its own.
func TestTmuxSocketPathStaysShortForADeepLockDirectory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	deep := "/home/someone/.config/ai/profiles/a-rather-long-profile-name/instances/run-3288740512"
	socket := tmuxSocketPath(deep)
	if len(socket) > maxSocketPath {
		t.Fatalf("socket path is %d bytes: %s", len(socket), socket)
	}
	// Every caller derives the path rather than recording it, so it must be
	// stable for a given lock directory and distinct between two.
	if socket != tmuxSocketPath(deep) {
		t.Fatal("socket path is not stable")
	}
	if socket == tmuxSocketPath(deep+"x") {
		t.Fatal("two lock directories share a socket")
	}
}

func TestApplyIndicatorLeavesUnmarkedProfilesAlone(t *testing.T) {
	cmd := exec.Command("/usr/bin/env")
	got, err := applyIndicator(cmd, Profile{Name: "claude", Provider: "claude"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != cmd {
		t.Fatalf("an unmarked profile was rewritten: %v", got.Args)
	}
}

func TestWrapWithTmuxKeepsTheProfileEnvironmentAndArguments(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	lockDir := t.TempDir()
	cmd := exec.Command("codex", "exec", "review this repository")
	cmd.Dir = "/home/someone/projects"
	cmd.Env = []string{"CODEX_HOME=/isolated", "AI_PROFILE=codex-work", "TMUX=/outer,123,0"}
	wrapped, err := wrapWithTmux(cmd, Profile{Name: "codex-work", Provider: "codex", Indicator: tmuxIndicator}, lockDir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(wrapped.Args, "\x00")
	for _, want := range []string{"codex", "exec", "review this repository"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argument %q was lost: %v", want, wrapped.Args)
		}
	}
	if !strings.Contains(strings.Join(wrapped.Env, "\n"), "CODEX_HOME=/isolated") {
		t.Errorf("the isolated environment was dropped: %v", wrapped.Env)
	}
	// tmux refuses to start inside another tmux unless TMUX is cleared.
	if strings.Contains(strings.Join(wrapped.Env, "\n"), "TMUX=") {
		t.Errorf("TMUX was not cleared: %v", wrapped.Env)
	}
	if wrapped.Dir != cmd.Dir {
		t.Errorf("working directory = %q, want %q", wrapped.Dir, cmd.Dir)
	}
}

// A provider is free text in profiles.json and reaches the generated config
// verbatim, where a quote or a # would end a string or start an expansion.
func TestTmuxConfigNeutralisesTheProviderString(t *testing.T) {
	conf := tmuxConfig(Profile{Name: "work", Provider: `x" #{pane_pid} "`})
	for _, line := range strings.Split(conf, "\n") {
		if !strings.HasPrefix(line, "set -g status-left ") {
			continue
		}
		if strings.Count(line, `"`) != 2 || strings.Contains(line, "#") {
			t.Fatalf("status-left is not safely quoted: %s", line)
		}
	}
}
