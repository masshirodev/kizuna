package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A provider's updater updates the install it belongs to, which is not always
// the one the profile's command resolves to. A machine with a package-manager
// copy in /usr/bin and the vendor's native install in ~/.local/bin, the latter
// later on PATH, has `claude update` refresh the native one while every launch
// keeps running the package's: the update reports success and nothing changes.
// ai-session cannot tell which install is right, but it can see the files on
// either side of an update and say when the one that moved is not the one that
// runs.

// binaryInstall is one place a command is installed, as it was on disk at one
// moment. target is where the path resolves after symlinks, which is what a
// native installer moves on update; modTime and size catch one that rewrites
// the file in place instead.
type binaryInstall struct {
	path    string
	target  string
	modTime time.Time
	size    int64
}

// knownInstallPaths are where a provider's own installer puts its CLI, for the
// case where that directory is not on PATH at all and the install would
// otherwise be invisible. Only the installers ai-session runs itself are listed.
func knownInstallPaths(provider string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch provider {
	case "claude":
		return []string{
			filepath.Join(home, ".local", "bin", "claude"),
			filepath.Join(home, ".claude", "local", "claude"),
		}
	case "opencode", "deepseek":
		return []string{filepath.Join(home, ".opencode", "bin", "opencode")}
	}
	return nil
}

// findInstalls lists every distinct install of command: PATH matches first, in
// PATH order, so the first entry is the one a launch runs; then the provider's
// known locations. Two paths resolving to the same file are one install, which
// matters on systems where /bin is a symlink to /usr/bin.
func findInstalls(command, pathEnv string, known []string) []binaryInstall {
	var paths []string
	if strings.ContainsRune(command, filepath.Separator) {
		paths = append(paths, command)
	} else {
		for _, dir := range filepath.SplitList(pathEnv) {
			if dir == "" {
				dir = "."
			}
			paths = append(paths, filepath.Join(dir, command))
		}
	}
	paths = append(paths, known...)

	var installs []binaryInstall
	seen := map[string]bool{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		installs = append(installs, binaryInstall{path: path, target: target, modTime: info.ModTime(), size: info.Size()})
	}
	return installs
}

// profileInstalls is findInstalls for a profile, against this process's PATH,
// which is the PATH both the update and a launch resolve the command with.
func profileInstalls(profile Profile) []binaryInstall {
	return findInstalls(profile.Command, os.Getenv("PATH"), knownInstallPaths(profile.Provider))
}

// shadowWarning compares the installs before and after an update. It is empty
// when the update can only have reached the install that runs: that one
// changed, or it is the only one there is.
func shadowWarning(command string, before, after []binaryInstall) string {
	if len(after) == 0 {
		return ""
	}
	running := after[0]
	var changed []binaryInstall
	for _, install := range after {
		if !sameInstall(install, before) {
			changed = append(changed, install)
		}
	}
	for _, install := range changed {
		if install.path == running.path {
			return ""
		}
	}
	name := filepath.Base(command)
	if len(changed) > 0 {
		return "the update changed " + shortenHome(changed[0].path) + ", but " + name + " runs " +
			shortenHome(running.path) + ", which did not change — remove that install, or put " +
			shortenHome(filepath.Dir(changed[0].path)) + " ahead of it on PATH"
	}
	if len(after) > 1 {
		return name + " runs " + shortenHome(running.path) + ", and another install at " +
			shortenHome(after[1].path) + " is shadowed by it; the update may only ever reach that one"
	}
	return ""
}

func sameInstall(install binaryInstall, set []binaryInstall) bool {
	for _, other := range set {
		if other.path == install.path {
			return other.target == install.target && other.modTime.Equal(install.modTime) && other.size == install.size
		}
	}
	return false
}
