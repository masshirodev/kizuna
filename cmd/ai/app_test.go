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

// seedThirdProfile adds a profile that is deliberately not in any app, so the
// member tests have something to widen a roster with.
func seedThirdProfile(t *testing.T, cfg *Config) {
	t.Helper()
	root, err := profileRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles = append(cfg.Profiles, Profile{Name: "outsider", Provider: "opencode", Command: "/bin/echo"})
	if err := os.MkdirAll(filepath.Join(root, "outsider"), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestAppMemberAddWidensRosterWithoutMovingTheSymlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, root := seedTwoMemberProfiles(t)
	seedThirdProfile(t, &cfg)
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
	if err := run([]string{"app", "member", "add", "shiori", "outsider"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.Members, []string{"gemini", "agy", "outsider"}) {
		t.Fatalf("members = %v, want [gemini agy outsider]", app.Members)
	}
	if app.Active != "gemini" {
		t.Fatalf("active member = %q, want gemini -- adding must not change what the app resolves to", app.Active)
	}

	// The whole point of an app profile is a path that does not move, so a
	// roster change must leave the link exactly where it was.
	target, err := filepath.EvalSymlinks(appLink(root, "shiori"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "gemini"))
	if err != nil {
		t.Fatal(err)
	}
	if target != want {
		t.Fatalf("app symlink resolved to %q, want %q", target, want)
	}
}

func TestAppMemberAddRejectsUnknownDuplicateAndUnknownApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	seedThirdProfile(t, &cfg)
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

	if err := run([]string{"app", "member", "add", "shiori", "ghost"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error adding a profile that does not exist")
	}
	if err := run([]string{"app", "member", "add", "shiori", "agy"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error adding a profile that is already a member")
	}
	if err := run([]string{"app", "member", "add", "shiori", "outsider", "outsider"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error naming the same profile twice in one call")
	}
	if err := run([]string{"app", "member", "add", "ghostapp", "outsider"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unknown app")
	}

	// A rejected call must leave the roster exactly as it was, not half-applied.
	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.Members, []string{"gemini", "agy"}) {
		t.Fatalf("members = %v, want the original [gemini agy]", app.Members)
	}
}

func TestAppMemberRemoveNarrowsRoster(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := seedTwoMemberProfiles(t)
	seedThirdProfile(t, &cfg)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini", "agy", "outsider"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "member", "remove", "shiori", "outsider"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.Members, []string{"gemini", "agy"}) {
		t.Fatalf("members = %v, want [gemini agy]", app.Members)
	}
	if app.Active != "gemini" {
		t.Fatalf("active member = %q, want gemini", app.Active)
	}
}

func TestAppMemberRemoveRefusesTheActiveMemberAndTheLastOne(t *testing.T) {
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

	// Dropping the active member would have to silently repoint the symlink,
	// which is the one thing something else's static config cannot survive.
	if err := run([]string{"app", "member", "remove", "shiori", "gemini"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error removing the active member")
	}
	// Emptying the app leaves the link resolving to a member it no longer names.
	if err := run([]string{"app", "member", "remove", "shiori", "gemini", "agy"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error removing every member")
	}
	if err := run([]string{"app", "member", "remove", "shiori", "ghost"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error removing a profile that is not a member")
	}

	saved, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	app, err := findApp(saved, "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(app.Members, []string{"gemini", "agy"}) {
		t.Fatalf("members = %v, want the original [gemini agy]", app.Members)
	}

	target, err := filepath.EvalSymlinks(appLink(root, "shiori"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "gemini"))
	if err != nil {
		t.Fatal(err)
	}
	if target != want {
		t.Fatalf("app symlink resolved to %q, want %q", target, want)
	}
}

// The switch that add and remove exist to make possible: widen the roster, then
// hand the app over to the new member.
func TestAppMemberAddThenUseSwitchesToTheNewMember(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, root := seedTwoMemberProfiles(t)
	seedThirdProfile(t, &cfg)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if err := run([]string{"app", "add", "shiori", "gemini"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	// A one-member app could not be switched at all before these commands.
	if err := run([]string{"app", "use", "shiori", "outsider"}, &stdout, &stderr); err == nil {
		t.Fatal("expected an error switching to a profile that is not yet a member")
	}
	if err := run([]string{"app", "member", "add", "shiori", "outsider"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"app", "use", "shiori", "outsider"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}

	target, err := filepath.EvalSymlinks(appLink(root, "shiori"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "outsider"))
	if err != nil {
		t.Fatal(err)
	}
	if target != want {
		t.Fatalf("app symlink resolved to %q, want %q", target, want)
	}
	resolved, err := resolveProfile(mustLoad(t, path), "shiori")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "outsider" {
		t.Fatalf("resolveProfile(shiori) = %q, want outsider", resolved.Name)
	}
}

func mustLoad(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
