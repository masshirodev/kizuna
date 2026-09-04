package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// appsDirName is the reserved subdirectory of profileRoot() holding one
// symlink per app, each pointing at its currently active member's profile
// directory. Reserved because a profile literally named "apps" would collide
// with it.
const appsDirName = "apps"

// findApp looks up an app by name, the App-side counterpart to findProfile.
func findApp(cfg Config, name string) (App, error) {
	for _, app := range cfg.Apps {
		if app.Name == name {
			return app, nil
		}
	}
	return App{}, fmt.Errorf("app %q not found", name)
}

// checkNameAvailable reports whether name is free to become a new profile or
// app: not already a profile, not already an app, and not the reserved word
// "apps". Profiles and apps share one namespace so a lookup by name is never
// ambiguous, and so ai-session never has to guess which one the user meant.
func checkNameAvailable(cfg Config, name string) error {
	if name == appsDirName {
		return fmt.Errorf("%q is reserved for application profiles", name)
	}
	if _, err := findProfile(cfg, name); err == nil {
		return fmt.Errorf("profile %q already exists", name)
	}
	if _, err := findApp(cfg, name); err == nil {
		return fmt.Errorf("app %q already exists", name)
	}
	return nil
}

// resolveProfile resolves either a profile name or an app name to the
// concrete Profile that should actually be launched. An app resolves to its
// currently active member, so every ai-session-mediated command (run, login,
// update, env, install, integrate) works on an app name exactly as it would
// on that member's own name — with zero changes to launch, profileEnv, or
// any indicator, since they only ever see a plain Profile.
func resolveProfile(cfg Config, name string) (Profile, error) {
	if profile, err := findProfile(cfg, name); err == nil {
		return profile, nil
	}
	app, err := findApp(cfg, name)
	if err != nil {
		return Profile{}, fmt.Errorf("profile or app %q not found", name)
	}
	return findProfile(cfg, app.Active)
}

// appsReferencing lists the apps that have profile as a member. The TUI's
// rename path uses this to refuse renaming a profile out from under an app:
// the app's Members/Active would go stale, and its symlink's relative target
// would point at a directory that no longer exists.
func appsReferencing(cfg Config, profile string) []string {
	var names []string
	for _, app := range cfg.Apps {
		for _, member := range app.Members {
			if member == profile {
				names = append(names, app.Name)
				break
			}
		}
	}
	return names
}

// appLink is the stable path an app's own static config can point at —
// ai-session repoints what it resolves to, but the path itself never moves.
func appLink(root, name string) string {
	return filepath.Join(root, appsDirName, name)
}

// pointApp (re)points an app's symlink at its currently active member's
// profile directory. The swap is atomic, mirroring saveConfig's tmp+rename
// pattern: a stale reader sees either the old target or the new one, never a
// missing or half-written link.
func pointApp(root string, app App) error {
	dir := filepath.Join(root, appsDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	link := appLink(root, app.Name)
	tmp := link + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	target := filepath.Join("..", app.Active)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func appCommand(args []string, cfg *Config, path string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "list" {
		return listApps(*cfg, stdout)
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return errors.New("usage: ai app add <name> <member> [member...]")
		}
		return addApp(args[1], args[2:], cfg, path, stdout)
	case "use":
		if len(args) != 3 {
			return errors.New("usage: ai app use <app> <member>")
		}
		return useApp(args[1], args[2], cfg, path, stdout)
	case "path":
		if len(args) != 2 {
			return errors.New("usage: ai app path <app>")
		}
		return appPath(*cfg, args[1], stdout)
	default:
		return fmt.Errorf("usage: ai app <add|use|list|path> ...; unknown subcommand %q", args[0])
	}
}

func addApp(name string, members []string, cfg *Config, path string, stdout io.Writer) error {
	if !validName(name) {
		return fmt.Errorf("invalid app name %q; use letters, numbers, dots, dashes, or underscores", name)
	}
	if err := checkNameAvailable(*cfg, name); err != nil {
		return err
	}
	for _, member := range members {
		if _, err := findProfile(*cfg, member); err != nil {
			return fmt.Errorf("member %q is not a profile: %w", member, err)
		}
	}
	app := App{Name: name, Members: append([]string(nil), members...), Active: members[0]}
	root, err := profileRoot()
	if err != nil {
		return err
	}
	if err := pointApp(root, app); err != nil {
		return err
	}
	cfg.Apps = append(cfg.Apps, app)
	if err := saveConfig(path, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created %s -> %s (%d member(s))\n", name, app.Active, len(app.Members))
	return nil
}

func useApp(name, member string, cfg *Config, path string, stdout io.Writer) error {
	app, err := findApp(*cfg, name)
	if err != nil {
		return err
	}
	found := false
	for _, candidate := range app.Members {
		if candidate == member {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%q is not a member of app %q; members are %v", member, name, app.Members)
	}
	app.Active = member
	root, err := profileRoot()
	if err != nil {
		return err
	}
	if err := pointApp(root, app); err != nil {
		return err
	}
	for index := range cfg.Apps {
		if cfg.Apps[index].Name == name {
			cfg.Apps[index] = app
			break
		}
	}
	if err := saveConfig(path, *cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s -> %s\n", name, member)
	return nil
}

func listApps(cfg Config, stdout io.Writer) error {
	apps := append([]App(nil), cfg.Apps...)
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	for _, app := range apps {
		provider := ""
		if active, err := findProfile(cfg, app.Active); err == nil {
			provider = active.Provider
		}
		fmt.Fprintf(stdout, "%s\t%s (%s)\t%v\n", app.Name, app.Active, provider, app.Members)
	}
	return nil
}

func appPath(cfg Config, name string, stdout io.Writer) error {
	if _, err := findApp(cfg, name); err != nil {
		return err
	}
	root, err := profileRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, appLink(root, name))
	return nil
}
