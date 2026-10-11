package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, profile, provider, name, description, extra string) {
	t.Helper()
	var dir string
	switch provider {
	case "claude":
		dir = filepath.Join(root, appName, "profiles", profile, "claude", skillsDirName, name)
	case "codex":
		dir = filepath.Join(root, appName, "profiles", profile, "codex", skillsDirName, name)
	default:
		dir = filepath.Join(root, appName, "profiles", profile, "config", "opencode", skillsDirName, name)
	}
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte(extra), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadSkillsTakesOnlyFoldersWithAManifest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeSkill(t, root, "cl", "claude", "release", "Cut a release", "")
	skills := filepath.Join(root, appName, "profiles", "cl", "claude", skillsDirName)
	if err := os.MkdirAll(filepath.Join(skills, "leftovers"), 0700); err != nil {
		t.Fatal(err)
	}
	// Codex keeps its built-ins under a dotted folder beside the real ones.
	if err := os.MkdirAll(filepath.Join(skills, ".system", "imagegen"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, ".system", "imagegen", "SKILL.md"), []byte("---\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}

	found, err := readSkills(claudeProfile("cl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].name != "release" {
		t.Fatalf("read %+v, want only the folder with a SKILL.md in it", found)
	}
	if found[0].description != "Cut a release" {
		t.Fatalf("description = %q, want the frontmatter line", found[0].description)
	}
}

func TestCopySkillsCarriesTheWholeFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeSkill(t, root, "cl", "claude", "release", "Cut a release", "echo hi\n")
	writeSkill(t, root, "cl", "claude", "triage", "Sort the inbox", "")

	copied, err := copySkills(claudeProfile("cl"), codexProfile("cx"), []string{"release"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(copied, ",") != "release" {
		t.Fatalf("copied %v, want only the named skill", copied)
	}
	target := filepath.Join(root, appName, "profiles", "cx", "codex", skillsDirName, "release")
	if got := readFile(t, filepath.Join(target, "scripts", "run.sh")); got != "echo hi\n" {
		t.Fatalf("the skill's script did not come with it: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, appName, "profiles", "cx", "codex", skillsDirName, "triage")); err == nil {
		t.Fatal("a skill that was not named was copied anyway")
	}
}

func TestCopySkillsRefusesAnInstalledNameAndReplacesWholeFolders(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeSkill(t, root, "cl", "claude", "release", "New wording", "")
	writeSkill(t, root, "cx", "codex", "release", "Old wording", "stale\n")

	if _, err := copySkills(claudeProfile("cl"), codexProfile("cx"), nil, false); err == nil {
		t.Fatal("an installed skill was overwritten without --replace")
	}
	target := filepath.Join(root, appName, "profiles", "cx", "codex", skillsDirName, "release")
	if got := readFile(t, filepath.Join(target, "SKILL.md")); !strings.Contains(got, "Old wording") {
		t.Fatalf("the refused copy still wrote something:\n%s", got)
	}
	if _, err := copySkills(claudeProfile("cl"), codexProfile("cx"), nil, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(target, "SKILL.md")); !strings.Contains(got, "New wording") {
		t.Fatalf("--replace did not overwrite the skill:\n%s", got)
	}
	// A replacement is the whole folder, so the previous skill's leftovers
	// cannot survive underneath the new one's manifest.
	if _, err := os.Stat(filepath.Join(target, "scripts", "run.sh")); err == nil {
		t.Fatal("the replaced skill kept a file the new one does not have")
	}
}

func TestSkillsAreRefusedForProvidersThatHaveNone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := readSkills(Profile{Name: "ag", Provider: "antigravity"}); err == nil {
		t.Fatal("antigravity reported a skills directory it does not have")
	}
}
