package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRanma puts a `ranma` on PATH that prints what it was asked to run and
// the agent-teams flag it received, instead of exec'ing anything.
func fakeRanma(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"ranma $* teams=$" + agentTeamsEnv + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ranma"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shimProfileConfig(t *testing.T, profile Profile) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{profile}}); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTmuxShimWrapsALaunchInARanmaPane(t *testing.T) {
	fakeRanma(t)
	t.Setenv(ranmaSocketEnv, "/run/user/1000/ranma/0.sock")
	t.Setenv(ranmaPaneEnv, "3")
	t.Setenv("TMUX", "")
	t.Setenv(agentTeamsEnv, "")
	shimProfileConfig(t, Profile{Name: "max2", Provider: "claude", Command: "/bin/echo", TmuxShim: true})

	var stdout, stderr strings.Builder
	if err := run([]string{"max2", "hello world"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "ranma tmux-shim -- /bin/echo hello world teams=1\n"; got != want {
		t.Fatalf("launch = %q, want %q", got, want)
	}
}

// Outside ranma there is nowhere to put a pane, so the profile runs as it
// always did rather than failing.
func TestTmuxShimIsInertOutsideRanma(t *testing.T) {
	fakeRanma(t)
	t.Setenv(ranmaSocketEnv, "")
	shimProfileConfig(t, Profile{Name: "max2", Provider: "claude", Command: "/bin/echo", TmuxShim: true})

	var stdout, stderr strings.Builder
	if err := run([]string{"max2", "plain"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "plain\n" {
		t.Fatalf("launch outside ranma = %q, want the command run directly", stdout.String())
	}
}

func TestTmuxShimLeavesUnmarkedProfilesAlone(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{ranmaSocketEnv + "=/s", ranmaPaneEnv + "=1"}
	got, err := applyTmuxShim(cmd, Profile{Name: "max2", Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got != cmd || len(got.Env) != 2 {
		t.Fatalf("an unmarked profile was rewritten: %v %v", got.Args, got.Env)
	}
}

// A launch from a pane the shim opened already has TMUX naming this ranma;
// it only needs the teams flag, not a second shim.
func TestTmuxShimDoesNotNestInsideItself(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{ranmaSocketEnv + "=/s", ranmaPaneEnv + "=4", "TMUX=/s,0,0"}
	got, err := applyTmuxShim(cmd, Profile{Name: "max2", Provider: "claude", TmuxShim: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != cmd {
		t.Fatalf("launch under the shim was wrapped again: %v", got.Args)
	}
	if envValue(got.Env, agentTeamsEnv) != "1" {
		t.Fatalf("teams flag missing: %v", got.Env)
	}
}

// A real tmux's TMUX is not ours, so the launch still goes through the shim.
func TestTmuxShimWrapsWhenTmuxIsSomebodyElses(t *testing.T) {
	fakeRanma(t)
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{ranmaSocketEnv + "=/s", ranmaPaneEnv + "=4", "TMUX=/tmp/tmux-1000/default,1,0"}
	got, err := applyTmuxShim(cmd, Profile{Name: "codex", Provider: "codex", TmuxShim: true})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got.Args[0]) != "ranma" || got.Args[1] != "tmux-shim" {
		t.Fatalf("launch = %v, want it under ranma tmux-shim", got.Args)
	}
	// Only Claude has agent teams to switch on.
	if envValue(got.Env, agentTeamsEnv) != "" {
		t.Fatalf("teams flag set for a codex profile: %v", got.Env)
	}
}

func TestTmuxShimKeepsTheUsersTeamsSetting(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{ranmaSocketEnv + "=/s", ranmaPaneEnv + "=4", "TMUX=/s,0,0", agentTeamsEnv + "=0"}
	got, err := applyTmuxShim(cmd, Profile{Name: "max2", Provider: "claude", TmuxShim: true})
	if err != nil {
		t.Fatal(err)
	}
	if envValue(got.Env, agentTeamsEnv) != "0" {
		t.Fatalf("the user's teams setting was overridden: %v", got.Env)
	}
}

func TestTmuxShimRefusesTheTmuxIndicator(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{ranmaSocketEnv + "=/s", ranmaPaneEnv + "=4"}
	if _, err := applyTmuxShim(cmd, Profile{Name: "oc", Provider: "opencode", Indicator: tmuxIndicator, TmuxShim: true}); err == nil {
		t.Fatal("the shim was applied under the tmux indicator")
	}
}

func TestIntegrateRanmaTogglesTheProfileSetting(t *testing.T) {
	path := shimProfileConfig(t, Profile{Name: "max2", Provider: "claude", Command: "claude"})

	var stdout, stderr strings.Builder
	if err := run([]string{"integrate", "ranma", "max2"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Profiles[0].TmuxShim {
		t.Fatal("integrate ranma did not turn the shim on")
	}
	if err := run([]string{"integrate", "ranma", "max2", "--off"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = loadConfig(path); cfg.Profiles[0].TmuxShim {
		t.Fatal("integrate ranma --off left the shim on")
	}
	if err := run([]string{"integrate", "statusline", "max2", "--off"}, &stdout, &stderr); err == nil {
		t.Fatal("--off was accepted by an integration that has no off")
	}
}
