package main

// A provider's default arguments belong to a launch: they configure the
// session the CLI is about to start. Not every subcommand starts a session —
// `opencode models` lists models and takes no `--auto`, and `claude mcp list`
// takes no session flags either — so placing the defaults first, as the
// launcher used to, made the CLI parse them as its own and either reject them
// or print its top-level help.
//
// The tables below name each CLI's subcommands and say whether that subcommand
// takes the profile's launch defaults. A word absent from a table is not a
// subcommand this launcher recognises — a prompt, a path — and the defaults go
// in front of it as they always did.

// subcommandKind records whether a subcommand accepts the profile's default
// launch arguments.
type subcommandKind int

const (
	// subcommandTakesDefaults marks a subcommand that starts a session and
	// accepts the same flags the bare CLI does: `opencode run --auto …`.
	subcommandTakesDefaults subcommandKind = iota
	// subcommandRejectsDefaults marks a subcommand that manages configuration,
	// credentials, or models and has no use for the launch defaults:
	// `opencode models` takes no `--auto`.
	subcommandRejectsDefaults
)

// openCodeSubcommands is built from `opencode --help`, `opencode run --help`,
// and `opencode models --help` (opencode 1.x). Only `run` starts a session the
// launch defaults apply to; the bare TUI is not a subcommand and keeps its
// defaults in front. DeepSeek runs through the same binary.
var openCodeSubcommands = map[string]subcommandKind{
	"run":        subcommandTakesDefaults,
	"completion": subcommandRejectsDefaults,
	"acp":        subcommandRejectsDefaults,
	"mcp":        subcommandRejectsDefaults,
	"attach":     subcommandRejectsDefaults,
	"debug":      subcommandRejectsDefaults,
	"providers":  subcommandRejectsDefaults,
	"auth":       subcommandRejectsDefaults,
	"agent":      subcommandRejectsDefaults,
	"upgrade":    subcommandRejectsDefaults,
	"uninstall":  subcommandRejectsDefaults,
	"serve":      subcommandRejectsDefaults,
	"web":        subcommandRejectsDefaults,
	"models":     subcommandRejectsDefaults,
	"stats":      subcommandRejectsDefaults,
	"export":     subcommandRejectsDefaults,
	"import":     subcommandRejectsDefaults,
	"github":     subcommandRejectsDefaults,
	"pr":         subcommandRejectsDefaults,
	"session":    subcommandRejectsDefaults,
	"plugin":     subcommandRejectsDefaults,
	"plug":       subcommandRejectsDefaults,
	"db":         subcommandRejectsDefaults,
}

// claudeSubcommands is built from `claude --help` and `claude mcp --help`.
// Claude's session flags live on the bare invocation (and its prompt form), so
// every named subcommand here manages state or the install rather than starting
// a configured session. `claude config` is deliberately absent: no such
// subcommand exists — `claude --help` lists none — so treating "config" as one
// would drop the defaults from a prompt that happened to be that word.
var claudeSubcommands = map[string]subcommandKind{
	"agents":      subcommandRejectsDefaults,
	"attach":      subcommandRejectsDefaults,
	"auth":        subcommandRejectsDefaults,
	"auto-mode":   subcommandRejectsDefaults,
	"doctor":      subcommandRejectsDefaults,
	"gateway":     subcommandRejectsDefaults,
	"import":      subcommandRejectsDefaults,
	"install":     subcommandRejectsDefaults,
	"logs":        subcommandRejectsDefaults,
	"mcp":         subcommandRejectsDefaults,
	"plugin":      subcommandRejectsDefaults,
	"plugins":     subcommandRejectsDefaults,
	"project":     subcommandRejectsDefaults,
	"respawn":     subcommandRejectsDefaults,
	"rm":          subcommandRejectsDefaults,
	"setup-token": subcommandRejectsDefaults,
	"stop":        subcommandRejectsDefaults,
	"kill":        subcommandRejectsDefaults,
	"ultrareview": subcommandRejectsDefaults,
	"update":      subcommandRejectsDefaults,
	"upgrade":     subcommandRejectsDefaults,
}

// codexSubcommands is built from `codex --help` and the help of each
// subcommand that could plausibly start a session. `exec`, `resume`, and `fork`
// each list `--model`, `--sandbox`, and the approval flags, so a Codex profile's
// defaults go after them. Everything else is management or tooling that does not
// take those flags.
var codexSubcommands = map[string]subcommandKind{
	"exec":           subcommandTakesDefaults,
	"resume":         subcommandTakesDefaults,
	"fork":           subcommandTakesDefaults,
	"review":         subcommandRejectsDefaults,
	"login":          subcommandRejectsDefaults,
	"logout":         subcommandRejectsDefaults,
	"mcp":            subcommandRejectsDefaults,
	"plugin":         subcommandRejectsDefaults,
	"mcp-server":     subcommandRejectsDefaults,
	"app-server":     subcommandRejectsDefaults,
	"remote-control": subcommandRejectsDefaults,
	"completion":     subcommandRejectsDefaults,
	"update":         subcommandRejectsDefaults,
	"doctor":         subcommandRejectsDefaults,
	"sandbox":        subcommandRejectsDefaults,
	"debug":          subcommandRejectsDefaults,
	"apply":          subcommandRejectsDefaults,
	"archive":        subcommandRejectsDefaults,
	"delete":         subcommandRejectsDefaults,
	"unarchive":      subcommandRejectsDefaults,
	"cloud":          subcommandRejectsDefaults,
	"exec-server":    subcommandRejectsDefaults,
	"features":       subcommandRejectsDefaults,
	"help":           subcommandRejectsDefaults,
}

// antigravitySubcommands is built from `agy --help`: none of its subcommands
// starts a session, so none takes the defaults. The bare `agy` TUI is not a
// subcommand and keeps them.
var antigravitySubcommands = map[string]subcommandKind{
	"agent":          subcommandRejectsDefaults,
	"agents":         subcommandRejectsDefaults,
	"changelog":      subcommandRejectsDefaults,
	"help":           subcommandRejectsDefaults,
	"install":        subcommandRejectsDefaults,
	"mcp":            subcommandRejectsDefaults,
	"mic-serve":      subcommandRejectsDefaults,
	"models":         subcommandRejectsDefaults,
	"plugin":         subcommandRejectsDefaults,
	"plugins":        subcommandRejectsDefaults,
	"remote-control": subcommandRejectsDefaults,
	"update":         subcommandRejectsDefaults,
}

// providerSubcommands is the lookup the launcher consults. A provider with no
// entry, or a word absent from its entry, is not recognised as a subcommand.
var providerSubcommands = map[string]map[string]subcommandKind{
	"opencode":    openCodeSubcommands,
	"deepseek":    openCodeSubcommands,
	"claude":      claudeSubcommands,
	"codex":       codexSubcommands,
	"antigravity": antigravitySubcommands,
}

// subcommandIndex reports where a provider CLI's subcommand would appear: the
// index of the first argument that is not a flag. Arguments beginning with a
// dash are skipped; it returns -1 when every argument is a flag. The scan is
// deliberately this shallow: it does not know which flags take a value, so the
// `x` in `--model x` is the first non-flag word and is treated as an unknown
// word, which keeps the defaults in front exactly as before.
func subcommandIndex(args []string) int {
	for index, arg := range args {
		if len(arg) > 0 && arg[0] == '-' {
			continue
		}
		return index
	}
	return -1
}

// subcommandDisposition reports whether the word at the first non-flag position
// names a subcommand of the provider's CLI and, if it does, whether that
// subcommand takes the profile's launch defaults.
func subcommandDisposition(provider, word string) (known, takesDefaults bool) {
	table, ok := providerSubcommands[provider]
	if !ok {
		return false, false
	}
	kind, ok := table[word]
	if !ok {
		return false, false
	}
	return true, kind == subcommandTakesDefaults
}
