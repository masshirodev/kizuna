package main

import (
	"maps"
	"regexp"
	"sort"
	"strings"
)

// MCP servers carry credentials as references to environment variables, and
// the CLIs disagree about how such a reference is spelled: Claude Code and
// Antigravity expand `$VAR` and `${VAR}` (with `${VAR:-default}`), OpenCode
// interpolates `{env:VAR}`, and Codex interpolates nothing at all — it keeps
// static values apart from environment-sourced ones in `env_http_headers`,
// `bearer_token_env_var`, and `env_vars`.
//
// Copying a server verbatim across that disagreement produces a server that
// fails loudly on its first call (a literal `${TOKEN}` sent over the wire),
// so what crosses between two profiles is rewritten into the destination's
// spelling. What cannot be spelled at all is left literal and documented
// beside the Codex renderer, which is where the gap lives.

// shellRef matches the environment references Claude Code and Antigravity
// expand: $VAR, ${VAR}, and ${VAR:-default}. The name is a real identifier,
// so a POSIX expansion like ${_X%o} is never mistaken for one.
var shellRef = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)|\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-[^}]*)?\}`)

// openCodeRef matches the one interpolation OpenCode performs.
var openCodeRef = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// shellRefName reports the variable referenced when value is exactly one
// shell-style reference and nothing else.
func shellRefName(value string) (string, bool) {
	match := shellRef.FindString(value)
	if match == "" || match != value {
		return "", false
	}
	parts := shellRef.FindStringSubmatch(value)
	if parts[1] != "" {
		return parts[1], true
	}
	return parts[2], true
}

// openCodeRefName reports the variable referenced when value is exactly one
// `{env:VAR}` and nothing else.
func openCodeRefName(value string) (string, bool) {
	match := openCodeRef.FindString(value)
	if match == "" || match != value {
		return "", false
	}
	return openCodeRef.FindStringSubmatch(value)[1], true
}

// envRefName reports the variable referenced when value is exactly one
// reference in either spelling. The Codex renderer reads both: a server
// copied out of OpenCode still names its variables the OpenCode way, and
// either spelling means the same indirection.
func envRefName(value string) (string, bool) {
	if name, ok := shellRefName(value); ok {
		return name, true
	}
	return openCodeRefName(value)
}

// shellToOpenCode rewrites every shell-style reference into `{env:VAR}`. A
// `${VAR:-default}` loses its default — OpenCode has no spelling for one —
// and keeps the variable, which fails loudly when unset rather than silently
// carrying a stale fallback.
func shellToOpenCode(value string) string {
	return shellRef.ReplaceAllStringFunc(value, func(match string) string {
		parts := shellRef.FindStringSubmatch(match)
		name := parts[1]
		if name == "" {
			name = parts[2]
		}
		return "{env:" + name + "}"
	})
}

// openCodeToShell rewrites every `{env:VAR}` into `${VAR}`.
func openCodeToShell(value string) string {
	return openCodeRef.ReplaceAllStringFunc(value, func(match string) string {
		return "${" + openCodeRef.FindStringSubmatch(match)[1] + "}"
	})
}

// bearerRefName reports the variable when value is an Authorization header of
// exactly `Bearer <one reference>` in either spelling — the shape Codex
// stores as `bearer_token_env_var` rather than as a header at all.
func bearerRefName(value string) (string, bool) {
	token, found := strings.CutPrefix(value, "Bearer ")
	if !found {
		return "", false
	}
	return envRefName(token)
}

// translateMCPServer rewrites one server's environment references from the
// source provider's spelling into the destination's. Providers sharing a
// spelling — Claude Code and Antigravity, or a copy within one provider —
// cross untouched. Values bound for Codex keep the spelling they arrived in:
// it has no interpolation to rewrite into, and its own renderer reads both
// spellings when deciding which headers and variables are environment-sourced.
func translateMCPServer(server mcpServer, fromProvider, toProvider string) mcpServer {
	from, to := envSpellingFor(fromProvider), envSpellingFor(toProvider)
	if from == to || to == envNone {
		return server
	}
	rewrite := openCodeToShell
	if to == envBraces {
		rewrite = shellToOpenCode
	}
	server.URL = rewrite(server.URL)
	server.Command = rewrite(server.Command)
	for index, arg := range server.Args {
		server.Args[index] = rewrite(arg)
	}
	for key, value := range server.Headers {
		server.Headers[key] = rewrite(value)
	}
	for key, value := range server.Env {
		server.Env[key] = rewrite(value)
	}
	return server
}

// envSpelling names how a provider spells an environment reference in its
// MCP configuration.
type envSpelling int

const (
	envShell envSpelling = iota
	envBraces
	envNone
)

func envSpellingFor(provider string) envSpelling {
	switch provider {
	case "opencode":
		return envBraces
	case "codex":
		return envNone
	default:
		return envShell
	}
}

// mcpTranslationNotes says, one line per change, what a copy does to a server
// on its way from one provider's configuration to another's. It reads the same
// splits the writers use, so the box that shows these before the copy cannot
// describe a rewrite the copy then does not make. A copy that changes nothing
// has no notes.
func mcpTranslationNotes(server mcpServer, fromProvider, toProvider string) []string {
	if fromProvider == toProvider {
		return nil
	}
	var notes []string
	if toProvider == "codex" {
		if server.transport() == mcpStdio {
			_, forwarded := splitCodexEnv(server.Env)
			for _, name := range forwarded {
				notes = append(notes, name+" passed through → env_vars")
			}
			return notes
		}
		_, sourced, bearer := splitCodexHeaders(server.Headers)
		if bearer != "" {
			notes = append(notes, "Authorization: Bearer "+bearer+" → bearer_token_env_var")
		}
		names := make([]string, 0, len(sourced))
		for name := range sourced {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			notes = append(notes, name+" → env_http_headers")
		}
		return notes
	}
	// Every other destination keeps the shape and only respells references,
	// so the notes are the values that come out spelled differently.
	before := mcpServerValues(server)
	after := mcpServerValues(translateMCPServer(cloneMCPServer(server), fromProvider, toProvider))
	for index := range before {
		if before[index] != after[index] {
			notes = append(notes, before[index]+" → "+after[index])
		}
	}
	return notes
}

// mcpServerValues lists every value translateMCPServer may rewrite, in a fixed
// order, so a translated copy can be compared with the original field by field.
func mcpServerValues(server mcpServer) []string {
	values := []string{server.URL, server.Command}
	values = append(values, server.Args...)
	for _, table := range []map[string]string{server.Headers, server.Env} {
		keys := make([]string, 0, len(table))
		for key := range table {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			values = append(values, table[key])
		}
	}
	return values
}

// cloneMCPServer copies a server deeply enough for translateMCPServer, which
// rewrites its slice and maps in place.
func cloneMCPServer(server mcpServer) mcpServer {
	server.Args = append([]string(nil), server.Args...)
	server.Headers = maps.Clone(server.Headers)
	server.Env = maps.Clone(server.Env)
	return server
}
