package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// An integration is something ai writes into or around a profile so the
// provider's CLI does more than it would alone: a status line naming the
// profile, OpenUsage's hooks, ranma's tmux shim. The TUI's integrations box
// and `ai integrate list` both show what each profile has, and both read it
// from integrationsFor, which decides with the same checks `ai integrate` and
// the launch make. A box that could say "on" while the launch disagrees would
// be worse than no box.

type integrationKind int

const (
	integrationStatusLine integrationKind = iota
	integrationOpenUsage
	integrationRanma
)

// integrationKinds is the order every view lists them in.
var integrationKinds = []integrationKind{integrationStatusLine, integrationOpenUsage, integrationRanma}

func (k integrationKind) name() string {
	switch k {
	case integrationStatusLine:
		return "status line"
	case integrationOpenUsage:
		return "openusage"
	default:
		return "ranma tmux shim"
	}
}

// word is what `ai integrate` calls it.
func (k integrationKind) word() string {
	switch k {
	case integrationStatusLine:
		return "statusline"
	case integrationOpenUsage:
		return "openusage"
	default:
		return "ranma"
	}
}

type integrationState int

const (
	// stateOff: not set, and nothing stops it being set.
	stateOff integrationState = iota
	// stateOn: ai wrote it and reads it back.
	stateOn
	// stateAnother: a status line is there that ai did not write. ai leaves
	// it alone and will not install over it.
	stateAnother
	// stateNotRead: OpenUsage's hooks are OpenUsage's; ai never looks at them,
	// so it cannot say whether they are installed.
	stateNotRead
	// stateLocked: refused while the profile runs.
	stateLocked
	// stateNA: not for this provider.
	stateNA
	// stateRefused: conflicts with another integration on the profile.
	stateRefused
	// stateNeeds: a tool it depends on is not on PATH.
	stateNeeds
)

// integrationCheck is one of the conditions that decide a state, shown so the
// box explains itself rather than only reporting.
type integrationCheck struct {
	ok   bool
	text string
	note string
}

// integrationStatus is one integration on one profile: its state and
// everything the box's detail pane and the CLI's listing say about it.
type integrationStatus struct {
	kind  integrationKind
	state integrationState
	// note qualifies the state in a word or two: which kind of status line,
	// why it is locked, which provider it is not for.
	note string
	// inert is the shim set on a profile while the TUI is not in a ranma
	// pane: on, but a launch from here runs without it.
	inert bool
	// needs names the missing tool for stateNeeds.
	needs string
	// reason says why a row cannot be acted on, in a sentence, and hint what
	// would get it moving.
	reason, hint string

	// runs is the external command acting on the row runs, for the rows that
	// are an installer rather than a config write.
	runs               string
	writes, writesNote string
	launch             string
	gives              string
	undo, undoNote     string
	// here is whether a launch from where the TUI runs gets it, and hereNote
	// the sentence under it.
	here, hereNote string
	hereApplies    bool
	checks         []integrationCheck
	sameAs         string
	// action is what ↵ does on the row ("turn on", "turn off", "install"),
	// or empty when it does nothing.
	action string
}

// integrationEnv is everything integrationsFor learns from outside the
// profile. It is passed in so the states can be tested without a machine
// that has tmux, ranma, and OpenUsage installed.
type integrationEnv struct {
	onPath     func(tool string) bool
	inRanma    bool
	running    func(Profile) bool
	statusLine func(Profile) statusLineOwner
}

// machineIntegrationEnv is where the TUI and the CLI read the machine from.
// Tests swap it for one that does not depend on what this box has on PATH.
var machineIntegrationEnv = liveIntegrationEnv

// liveIntegrationEnv is the environment the TUI and the CLI actually run in.
func liveIntegrationEnv() integrationEnv {
	return integrationEnv{
		onPath: func(tool string) bool {
			_, err := exec.LookPath(tool)
			return err == nil
		},
		inRanma:    insideRanma(os.Environ()),
		running:    profileIsRunning,
		statusLine: readStatusLineOwner,
	}
}

type statusLineOwner int

const (
	statusLineNone statusLineOwner = iota
	statusLineOurs
	statusLineTheirs
)

// readStatusLineOwner looks at the statusLine a native provider reads. It is
// ai's when it is the command installStatusLine writes; anything else there is
// the user's. A file that cannot be read is treated as holding nothing, which
// is also what installStatusLine will find when it tries.
func readStatusLineOwner(profile Profile) statusLineOwner {
	path, err := statusLineSettingsPath(profile)
	if err != nil {
		return statusLineNone
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return statusLineNone
	}
	var settings struct {
		StatusLine json.RawMessage `json:"statusLine"`
	}
	if json.Unmarshal(data, &settings) != nil || len(settings.StatusLine) == 0 || string(settings.StatusLine) == "null" {
		return statusLineNone
	}
	var line struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(settings.StatusLine, &line) == nil && line.Command == statusLineCommand {
		return statusLineOurs
	}
	return statusLineTheirs
}

// supportsOpenUsage is whether OpenUsage has an integration for the provider.
// It has none for Antigravity, and handing it the provider's name anyway only
// gets a failure from OpenUsage that names nothing ai can act on.
func supportsOpenUsage(provider string) bool {
	switch openUsageIntegration(provider) {
	case "claude_code", "codex", "opencode":
		return true
	}
	return false
}

func integrationsFor(profile Profile, env integrationEnv) []integrationStatus {
	statuses := make([]integrationStatus, 0, len(integrationKinds))
	for _, kind := range integrationKinds {
		statuses = append(statuses, integrationFor(profile, kind, env))
	}
	return statuses
}

func integrationFor(profile Profile, kind integrationKind, env integrationEnv) integrationStatus {
	switch kind {
	case integrationStatusLine:
		if supportsNativeStatusLine(profile.Provider) {
			return nativeStatusLine(profile, env)
		}
		return tmuxStatusLine(profile, env)
	case integrationOpenUsage:
		return openUsageStatus(profile, env)
	default:
		return ranmaStatus(profile, env)
	}
}

func nativeStatusLine(profile Profile, env integrationEnv) integrationStatus {
	// Relative to the profile's directory: the full path does not fit the
	// pane, and the part that says which CLI reads it is the part that matters.
	settings := "settings.json"
	if path, err := statusLineSettingsPath(profile); err == nil {
		if root, err := profileRoot(); err == nil {
			if rel, err := filepath.Rel(filepath.Join(root, profile.Name), path); err == nil {
				settings = rel
			}
		}
	}
	status := integrationStatus{
		kind:        integrationStatusLine,
		note:        "own line",
		writes:      `"statusLine"`,
		writesNote:  "in the profile's " + settings,
		launch:      "nothing: " + profile.Provider + " reads it itself",
		gives:       "the CLI names [" + profile.Name + "] on its own status line",
		undo:        "none yet",
		undoNote:    "remove statusLine from settings.json by hand",
		here:        "applies to every launch",
		hereNote:    "the setting lives in the profile, wherever it is launched from",
		hereApplies: true,
		sameAs:      "ai integrate statusline " + profile.Name,
	}
	switch env.statusLine(profile) {
	case statusLineOurs:
		status.state = stateOn
		status.checks = []integrationCheck{{ok: true, text: "settings.json carries ai's statusLine"}}
	case statusLineTheirs:
		status.state = stateAnother
		status.note = "not ai's"
		status.reason = "a statusLine that is not ai's is already there; ai leaves it alone"
		status.hint = "remove it from settings.json by hand and ai can install its own"
		status.checks = []integrationCheck{{text: "settings.json already sets a statusLine", note: "yours, so it is not replaced"}}
	default:
		status.state = stateOff
		status.action = "turn on"
		status.checks = []integrationCheck{{ok: true, text: "no other statusLine in settings.json"}}
	}
	return status
}

func tmuxStatusLine(profile Profile, env integrationEnv) integrationStatus {
	hasTmux := env.onPath("tmux")
	status := integrationStatus{
		kind:        integrationStatusLine,
		note:        "tmux bar",
		writes:      `"indicator": "tmux"`,
		writesNote:  "on " + profile.Name + " in profiles.json",
		launch:      "runs the CLI inside a private tmux session",
		gives:       "a status bar naming the profile above the CLI",
		undo:        "none yet",
		undoNote:    `remove "indicator" from profiles.json by hand`,
		here:        "applies to every launch",
		hereNote:    "the setting lives in the profile, wherever it is launched from",
		hereApplies: true,
		sameAs:      "ai integrate statusline " + profile.Name,
		checks: []integrationCheck{
			{ok: hasTmux, text: "tmux on PATH"},
			{ok: !profile.TmuxShim, text: "no ranma tmux shim on this profile", note: "the two are refused together"},
		},
	}
	switch {
	case profile.Indicator == tmuxIndicator && profile.TmuxShim:
		status.state = stateRefused
		status.reason = "both the tmux bar and ranma's tmux shim are set, and a launch refuses the pair"
		status.hint = "turn the shim off here, or with ai integrate ranma " + profile.Name + " --off"
	case profile.Indicator == tmuxIndicator:
		status.state = stateOn
	case !hasTmux:
		status.state, status.needs = stateNeeds, "tmux"
		status.reason = profile.Provider + " has no status line of its own, and the tmux bar needs tmux on PATH"
		status.hint = "install tmux, or put it on PATH, and this row can be turned on"
	default:
		// installIndicator does not refuse the pair, but every launch after
		// it would; the box refuses up front instead of offering a trap.
		if profile.TmuxShim {
			status.state = stateRefused
			status.reason = profile.Name + " launches under ranma's tmux shim, and the tmux bar is refused beside it"
			status.hint = "turn the shim off first, then the tmux bar can be turned on"
			return status
		}
		status.state = stateOff
		status.action = "turn on"
	}
	return status
}

func openUsageStatus(profile Profile, env integrationEnv) integrationStatus {
	integration := openUsageIntegration(profile.Provider)
	status := integrationStatus{
		kind:       integrationOpenUsage,
		writes:     "OpenUsage's hooks",
		writesNote: "beside the profile's own state",
		launch:     "nothing: the hooks run inside the CLI",
		gives:      "OpenUsage reads this profile's usage",
		undo:       "none from ai",
		undoNote:   "OpenUsage's own uninstall, in the profile's environment",
		here:       "applies to every launch",
		hereNote:   "ai never reads the hooks back, so this row keeps saying not read",
		sameAs:     "ai integrate openusage " + profile.Name,
	}
	if !supportsOpenUsage(profile.Provider) {
		status.state, status.note = stateNA, profile.Provider
		status.reason = "OpenUsage has no " + profile.Provider + " integration"
		status.checks = []integrationCheck{{text: "OpenUsage has an integration for " + profile.Provider}}
		return status
	}
	hasTool := env.onPath("openusage")
	running := env.running(profile)
	status.runs = "openusage integrations install " + integration
	status.checks = []integrationCheck{
		{ok: hasTool, text: "openusage on PATH"},
		{ok: !running, text: "no instance of " + profile.Name + " running", note: "the installer takes the profile's exclusive lock"},
	}
	switch {
	case !hasTool:
		status.state, status.needs = stateNeeds, "openusage"
		status.reason = "the installer is OpenUsage's own, and openusage is not on PATH"
		status.hint = "install OpenUsage, or put openusage on PATH, and this row can be installed"
	case running:
		status.state, status.note = stateLocked, "running"
		status.reason = "openusage is locked until " + profile.Name + " stops"
		status.hint = "stop its instances (K) and the installer can run"
	default:
		status.state = stateNotRead
		status.action = "install"
	}
	return status
}

func ranmaStatus(profile Profile, env integrationEnv) integrationStatus {
	hasRanma := env.onPath("ranma")
	status := integrationStatus{
		kind:       integrationRanma,
		writes:     `"tmux_shim": true`,
		writesNote: "on " + profile.Name + " in profiles.json",
		launch:     "runs it as  ranma tmux-shim -- " + profile.Command,
		gives:      "tmux panes it opens become ranma panes",
		undo:       "↵ again",
		undoNote:   "or ai integrate ranma … --off",
		sameAs:     "ai integrate ranma " + profile.Name,
		checks: []integrationCheck{
			{ok: hasRanma, text: "ranma on PATH"},
			{ok: profile.Indicator != tmuxIndicator, text: "no tmux status bar on this profile", note: "the two are refused together"},
		},
	}
	if profile.Provider == "claude" {
		status.launch = "adds " + agentTeamsEnv + "=1"
		status.gives = "teammates open in ranma panes"
	}
	if env.inRanma {
		status.here, status.hereApplies = "applies here", true
		status.hereNote = "this TUI is in a ranma pane; a launch from here runs under the shim"
	} else {
		status.here = "applies only when launched from ranma"
		status.hereNote = "this TUI is not in a ranma pane; a launch from here runs without it"
	}
	switch {
	case profile.Indicator == tmuxIndicator:
		status.state, status.note = stateRefused, "tmux bar"
		status.reason = profile.Name + " shows its profile in a tmux status bar, and the shim is refused beside it"
		status.hint = `the status bar has no undo yet: remove "indicator": "tmux" from profiles.json by hand, then turn the shim on`
		if profile.TmuxShim {
			// Set by hand beside the bar: turning it off is the way out.
			status.action, status.sameAs = "turn off", status.sameAs+" --off"
			status.hint = "turn the shim off; a launch refuses the pair until one of them goes"
		} else {
			status.undo, status.undoNote = "", ""
		}
	case profile.TmuxShim:
		status.state, status.inert = stateOn, !env.inRanma
		status.action, status.sameAs = "turn off", status.sameAs+" --off"
		if status.inert {
			status.note = "inert here"
		}
	case !hasRanma:
		status.state, status.needs = stateNeeds, "ranma"
		status.reason = "ranma is not on PATH"
		status.hint = "install ranma, or put it on PATH, and this row can be turned on"
	default:
		status.state = stateOff
		status.action = "turn on"
	}
	return status
}

// word is the state as the box and the listing print it.
func (s integrationStatus) word() string {
	switch s.state {
	case stateOn:
		if s.kind == integrationStatusLine && s.note == "own line" {
			return "● ai's line"
		}
		return "● on"
	case stateAnother:
		return "● another"
	case stateNotRead:
		return "· not read"
	case stateLocked:
		return "▶ locked"
	case stateNA:
		return "— n/a"
	case stateRefused:
		return "✗ refused"
	case stateNeeds:
		return "needs " + s.needs
	}
	return "○ off"
}

// countOn is how many integrations a profile has on, which the palette shows
// beside `I`.
func countOn(statuses []integrationStatus) int {
	count := 0
	for _, status := range statuses {
		if status.state == stateOn {
			count++
		}
	}
	return count
}

// integrateList prints `ai integrate list [profile]`: one line per profile and
// integration, in the words the box uses.
func integrateList(profiles []Profile, env integrationEnv, stdout io.Writer) {
	for _, profile := range profiles {
		for _, status := range integrationsFor(profile, env) {
			line := fmt.Sprintf("%s\t%s\t%s", profile.Name, status.kind.word(), status.word())
			if status.note != "" {
				line += "\t" + status.note
			}
			fmt.Fprintln(stdout, strings.TrimRight(line, "\t"))
		}
	}
}
