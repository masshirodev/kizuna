package main

import (
	"slices"
	"strings"
)

// A launch line reads the way a shell reads one: leading NAME=value words are
// environment and everything from the first other word on is arguments, so
// `FOO=1 --model opus` sets FOO and passes two arguments. An assignment after
// an argument is an argument, and a quoted one ('FOO=1') is one too.
//
// A launch's environment comes from three places, later winning:
//
//  1. the profile's default env, which only fills a name nothing else set;
//  2. the inherited environment and the variables ai-session sets itself;
//  3. whatever was typed at the p prompt for this launch.
//
// A default fills only, because `FOO=1 ai max` was typed on purpose and the
// default was not; the launcher cannot tell that from an exported FOO, so an
// exported one wins as well, and the editor says when it does. The prompt's
// env wins over everything, ai-session's own variables included, because it
// was typed for this launch alone; overriding those is warned about, since it
// takes the session out of the profile's isolation.

// parseLaunchLine splits a typed line into its leading assignments and the
// arguments after them.
func parseLaunchLine(value string) (env, args []string, err error) {
	words, err := splitWords(value)
	if err != nil {
		return nil, nil, err
	}
	index := 0
	for index < len(words) && words[index].assignment {
		env = append(env, words[index].text)
		index++
	}
	for _, word := range words[index:] {
		args = append(args, word.text)
	}
	return env, args, nil
}

// formatLaunchLine is the inverse of parseLaunchLine. An argument that would
// read back as an assignment is quoted whole, so it stays an argument.
func formatLaunchLine(env, args []string) string {
	parts := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" {
			parts = append(parts, name+"=")
		} else {
			parts = append(parts, name+"="+formatArguments([]string{value}))
		}
	}
	if len(args) > 0 {
		first := formatArguments(args[:1])
		// Left bare, it holds no quote to escape.
		if name, _, ok := strings.Cut(args[0], "="); ok && validEnvName(name) && first == args[0] {
			first = "'" + first + "'"
		}
		parts = append(parts, first)
		if len(args) > 1 {
			parts = append(parts, formatArguments(args[1:]))
		}
	}
	return strings.Join(parts, " ")
}

// validEnvName is the shell's rule for a variable name.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		switch {
		case char == '_', char >= 'A' && char <= 'Z', char >= 'a' && char <= 'z':
		case char >= '0' && char <= '9' && index > 0:
		default:
			return false
		}
	}
	return true
}

// ownedEnvNames are the variables ai-session sets for a profile: the ones it
// strips from every inherited environment, and the ones this provider's
// isolation adds. A default cannot change them, and typing one warns.
func ownedEnvNames(profile Profile) []string {
	names := []string{
		"CODEX_HOME", "CLAUDE_CONFIG_DIR",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
		profileNameEnv, profileProviderEnv,
	}
	for _, entry := range profileEnv(profile) {
		if name, _, _ := strings.Cut(entry, "="); !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// ownedNamesIn names the variables among entries that ai-session sets itself.
// Typed at the prompt they override its own and are warned about; as defaults
// they could never apply, and the editor refuses them.
func ownedNamesIn(profile Profile, entries []string) []string {
	owned := ownedEnvNames(profile)
	var names []string
	for _, entry := range entries {
		if name, _, _ := strings.Cut(entry, "="); slices.Contains(owned, name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// withLaunchEnv layers a launch's own variables over the environment
// launchEnvironment built. plain drops the defaults, as it drops the default
// arguments.
func withLaunchEnv(env []string, profile Profile, plain bool, typed []string) []string {
	result := slices.Clone(env)
	if !plain {
		for _, entry := range profile.DefaultEnv {
			if name, _, ok := strings.Cut(entry, "="); ok && !hasEnv(result, name) {
				result = append(result, entry)
			}
		}
	}
	for _, entry := range typed {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		result = append(withoutEnv(result, name), entry)
	}
	return result
}

// appliedDefaults are the default variables a launch from this environment
// would actually set: those nothing else sets first.
func appliedDefaults(profile Profile, environ []string) []string {
	var applied []string
	for _, entry := range profile.DefaultEnv {
		if name, _, ok := strings.Cut(entry, "="); ok && !hasEnv(environ, name) && !slices.Contains(ownedEnvNames(profile), name) {
			applied = append(applied, entry)
		}
	}
	return applied
}

// shadowedDefaults names the default variables the given environment already
// sets, so the default will not apply.
func shadowedDefaults(profile Profile, environ []string) []string {
	var names []string
	for _, entry := range profile.DefaultEnv {
		if name, _, ok := strings.Cut(entry, "="); ok && hasEnv(environ, name) && !slices.Contains(ownedEnvNames(profile), name) {
			names = append(names, name)
		}
	}
	return names
}

// hasEnv reports whether a name is set at all. Set to empty counts: `FOO= ai
// max` is as deliberate as `FOO=1 ai max`.
func hasEnv(env []string, name string) bool {
	return slices.ContainsFunc(env, func(entry string) bool {
		key, _, ok := strings.Cut(entry, "=")
		return ok && key == name
	})
}

// previewEnv is what a launch from environ would set beyond ai-session's own
// variables, in the order it is written before the command: the defaults that
// apply, then what was typed. A typed name replaces its default.
func previewEnv(profile Profile, environ, typed []string) []string {
	var env []string
	for _, entry := range appliedDefaults(profile, environ) {
		if name, _, _ := strings.Cut(entry, "="); !hasEnv(typed, name) {
			env = append(env, entry)
		}
	}
	return append(env, typed...)
}
