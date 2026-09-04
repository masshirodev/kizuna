package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func seedTwoMemberProfiles(t *testing.T) (Config, string) {
	t.Helper()
	root, err := profileRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Profiles: []Profile{
		{Name: "gemini", Provider: "antigravity", Command: "/bin/echo"},
		{Name: "agy", Provider: "codex", Command: "/bin/echo"},
	}}
	for _, profile := range cfg.Profiles {
		if err := os.MkdirAll(filepath.Join(root, profile.Name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, root
}

func TestAppAddPointsSymlinkAtFirstMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, root := seedTwoMemberProfiles(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	link := appLink(root, "shiori")
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("app symlink did not resolve: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "gemini"))
	if err != nil {
		t.Fatal(err)
	}
	if target != want {
		t.Fatalf("app symlink resolved to %q, want %q", target, want)
	}

	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if app.Active != "gemini" {
		t.Fatalf("active member = %q, want gemini", app.Active)
	}
}

func TestAppAddRejectsUnknownMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "ghost"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unknown member")
	}
	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Apps) != 0 {
		t.Fatalf("app was persisted despite the invalid member: %v", saved.Apps)
	}
}

func TestAppAddRejectsNameCollisions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "gemini", "agy"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error naming an app after an existing profile")
	}
	if err := run([]string{"app", "add", "apps", "gemini"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error using the reserved name \"apps\"")
	}
	if err := run([]string{"app", "add", "shiori", "gemini"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "add", "shiori", "agy"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error naming an app after an existing app")
	}
	if err := run([]string{"profile", "add", "shiori", "codex"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error naming a profile after an existing app")
	}
}

func TestAppUseRepointsSymlinkAndPersistsActive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, root := seedTwoMemberProfiles(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "use", "shiori", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	link := appLink(root, "shiori")
	target, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("app symlink did not resolve: %v", err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "agy"))
	if err != nil {
		t.Fatal(err)
	}
	if target != want {
		t.Fatalf("app symlink resolved to %q, want %q", target, want)
	}

	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if app.Active != "agy" {
		t.Fatalf("active member = %q, want agy", app.Active)
	}
}

func TestAppUseRejectsNonMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	cfg.Profiles = append(cfg.Profiles, Profile{Name: "outsider", Provider: "opencode", Command: "/bin/echo"})
	root, err := profileRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "outsider"), 0700); err != nil {
		t.Fatal(err)
	}
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "use", "shiori", "outsider"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error switching to a non-member profile")
	}
	if err := run([]string{"app", "use", "ghost", "gemini"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unknown app")
	}
}

func TestResolveProfileFollowsActiveMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	cfg.Apps = []App{{Name: "shiori", Members: []string{"gemini", "agy"}, Active: "agy"}}

	resolved, err := resolveProfile(cfg, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	want, err := findProfile(cfg, "agy")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolved, want) {
		t.Fatalf("resolveProfile(shiori) = %+v, want %+v", resolved, want)
	}

	plain, err := findProfile(cfg, "gemini")
	if err != nil {
		t.Fatal(err)
	}
	resolvedPlain, err := resolveProfile(cfg, "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resolvedPlain, plain) {
		t.Fatalf("resolveProfile(gemini) = %+v, want %+v", resolvedPlain, plain)
	}

	if _, err := resolveProfile(cfg, "ghost"); err == nil {
		t.Fatal("expected an error for a name that is neither a profile nor an app")
	}
}

func TestBareAppInvocationLaunchesActiveMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "use", "shiori", "agy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := run([]string{"shiori", "hello"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello\n" {
		t.Fatalf("bare app output = %q, want the forwarded argument echoed back", stdout.String())
	}
}
