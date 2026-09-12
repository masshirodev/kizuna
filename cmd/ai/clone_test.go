package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeProfileState lays down the shape of a used Claude Code profile: the
// settings and skills that are configuration, and the credential, history, and
// project state that is the account.
func claudeProfileState(t *testing.T, root, name string) {
	t.Helper()
	writeProfileFile(t, root, `{"model":"opus","statusLine":{"type":"command"}}`, name, "claude", "settings.json")
	writeProfileFile(t, root, `{"userID":"abc","mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"}}}`, name, "claude", ".claude.json")
	writeProfileFile(t, root, `{"token":"secret"}`, name, "claude", ".credentials.json")
	writeProfileFile(t, root, `{"prompt":"hello"}`, name, "claude", "history.jsonl")
	writeProfileFile(t, root, `{}`, name, "claude", "projects", "home", "session.jsonl")
	writeSkill(t, root, name, "claude", "release", "Cut a release", "echo hi\n")
}

func cloneTestConfig(t *testing.T, root string, profiles ...Profile) (Config, string) {
	t.Helper()
	path := filepath.Join(root, appName, "profiles.json")
	cfg := Config{Profiles: profiles}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func TestCloneCopiesTheSetupAndLeavesTheAccountBehind(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	claudeProfileState(t, root, "max")
	source := claudeProfile("max")
	source.DefaultArgs = []string{"--permission-mode", "auto"}
	source.Notes = "personal"
	cfg, path := cloneTestConfig(t, root, source)

	var out strings.Builder
	if err := cloneProfile("max", "spare", false, &cfg, path, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ai login spare") {
		t.Fatalf("the clone did not say the account has to be logged in:\n%s", out.String())
	}

	clone, err := findProfile(cfg, "spare")
	if err != nil {
		t.Fatal(err)
	}
	if clone.Provider != "claude" || strings.Join(clone.DefaultArgs, " ") != "--permission-mode auto" || clone.Notes != "personal" {
		t.Fatalf("the clone launches differently from its source: %+v", clone)
	}

	dir := filepath.Join(root, appName, "profiles", "spare", "claude")
	for _, carried := range []string{
		filepath.Join(dir, "settings.json"),
		filepath.Join(dir, skillsDirName, "release", "SKILL.md"),
		filepath.Join(dir, skillsDirName, "release", "scripts", "run.sh"),
	} {
		if _, err := os.Stat(carried); err != nil {
			t.Fatalf("the clone is missing configuration it should have: %v", err)
		}
	}
	for _, left := range []string{".credentials.json", "history.jsonl", "projects"} {
		if _, err := os.Stat(filepath.Join(dir, left)); err == nil {
			t.Fatalf("the clone carried %q, which belongs to the account rather than the setup", left)
		}
	}
	// Only the MCP servers cross out of .claude.json; the account's own keys
	// stay in the profile that owns them.
	written := readFile(t, filepath.Join(dir, ".claude.json"))
	if !strings.Contains(written, "ctx.example") {
		t.Fatalf("the clone lost its MCP servers:\n%s", written)
	}
	if strings.Contains(written, "abc") {
		t.Fatalf("the clone carried the source account's identity:\n%s", written)
	}
}

func TestCloneWithStateCarriesTheAccountButNotTheLocks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	claudeProfileState(t, root, "max")
	writeProfileFile(t, root, "1234\n", "max", instancesDirectory, "run-a", ".active.lock")
	cfg, path := cloneTestConfig(t, root, claudeProfile("max"))

	var out strings.Builder
	if err := cloneProfile("max", "spare", true, &cfg, path, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "credentials came with it") {
		t.Fatalf("a clone that copied credentials did not say so:\n%s", out.String())
	}
	dir := filepath.Join(root, appName, "profiles", "spare")
	if _, err := os.Stat(filepath.Join(dir, "claude", ".credentials.json")); err != nil {
		t.Fatalf("--with-state left the credentials behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, instancesDirectory)); err == nil {
		t.Fatal("the clone claimed the source's running instances")
	}
}

func TestCloneRefusesANameAlreadyInUse(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	claudeProfileState(t, root, "max")
	cfg, path := cloneTestConfig(t, root, claudeProfile("max"), claudeProfile("spare"))

	if err := cloneProfile("max", "spare", false, &cfg, path, &strings.Builder{}); err == nil {
		t.Fatal("cloning over an existing profile was allowed")
	}
	if err := cloneProfile("max", "apps", false, &cfg, path, &strings.Builder{}); err == nil {
		t.Fatal("a clone took the name reserved for application profiles")
	}
	if err := cloneProfile("max", "not a name", false, &cfg, path, &strings.Builder{}); err == nil {
		t.Fatal("a clone took a name a directory cannot be called")
	}
}

func TestCloneCopiesSymlinksAsSymlinks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	claudeProfileState(t, root, "max")
	shared := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(shared, []byte("shared rules\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, appName, "profiles", "max", "claude", "AGENTS.md")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}
	cfg, path := cloneTestConfig(t, root, claudeProfile("max"))
	if err := cloneProfile("max", "spare", false, &cfg, path, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	cloned := filepath.Join(root, appName, "profiles", "spare", "claude", "AGENTS.md")
	target, err := os.Readlink(cloned)
	if err != nil {
		t.Fatalf("the clone turned a symlink into a copy: %v", err)
	}
	if target != shared {
		t.Fatalf("symlink points at %q, want %q", target, shared)
	}
}
