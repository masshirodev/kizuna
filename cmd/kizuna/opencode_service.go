package main

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// OpenCode 2.x no longer runs its server inside the process that needs it.
// The TUI, `run`, `session export` and the rest look for a background service
// through a discovery file in XDG_STATE_HOME/opencode/service.json and, when
// none is found, start `opencode serve --service`: a detached daemon that is
// reparented to init and outlives the launch. Its port is pinned on first
// start in XDG_CONFIG_HOME/opencode/service.json.
//
// Both halves collide with how the launcher isolates a profile. Every
// concurrent instance gets a fresh XDG_STATE_HOME (so it never finds the
// discovery file) but shares the profile's XDG_CONFIG_HOME (so it always reads
// the same pinned port). Each launch therefore starts another daemon on a port
// a previous one still holds, and fails with "Managed service port … is
// already in use". A daemon that does start keeps the instance's or the
// archive's opencode.db open after the launch is over, under the merge-back.
//
// `--standalone` restores the 1.x shape: a private server inside the process,
// gone when it exits. OpenCode has no environment variable or config key for
// it, so the launcher adds the flag to every invocation that would reach the
// service. 1.x rejects the flag, which is why the binary's version decides.

// openCodeStandaloneFlag asks OpenCode 2.x for a private, in-process server.
const openCodeStandaloneFlag = "--standalone"

// openCodeServerFlag points OpenCode 2.x at a server URL. It cannot be
// combined with --standalone; someone passing it chose their server.
const openCodeServerFlag = "--server"

// openCodeStandaloneCommands names the OpenCode 2.x subcommands that reach the
// background service, built from each one's `--help` (opencode 2.0.32): the
// value lists the second word that does, or is nil when the subcommand itself
// does. The bare TUI is not a subcommand and is handled apart. `api`,
// `reload` and `pair` also take the flag but exist to talk to a running
// service, so they are left to the person who typed them.
var openCodeStandaloneCommands = map[string][]string{
	"run":     nil,
	"mini":    nil,
	"models":  nil,
	"stats":   nil,
	"session": {"list", "delete", "export", "import"},
	"auth":    {"login"},
}

// openCodeVersionTimeout bounds the version probe. `opencode --version`
// answers in about a tenth of a second; this only stops a wedged binary from
// holding up a launch.
const openCodeVersionTimeout = 5 * time.Second

var openCodeVersionPattern = regexp.MustCompile(`(\d+)\.\d+`)

var openCodeServiceCache = struct {
	sync.Mutex
	usesService map[string]bool
}{usesService: map[string]bool{}}

// openCodeServiceProbe reports whether an opencode binary is a 2.x one that
// defaults to the background service. A package variable so tests, whose
// stand-in binaries log every call, can answer without running them.
var openCodeServiceProbe = probeOpenCodeService

// openCodeUsesService answers openCodeServiceProbe once per resolved binary
// for the life of the process: the TUI launches repeatedly, and an upgrade
// that swaps the binary underneath it is picked up by the next kizuna.
func openCodeUsesService(command string) bool {
	key := command
	if path, err := exec.LookPath(command); err == nil {
		key = path
	}
	openCodeServiceCache.Lock()
	defer openCodeServiceCache.Unlock()
	if answer, ok := openCodeServiceCache.usesService[key]; ok {
		return answer
	}
	answer := openCodeServiceProbe(command)
	openCodeServiceCache.usesService[key] = answer
	return answer
}

// probeOpenCodeService runs `opencode --version` ("opencode v2.0.32", "1.18.29"
// before 2.x). A binary that does not answer is treated as 1.x: adding the
// flag to one that rejects it would break every launch, while leaving it off a
// 2.x one only brings back the daemon.
func probeOpenCodeService(command string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), openCodeVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, "--version")
	cmd.Env = cleanEnvironment(os.Environ())
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	match := openCodeVersionPattern.FindSubmatch(output)
	if match == nil {
		return false
	}
	major, err := strconv.Atoi(string(match[1]))
	return err == nil && major >= 2
}

// isOpenCodeBinary reports whether a profile runs the opencode binary.
// DeepSeek does too, and its daemon would hold the machine's ordinary store.
func isOpenCodeBinary(profile Profile) bool {
	return profile.Provider == "opencode" || profile.Provider == "deepseek"
}

// withOpenCodeStandalone returns args with --standalone added where an
// OpenCode 2.x invocation would otherwise reach the background service, and
// args unchanged for every other provider, binary, or subcommand. command is
// what is about to run: a profile's launcher also runs other tools under its
// environment (openusage), which must never be handed the flag.
func withOpenCodeStandalone(profile Profile, command string, args []string) []string {
	if command != profile.Command || !isOpenCodeBinary(profile) || !needsOpenCodeStandalone(args) || !openCodeUsesService(command) {
		return args
	}
	return insertOpenCodeStandalone(args)
}

// needsOpenCodeStandalone reports whether args select a service-backed
// invocation that has not already chosen its server. It is decided before the
// version probe so a `kizuna <profile> upgrade` never pays for one.
func needsOpenCodeStandalone(args []string) bool {
	for _, arg := range args {
		if arg == openCodeStandaloneFlag || arg == openCodeServerFlag ||
			hasFlagValue(arg, openCodeStandaloneFlag) || hasFlagValue(arg, openCodeServerFlag) {
			return false
		}
	}
	return openCodeStandalonePosition(args) >= 0
}

// insertOpenCodeStandalone places the flag where OpenCode's parser reads it:
// after the subcommand words that own it, or first for the bare TUI.
func insertOpenCodeStandalone(args []string) []string {
	position := openCodeStandalonePosition(args)
	if position < 0 {
		return args
	}
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:position]...)
	result = append(result, openCodeStandaloneFlag)
	return append(result, args[position:]...)
}

// openCodeStandalonePosition returns the index the flag goes in front of, or
// -1 when args name a subcommand that never reaches the service. A first word
// that is no known subcommand is the TUI's directory, or the value of one of
// its flags (`-s ses_…`), and the TUI takes the flag first. The shallow scan
// is subcommandIndex's, with its known blind spot: `--prompt run` reads as the
// run subcommand, and the flag then lands after "run", which OpenCode still
// accepts as the TUI's.
func openCodeStandalonePosition(args []string) int {
	index := subcommandIndex(args)
	if index < 0 {
		return 0
	}
	word := args[index]
	verbs, ok := openCodeStandaloneCommands[word]
	if !ok {
		if _, known := openCodeSubcommands[word]; known {
			return -1
		}
		return 0
	}
	if verbs == nil {
		return index + 1
	}
	rest := args[index+1:]
	next := subcommandIndex(rest)
	if next < 0 {
		return -1
	}
	for _, verb := range verbs {
		if rest[next] == verb {
			return index + 1 + next + 1
		}
	}
	return -1
}

func hasFlagValue(arg, flag string) bool {
	return len(arg) > len(flag) && arg[:len(flag)] == flag && arg[len(flag)] == '='
}

// openCodeSessionToolArgs spells a session export or import for the binary at
// hand: 2.x moved both under `session`, gave import a --directory, and needs
// --standalone so neither starts a daemon on the archive or the instance.
// 1.x keeps the top-level words and takes the directory from the working
// directory, which runOpenCodeTool sets either way.
func openCodeSessionToolArgs(profile Profile, verb, target, directory string) []string {
	if !openCodeUsesService(profile.Command) {
		return []string{verb, target}
	}
	args := []string{"session", verb, openCodeStandaloneFlag}
	if verb == "import" && directory != "" {
		args = append(args, "--directory", directory)
	}
	return append(args, target)
}
