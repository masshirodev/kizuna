package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A launched CLI owns the whole screen, so the profile has to be named by
// something outside the conversation. Claude Code can render it itself through
// its statusLine setting; the providers with no equivalent are wrapped in a
// tmux session whose status bar sits above whatever the CLI draws.
const tmuxIndicator = "tmux"

// maxSocketPath is the portable ceiling on a Unix domain socket path; the
// sun_path field is 108 bytes on Linux, including the terminator.
const maxSocketPath = 100

// statusLineCommand reads the marker profileEnv puts in the launch environment,
// so the line names the profile that is actually paying for the session rather
// than one baked in when it was installed.
const statusLineCommand = `printf '[%s] %s' "${AI_PROFILE:-no profile}" "${PWD##*/}"`

func supportsNativeStatusLine(provider string) bool {
	return provider == "claude"
}

// installIndicator gives a profile the best indicator its provider supports and
// reports what it did. Providers with no native status line are marked for the
// tmux wrapper instead, which the next launch picks up.
func installIndicator(profile Profile, cfg *Config, configPath string, stdout io.Writer) error {
	if supportsNativeStatusLine(profile.Provider) {
		path, err := installStatusLine(profile)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s renders its own status line now (%s)\n", profile.Name, path)
		return nil
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("provider %q has no status line of its own, and the tmux fallback needs tmux on PATH", profile.Provider)
	}
	for index := range cfg.Profiles {
		if cfg.Profiles[index].Name == profile.Name {
			cfg.Profiles[index].Indicator = tmuxIndicator
			break
		}
	}
	if err := saveConfig(configPath, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s launches inside a tmux status bar now (%s has no status line of its own)\n", profile.Name, profile.Provider)
	return nil
}

// installStatusLine merges the setting into the profile's own settings.json,
// keeping every other key. An existing statusLine is left alone: it is the
// user's, and replacing it would silently drop whatever it showed.
func installStatusLine(profile Profile) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, profile.Name, "claude", "settings.json")
	settings := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	}
	if _, exists := settings["statusLine"]; exists {
		return "", fmt.Errorf("%s already sets statusLine; remove it first to avoid losing what it shows", path)
	}
	encoded, err := json.Marshal(map[string]string{"type": "command", "command": statusLineCommand})
	if err != nil {
		return "", err
	}
	settings["statusLine"] = encoded
	merged, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(merged, '\n'), 0600)
}

// applyIndicator rewrites a launch for the profile's indicator. A profile with
// none, or one whose provider renders its own, is launched untouched.
func applyIndicator(cmd *exec.Cmd, profile Profile, lockDir string) (*exec.Cmd, error) {
	if profile.Indicator != tmuxIndicator {
		return cmd, nil
	}
	return wrapWithTmux(cmd, profile, lockDir)
}

// tmuxSocketPath keeps the socket short and outside the instance directory. A
// Unix socket path is capped near 108 bytes, and the per-instance lock directory
// nested under the profile root is long enough to blow that budget on its own.
// The name is derived from the lock directory so every caller agrees on it
// without having to record it.
func tmuxSocketPath(lockDir string) string {
	digest := sha256.Sum256([]byte(lockDir))
	return filepath.Join(socketDir(), fmt.Sprintf("ai-%x.sock", digest[:6]))
}

func socketDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}

// wrapWithTmux re-points a launch at tmux so a status bar can name the profile
// above a CLI that owns the screen. The dedicated socket is not a detail: tmux
// runs a server, and an existing one would hand the child that server's
// environment instead of this profile's isolated directories.
func wrapWithTmux(cmd *exec.Cmd, profile Profile, lockDir string) (*exec.Cmd, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, errors.New("this profile's indicator is tmux, but tmux is not on PATH")
	}
	socket := tmuxSocketPath(lockDir)
	// The failure a too-long socket produces is "File name too long" from tmux
	// itself, which says nothing about which path is at fault.
	if len(socket) > maxSocketPath {
		return nil, fmt.Errorf("tmux socket path %s exceeds the %d byte limit; set XDG_RUNTIME_DIR to a shorter directory", socket, maxSocketPath)
	}
	confPath := filepath.Join(lockDir, "tmux.conf")
	if err := os.WriteFile(confPath, []byte(tmuxConfig(profile)), 0600); err != nil {
		return nil, err
	}
	args := []string{"-S", socket, "-f", confPath, "new-session"}
	if cmd.Dir != "" {
		args = append(args, "-c", cmd.Dir)
	}
	// tmux runs multiple arguments directly rather than through a shell, so the
	// CLI's own arguments survive spaces and quotes untouched.
	args = append(args, cmd.Args...)
	wrapped := exec.Command("tmux", args...)
	wrapped.Dir = cmd.Dir
	// tmux refuses to start inside another tmux unless TMUX is cleared, and the
	// dedicated socket makes nesting safe here.
	wrapped.Env = withoutEnv(cmd.Env, "TMUX")
	wrapped.Stdin, wrapped.Stdout, wrapped.Stderr = cmd.Stdin, cmd.Stdout, cmd.Stderr
	return wrapped, nil
}

func tmuxConfig(profile Profile) string {
	left := tmuxSafe(sessionTitle(profile))
	return strings.Join([]string{
		"set -g status on",
		"set -g status-position top",
		"set -g status-justify left",
		"set -g status-interval 0",
		`set -g status-style "bg=#7C3AED,fg=#FFFFFF,bold"`,
		`set -g status-left " ` + left + ` "`,
		"set -g status-left-length 80",
		`set -g status-right " #{pane_current_path} "`,
		"set -g status-right-length 60",
		`setw -g window-status-format ""`,
		`setw -g window-status-current-format ""`,
		// The bar is meant to be furniture, not a multiplexer the user has to
		// think about, so tmux's own key handling stays out of the way.
		"set -g prefix None",
		"set -g prefix2 None",
		"set -g escape-time 0",
		"set -g history-limit 50000",
		"set -g mouse on",
		"",
	}, "\n")
}

// tmuxSafe strips what would end a tmux config string early or be read as a
// format expansion. A provider is free text in profiles.json and reaches this
// file verbatim without it.
func tmuxSafe(value string) string {
	return strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f || char == '"' || char == '\\' || char == '#' {
			return -1
		}
		return char
	}, value)
}

func withoutEnv(values []string, name string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if key, _, _ := strings.Cut(value, "="); key != name {
			result = append(result, value)
		}
	}
	return result
}

// stopTmuxServer ends the session a wrapped launch created. Killing the client
// alone would leave the CLI running headless on its own socket.
func stopTmuxServer(lockDir string) {
	socket := tmuxSocketPath(lockDir)
	if _, err := os.Stat(socket); err != nil {
		return
	}
	_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
	_ = os.Remove(socket)
}
