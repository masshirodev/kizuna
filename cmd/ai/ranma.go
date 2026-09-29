package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// ranma (the terminal window manager) cannot give a program tmux panes, but its
// tmux shim can: `ranma tmux-shim -- CMD` runs CMD with a `tmux` on PATH that
// opens ranma panes. Claude Code's agent teams find tmux through TMUX and split
// a pane per teammate, so a profile launched under the shim gets its teammates
// beside it instead of in-process. ranma's doc/CONFIG.md, "Programs that drive
// tmux", is the other half of this.
const (
	ranmaSocketEnv = "RANMA_SOCKET"
	ranmaPaneEnv   = "RANMA_PANE"
	agentTeamsEnv  = "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"
)

// wrapLaunch applies every wrapper a profile asks for, in the order they nest.
func wrapLaunch(cmd *exec.Cmd, profile Profile, lockDir string) (*exec.Cmd, error) {
	cmd, err := applyIndicator(cmd, profile, lockDir)
	if err != nil {
		return nil, err
	}
	return applyTmuxShim(cmd, profile)
}

// insideRanma reports whether env belongs to a ranma pane, which is the only
// place the shim can open panes. Both variables are needed: the shim refuses to
// start without the pane it is splitting from.
func insideRanma(env []string) bool {
	return envValue(env, ranmaSocketEnv) != "" && envValue(env, ranmaPaneEnv) != ""
}

// underTmuxShim reports whether TMUX already names this ranma, which is what a
// launch from a pane the shim opened (or from a shell under it) inherits.
// Wrapping again would change nothing.
func underTmuxShim(env []string) bool {
	socket := envValue(env, ranmaSocketEnv)
	server, _, _ := strings.Cut(envValue(env, "TMUX"), ",")
	return socket != "" && server == socket
}

// applyTmuxShim routes a launch through `ranma tmux-shim` when the profile asks
// for it and the launch happens in a ranma pane. Anywhere else the profile
// launches as it always did: the setting describes where teammates should go
// when there is somewhere to put them, not a requirement to be in ranma.
func applyTmuxShim(cmd *exec.Cmd, profile Profile) (*exec.Cmd, error) {
	if !profile.TmuxShim || !insideRanma(cmd.Env) {
		return cmd, nil
	}
	// The tmux indicator starts a real tmux server with TMUX cleared, so
	// whatever runs inside it reaches that tmux and never the shim.
	if profile.Indicator == tmuxIndicator {
		return nil, errors.New("this profile uses both the tmux indicator and ranma's tmux shim; they both claim tmux, so turn one off")
	}
	env := cmd.Env
	// Agent teams are the reason to be under the shim at all, and Claude Code
	// keeps them behind this flag. A value already set, including 0, is the
	// user's and stays.
	if profile.Provider == "claude" && envValue(env, agentTeamsEnv) == "" {
		env = append(withoutEnv(env, agentTeamsEnv), agentTeamsEnv+"=1")
	}
	if underTmuxShim(env) {
		cmd.Env = env
		return cmd, nil
	}
	ranma, err := exec.LookPath("ranma")
	if err != nil {
		return nil, errors.New("this profile launches under ranma's tmux shim, but ranma is not on PATH")
	}
	// The shim execs the command in its own place, so the PID the lock
	// records is still the CLI's by the time anything reads it.
	wrapped := exec.Command(ranma, append([]string{"tmux-shim", "--"}, cmd.Args...)...)
	wrapped.Dir = cmd.Dir
	wrapped.Env = env
	wrapped.Stdin, wrapped.Stdout, wrapped.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	return wrapped, nil
}

// setTmuxShim turns the shim on or off for a profile and saves the config.
func setTmuxShim(profile Profile, on bool, cfg *Config, configPath string, stdout io.Writer) error {
	if on && profile.Indicator == tmuxIndicator {
		return fmt.Errorf("%s launches inside the tmux indicator, which hides the shim; remove \"indicator\" from its entry in %s first", profile.Name, configPath)
	}
	for index := range cfg.Profiles {
		if cfg.Profiles[index].Name == profile.Name {
			cfg.Profiles[index].TmuxShim = on
			break
		}
	}
	if err := saveConfig(configPath, *cfg); err != nil {
		return err
	}
	if !on {
		fmt.Fprintf(stdout, "%s no longer launches under ranma's tmux shim\n", profile.Name)
		return nil
	}
	fmt.Fprintf(stdout, "%s launches under ranma's tmux shim when started in a ranma pane\n", profile.Name)
	if profile.Provider == "claude" {
		fmt.Fprintf(stdout, "agent teams are switched on there (%s=1), and each teammate opens in a ranma pane\n", agentTeamsEnv)
	}
	return nil
}

func envValue(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if key, rest, ok := strings.Cut(entry, "="); ok && key == name {
			// exec uses the last duplicate, so this does too.
			value = rest
		}
	}
	return value
}
