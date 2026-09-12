package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Cloning a profile copies its setup, not its identity. That is the whole
// decision this file makes, and it is the one that follows from what the rest
// of the launcher is for: two profiles exist to keep two accounts apart, so a
// clone that carried the credential file would hand the same refresh token to
// two directories and let the provider's own rotation invalidate whichever one
// was used second. The new profile is the old one's configuration — its MCP
// servers, its skills, its settings, its launch arguments — with no account
// attached, waiting on `ai login`.
//
// Moving an account, credentials and all, is a different operation and already
// has one: `ai profile export` and `ai profile import`. `--with-state` is here
// for the case where that round trip is not what you want, and it says what it
// is doing rather than being the quiet default.

// profileConfigPaths lists what "configuration" means for a provider, relative
// to the profile's state directory. It is an allowlist and not a set of
// exclusions on purpose: a provider's state directory also holds conversation
// history, caches, machine identifiers, and the credential file, and the cost
// of forgetting to exclude one of those is higher than the cost of a clone
// arriving without some setting nobody noticed was missing.
//
// Claude Code's .claude.json is absent here even though it holds the MCP
// servers, because the rest of that file is the account: its user id, its OAuth
// record, its per-project history. Only the mcpServers key crosses, and
// cloneProfileState copies it separately.
func profileConfigPaths(provider string) []string {
	switch provider {
	case "claude":
		return []string{
			"claude/settings.json",
			"claude/CLAUDE.md",
			"claude/AGENTS.md",
			"claude/skills",
			"claude/agents",
			"claude/commands",
			"claude/hooks",
			"claude/rules",
			"claude/output-styles",
			"claude/plugins/config.json",
		}
	case "codex":
		return []string{
			"codex/config.toml",
			"codex/AGENTS.md",
			"codex/skills",
			"codex/prompts",
			"codex/rules",
			"codex/hooks.json",
		}
	case "opencode":
		// OpenCode keeps config, data, and state apart already, and only the
		// first of the three is configuration: auth.json and opencode.db are
		// both under data.
		return []string{"config/opencode"}
	case "antigravity":
		return []string{
			"home/.gemini/config/config.json",
			"home/.gemini/config/mcp_config.json",
			"home/.gemini/antigravity-cli/settings.json",
		}
	default:
		return nil
	}
}

// cloneSkipped names what never crosses even with --with-state: the lock and
// instance bookkeeping that describes processes on this machine right now, and
// the generated trees each CLI rebuilds for itself. A clone of a running
// profile's instances directory would claim a PID it does not own.
func cloneSkipped(provider, relative string) bool {
	switch relative {
	case ".active.lock", instanceMetaFile, instancesDirectory:
		return true
	}
	if filepath.Base(relative) == "node_modules" {
		return true
	}
	switch provider {
	case "codex":
		// Codex recreates these helper symlinks per invocation.
		return relative == filepath.Join("codex", "tmp")
	case "antigravity":
		agy := filepath.Join("home", ".gemini", "antigravity-cli")
		for _, generated := range []string{"bin", "builtin", "cache", "crashes", "log", "updater"} {
			if relative == filepath.Join(agy, generated) {
				return true
			}
		}
		return relative == filepath.Join("home", ".cache")
	}
	return false
}

// cloneProfileState fills a new profile's state directory from an existing
// one's. It is the half of cloning that touches the disk; the caller owns the
// config entry, so a failure here leaves no half-registered profile behind.
func cloneProfileState(source, destination Profile, withState bool) error {
	root, err := profileRoot()
	if err != nil {
		return err
	}
	from := filepath.Join(root, source.Name)
	to := filepath.Join(root, destination.Name)
	if err := os.MkdirAll(to, 0700); err != nil {
		return err
	}
	if withState {
		// A registered profile whose state directory was removed by hand still
		// clones: there is nothing to carry, which is not the same as a failure.
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			return ensureClonedState(destination)
		} else if err != nil {
			return err
		}
		return copyTree(from, to, func(relative string) bool {
			return cloneSkipped(source.Provider, relative)
		})
	}
	for _, relative := range profileConfigPaths(source.Provider) {
		path := filepath.FromSlash(relative)
		if _, err := os.Lstat(filepath.Join(from, path)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if err := copyTree(filepath.Join(from, path), filepath.Join(to, path), func(relative string) bool {
			return filepath.Base(relative) == "node_modules"
		}); err != nil {
			return err
		}
	}
	// Claude Code is the provider whose MCP servers live inside a file the
	// clone deliberately does not copy, so they are carried over on their own.
	if source.Provider == "claude" {
		servers, err := readMCPServers(source)
		if err != nil {
			return err
		}
		if err := mergeMCPServers(destination, servers); err != nil {
			return err
		}
	}
	return ensureClonedState(destination)
}

// ensureClonedState creates the directories a launch expects, so a clone that
// copied nothing — a provider with no known configuration — is still a profile
// that runs.
func ensureClonedState(profile Profile) error {
	_, err := ensureProfileState(profile)
	return err
}

// copyTree copies one path onto another, recursing through directories. Walk
// uses Lstat, so a symlink is recreated rather than followed: inside a profile
// these point at the user's own dotfiles, and resolving one would replace a
// link that tracks its target with a copy that silently stops.
//
// Anything that is neither a regular file, a directory, nor a symlink — a
// socket a CLI left behind, say — is passed over. Refusing the whole clone over
// one of those would fail on exactly the profiles most worth cloning.
func copyTree(source, destination string, skip func(relative string) bool) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if skip != nil && relative != "." && skip(relative) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		case info.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		default:
			return nil
		}
	})
}

func copyFile(source, destination string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// cloneProfile is the command: a new profile that launches the way an existing
// one does, with its configuration already in place.
func cloneProfile(sourceName, name string, withState bool, cfg *Config, configFile string, stdout io.Writer) error {
	source, err := findProfile(*cfg, sourceName)
	if err != nil {
		return err
	}
	if !validName(name) {
		return fmt.Errorf("invalid profile name %q; use letters, numbers, dots, dashes, or underscores", name)
	}
	if err := checkNameAvailable(*cfg, name); err != nil {
		return err
	}
	// Reading a config file while its CLI is writing one gives a clone of a
	// half-written file, and export refuses for the same reason.
	if profileIsRunning(source) {
		return fmt.Errorf("profile %q is running; stop it before cloning", source.Name)
	}
	clone := source
	clone.Name = name
	if err := cloneProfileState(source, clone, withState); err != nil {
		return err
	}
	cfg.Profiles = append(cfg.Profiles, clone)
	if err := saveConfig(configFile, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "cloned %s into %s (%s)\n", source.Name, name, clone.Provider)
	if withState {
		fmt.Fprintf(stdout, "credentials came with it; do not run both accounts at once\n")
		return nil
	}
	// The one thing a clone cannot do is the thing its name most suggests it
	// did, so it is said rather than left to be discovered at the next launch.
	fmt.Fprintf(stdout, "%s copied; credentials were not — run 'ai login %s'\n", cloneSummary(source.Provider), name)
	return nil
}

func cloneSummary(provider string) string {
	if len(profileConfigPaths(provider)) == 0 {
		return "launch settings"
	}
	return "configuration"
}
