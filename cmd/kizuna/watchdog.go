package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
)

// The watchdog is a dead-man's switch for an agent that ends its turn waiting
// on something that never finishes. Nothing wakes an idle Claude Code session
// except a finished background task or typed input, so a coordinator whose
// background job hung sat idle all night. An agent arms the watchdog on itself
// ("nudge me if I sit idle for 30 minutes"), a systemd user timer runs
// `kizuna watchdog --check` every few minutes, and a session idle past its
// deadline gets one line typed at its prompt through the same path `kizuna
// send` uses. It knows nothing about what the agent is waiting for: idle too
// long, nudge. doc/watchdog.md is the account.

const (
	watchdogStateFile     = "watchdog.json"
	watchdogLockFile      = ".watchdog.lock"
	watchdogCheckLockFile = ".watchdog-check.lock"
	// watchdogDefaultMax is how many nudges in a row an agent gets with no
	// busy period between them before the watchdog gives up on it: an agent
	// that ignores three nudges is not helped by a fourth, and a wedged
	// session should not be typed into all night.
	watchdogDefaultMax = 3
	// watchdogMinIdle keeps a threshold above the checker's own interval, so
	// a deadline is not shorter than the time it takes to notice it.
	watchdogMinIdle = time.Minute
	watchdogUnit    = "kizuna-watchdog"
	// watchdogInterval is how often the timer runs the check. The idle clock
	// starts at the first check that sees the agent idle, so a deadline is met
	// up to one interval late.
	watchdogInterval = 2 * time.Minute
	// watchdogLinePrefix marks the typed line as the watchdog's, so the agent
	// does not mistake it for the user.
	watchdogLinePrefix = "[kizuna watchdog]"
)

// watchdogDefaultText is the nudge when the arming agent gives none.
const watchdogDefaultText = "You have been idle past the deadline you set. If you are waiting on something, check whether it is still running and whether it can still finish; if it hung, stop it and carry on."

// watchdogEntry is one armed agent. The fields after Max are the checker's
// bookkeeping, rewritten on every check.
type watchdogEntry struct {
	ID string `json:"id"`
	// LockDir and PID pin the instance the entry was armed on, so an id that
	// outlives its instance is not mistaken for it, and an instance that
	// vanished from one listing but whose lock is still live is not disarmed.
	LockDir string `json:"lock_dir"`
	PID     int    `json:"pid,omitempty"`
	// Idle is the threshold, as a Go duration ("30m").
	Idle    string    `json:"idle"`
	Text    string    `json:"text"`
	Max     int       `json:"max"`
	ArmedAt time.Time `json:"armed_at"`
	ArmedBy string    `json:"armed_by,omitempty"`

	// IdleSince is when the checker first saw the agent idle in the current
	// stretch, or the last nudge, whichever is later. Zero while it is not
	// known to be idle.
	IdleSince time.Time `json:"idle_since,omitzero"`
	// LastState is what the last check saw: busy, idle, or unknown.
	LastState string    `json:"last_state,omitempty"`
	LastCheck time.Time `json:"last_check,omitzero"`
	// Nudges counts every nudge typed; Consecutive only those since the agent
	// was last seen busy. Exhausted is set when Consecutive reaches Max.
	Nudges      int       `json:"nudges"`
	Consecutive int       `json:"consecutive"`
	LastNudge   time.Time `json:"last_nudge,omitzero"`
	Exhausted   bool      `json:"exhausted,omitempty"`
	// LastError is why the last nudge could not be typed.
	LastError string `json:"last_error,omitempty"`
}

func (e watchdogEntry) threshold() time.Duration {
	d, err := time.ParseDuration(e.Idle)
	if err != nil || d < watchdogMinIdle {
		return watchdogMinIdle
	}
	return d
}

type watchdogState struct {
	Agents map[string]watchdogEntry `json:"agents"`
}

func watchdogDir() (string, error) {
	base, err := configBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

func readWatchdogState() (watchdogState, error) {
	state := watchdogState{Agents: map[string]watchdogEntry{}}
	dir, err := watchdogDir()
	if err != nil {
		return state, err
	}
	data, err := os.ReadFile(filepath.Join(dir, watchdogStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("read %s: %w", watchdogStateFile, err)
	}
	if state.Agents == nil {
		state.Agents = map[string]watchdogEntry{}
	}
	return state, nil
}

// updateWatchdogState is the one way the state file changes: under an
// exclusive lock, read, change, and write atomically. Arming, disarming and a
// check's results can then land at the same moment without one losing the
// other. Nothing slow runs while the lock is held, so waiting on it is short.
func updateWatchdogState(change func(*watchdogState) error) error {
	dir, err := watchdogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, watchdogLockFile), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state, err := readWatchdogState()
	if err != nil {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, watchdogStateFile), append(data, '\n'))
}

// resolveWatchdogTarget turns `self`, an id, a profile or a session name into
// the running agent it means. `self` is the instance this process runs inside,
// found the way `kizuna send` names its sender.
func resolveWatchdogTarget(cfg Config, peers []peer, target string) (peer, error) {
	if target == "self" || target == "" {
		id := enclosingInstanceID(cfg, os.Getpid())
		if id == "" {
			return peer{}, errors.New("`self` means the agent this runs inside, and this is not running inside a kizuna-launched session; name the agent by its id (`kizuna peers`)")
		}
		target = id
	}
	return resolvePeer(peers, target)
}

// watchdogReportsIdle is whether kizuna can tell when an agent is idle. Only
// Claude Code says (peers.go), and a watchdog that can never see idle can
// never fire, which is the silent failure this exists to prevent.
func watchdogReportsIdle(p peer) bool {
	return p.Provider == "claude"
}

type watchdogArm struct {
	target string
	idle   time.Duration
	text   string
	max    int
}

func armWatchdog(cfg Config, peers []peer, arm watchdogArm, armedBy string, now time.Time) (watchdogEntry, error) {
	if arm.idle < watchdogMinIdle {
		return watchdogEntry{}, fmt.Errorf("--idle must be at least %s, the checker only looks every %s", watchdogMinIdle, watchdogInterval)
	}
	if arm.max <= 0 {
		arm.max = watchdogDefaultMax
	}
	text := watchdogOneLine(arm.text)
	if text == "" {
		text = watchdogDefaultText
	}
	p, err := resolveWatchdogTarget(cfg, peers, arm.target)
	if err != nil {
		return watchdogEntry{}, err
	}
	switch {
	case p.Headless:
		return watchdogEntry{}, fmt.Errorf("%s is a headless run: it has no prompt to nudge, and ends by itself", p.ID)
	case !watchdogReportsIdle(p):
		return watchdogEntry{}, fmt.Errorf("%s runs %s, which does not report busy or idle, so the watchdog could never tell it is idle; only Claude Code does", p.ID, p.Provider)
	case p.Pane == "" && p.TmuxSocket == "":
		return watchdogEntry{}, fmt.Errorf("%s's pane is unknown (it was not launched in ranma or under the tmux status bar), so there is nowhere to type a nudge", p.ID)
	}
	entry := watchdogEntry{
		ID: p.ID, LockDir: p.lockDir, PID: p.PID,
		Idle: watchdogSpan(arm.idle), Text: text, Max: arm.max,
		ArmedAt: now, ArmedBy: armedBy,
	}
	err = updateWatchdogState(func(state *watchdogState) error {
		state.Agents[entry.ID] = entry
		return nil
	})
	return entry, err
}

// disarmWatchdog removes an entry. A stored id is taken as it is, because the
// agent it names may already be gone; anything else is resolved first.
func disarmWatchdog(cfg Config, peers func() []peer, target string) (string, error) {
	var id string
	if target != "" && target != "self" {
		state, err := readWatchdogState()
		if err != nil {
			return "", err
		}
		if _, armed := state.Agents[target]; armed {
			id = target
		}
	}
	if id == "" {
		p, err := resolveWatchdogTarget(cfg, peers(), target)
		if err != nil {
			return "", err
		}
		id = p.ID
	}
	found := false
	err := updateWatchdogState(func(state *watchdogState) error {
		_, found = state.Agents[id]
		delete(state.Agents, id)
		return nil
	})
	if err != nil {
		return "", err
	}
	if !found {
		return id + " had no watchdog armed", nil
	}
	return "watchdog disarmed for " + id, nil
}

// watchdogOneLine makes the nudge text safe to type: one line, no control
// characters, so nothing but the final Enter submits it.
func watchdogOneLine(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

// watchdogLine is what gets typed: who is typing, how long the agent has been
// idle, and the armed text.
func watchdogLine(e watchdogEntry, now time.Time) string {
	return fmt.Sprintf("%s Idle for %s (deadline %s, nudge %d of %d). %s",
		watchdogLinePrefix, watchdogSpan(now.Sub(e.IdleSince)), watchdogSpan(e.threshold()), e.Consecutive+1, e.Max, e.Text)
}

type watchdogAction int

const (
	watchdogWait watchdogAction = iota
	watchdogNudge
	watchdogDisarm
)

// stepWatchdog is one check of one armed agent: what it saw, and what to do.
// found is whether the agent was among the live peers; alive whether its lock
// is still held, which tells a vanished agent from one a listing missed.
func stepWatchdog(e watchdogEntry, p peer, found, alive bool, now time.Time) (watchdogEntry, watchdogAction) {
	e.LastCheck = now
	if !found {
		if !alive {
			return e, watchdogDisarm
		}
		// Still running, but the listing could not describe it: unknown.
		e.LastState, e.IdleSince = "unknown", time.Time{}
		return e, watchdogWait
	}
	if e.PID != 0 && p.PID != 0 && p.PID != e.PID {
		return e, watchdogDisarm
	}
	state := p.State
	if p.Headless {
		state = ""
	}
	switch state {
	case "busy":
		e.LastState = "busy"
		e.IdleSince = time.Time{}
		e.Consecutive, e.Exhausted = 0, false
		return e, watchdogWait
	case "idle":
		e.LastState = "idle"
		if e.IdleSince.IsZero() {
			e.IdleSince = now
			return e, watchdogWait
		}
		if e.Exhausted || e.Consecutive >= e.Max || now.Sub(e.IdleSince) < e.threshold() {
			return e, watchdogWait
		}
		return e, watchdogNudge
	}
	// Unknown is never typed into, and breaks the idle stretch: the clock
	// only runs while every check has seen the agent idle.
	e.LastState, e.IdleSince = "unknown", time.Time{}
	return e, watchdogWait
}

// recordNudge restarts the idle clock from the nudge and counts it.
func recordNudge(e watchdogEntry, now time.Time) watchdogEntry {
	e.Nudges++
	e.Consecutive++
	e.LastNudge, e.IdleSince, e.LastError = now, now, ""
	if e.Consecutive >= e.Max {
		e.Exhausted = true
	}
	return e
}

// watchdogProbe is what a check asks the outside world, so a test can answer
// instead of claude, ranma and the process table.
type watchdogProbe struct {
	peers    func(Config) []peer
	alive    func(lockDir string) bool
	typeLine func(peer, string) error
	now      func() time.Time
}

func liveWatchdogProbe() watchdogProbe {
	return watchdogProbe{
		peers: collectPeers,
		alive: func(lockDir string) bool {
			if lockDir == "" {
				return false
			}
			active, err := profileLockIsActive(filepath.Join(lockDir, ".active.lock"))
			return err == nil && active
		},
		typeLine: typeIntoPane,
		now:      time.Now,
	}
}

// watchdogCheck runs one round. Only one round runs at a time; a second one
// started meanwhile (the timer and a manual run) leaves at once rather than
// nudging the same agent twice.
func watchdogCheck(cfg Config, probe watchdogProbe, stdout io.Writer) error {
	dir, err := watchdogDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	checkLock, err := os.OpenFile(filepath.Join(dir, watchdogCheckLockFile), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer checkLock.Close()
	if err := syscall.Flock(int(checkLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Fprintln(stdout, "another watchdog check is running")
		return nil
	}
	defer syscall.Flock(int(checkLock.Fd()), syscall.LOCK_UN)

	snapshot, err := readWatchdogState()
	if err != nil {
		return err
	}
	if len(snapshot.Agents) == 0 {
		return nil
	}
	// Only the profiles something is armed in are asked, so an idle machine's
	// check costs one file read and a busy one a `claude agents` per profile.
	armedProfiles := map[string]bool{}
	for id := range snapshot.Agents {
		profile, _, _ := strings.Cut(id, "/")
		armedProfiles[profile] = true
	}
	scoped := cfg
	scoped.Profiles = nil
	for _, profile := range cfg.Profiles {
		if armedProfiles[profile.Name] {
			scoped.Profiles = append(scoped.Profiles, profile)
		}
	}
	byID := map[string]peer{}
	for _, p := range probe.peers(scoped) {
		byID[p.ID] = p
	}

	type outcome struct {
		entry  watchdogEntry
		remove bool
	}
	outcomes := map[string]outcome{}
	ids := make([]string, 0, len(snapshot.Agents))
	for id := range snapshot.Agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := snapshot.Agents[id]
		p, found := byID[id]
		alive := found || probe.alive(entry.LockDir)
		now := probe.now()
		next, action := stepWatchdog(entry, p, found, alive, now)
		switch action {
		case watchdogDisarm:
			outcomes[id] = outcome{remove: true}
			fmt.Fprintf(stdout, "%s is gone; its watchdog is disarmed\n", id)
			continue
		case watchdogNudge:
			// The state came from this round's listing, a moment ago; the
			// gate is the same one `kizuna send` applies before typing.
			if p.State != "idle" || p.Headless {
				break
			}
			if err := probe.typeLine(p, watchdogLine(next, now)); err != nil {
				next.LastError = err.Error()
				fmt.Fprintf(stdout, "%s: could not type the nudge: %v\n", id, err)
				break
			}
			next = recordNudge(next, now)
			fmt.Fprintf(stdout, "%s: nudged (%d of %d)\n", id, next.Consecutive, next.Max)
			if next.Exhausted {
				fmt.Fprintf(stdout, "%s: %d nudges with no busy period between them; no more until it is seen busy\n", id, next.Max)
			}
		}
		outcomes[id] = outcome{entry: next}
	}
	// An entry re-armed or disarmed while this round ran is the newer word,
	// and is left as it is.
	return updateWatchdogState(func(state *watchdogState) error {
		for id, result := range outcomes {
			current, ok := state.Agents[id]
			if !ok || !current.ArmedAt.Equal(snapshot.Agents[id].ArmedAt) {
				continue
			}
			if result.remove {
				delete(state.Agents, id)
			} else {
				state.Agents[id] = result.entry
			}
		}
		return nil
	})
}

func watchdogList(stdout io.Writer, asJSON bool, now time.Time) error {
	state, err := readWatchdogState()
	if err != nil {
		return err
	}
	entries := make([]watchdogEntry, 0, len(state.Agents))
	for _, entry := range state.Agents {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	if asJSON {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no watchdog armed")
		return nil
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tIDLE\tSTATUS\tSEEN\tIDLE SINCE\tNUDGES\tLAST NUDGE\tTEXT")
	for _, e := range entries {
		status := "armed"
		if e.Exhausted {
			status = "exhausted"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d/%d (%d total)\t%s\t%s\n",
			e.ID, watchdogSpan(e.threshold()), status, dash(e.LastState), watchdogAgo(e.IdleSince, now),
			e.Consecutive, e.Max, e.Nudges, watchdogAgo(e.LastNudge, now), watchdogEllipsis(e.Text, 48))
	}
	return writer.Flush()
}

func watchdogAgo(t, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("15:04") + " (" + watchdogSpan(now.Sub(t)) + " ago)"
}

// watchdogSpan writes a duration to the minute the way it is typed on the
// command line: 1m, 1h30m, 2h.
func watchdogSpan(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return "<1m"
	}
	text := strings.TrimSuffix(d.String(), "0s")
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}

func watchdogEllipsis(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

// --- the timer ---------------------------------------------------------------

// systemctlUser runs `systemctl --user`; a test replaces it.
var systemctlUser = func(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func watchdogUnitDir() (string, error) {
	base, err := configBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "systemd", "user"), nil
}

// systemdEscape keeps a value literal in a unit file, where % starts a
// specifier.
func systemdEscape(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

// watchdogUnits renders the oneshot service and its timer. The service
// carries the PATH it was installed from, because a user service starts with
// systemd's short default PATH, and the check needs `claude` to ask who is
// idle and `ranma` or `tmux` to type.
func watchdogUnits(executable, path, configHome string) (service, timer string) {
	service = fmt.Sprintf(`# Written by kizuna watchdog --install; kizuna watchdog --uninstall removes it.
[Unit]
Description=kizuna watchdog: nudge agents idle past their deadline
Documentation=https://github.com/masshirodev/kizuna/blob/main/doc/watchdog.md

[Service]
Type=oneshot
Environment="PATH=%s"
Environment="XDG_CONFIG_HOME=%s"
ExecStart="%s" watchdog --check
TimeoutStartSec=120
`, systemdEscape(path), systemdEscape(configHome), systemdEscape(executable))
	timer = fmt.Sprintf(`# Written by kizuna watchdog --install; kizuna watchdog --uninstall removes it.
[Unit]
Description=Run the kizuna watchdog check every %s

[Timer]
OnBootSec=%s
OnUnitActiveSec=%s
AccuracySec=15s

[Install]
WantedBy=timers.target
`, watchdogInterval, watchdogSystemdSpan(watchdogInterval), watchdogSystemdSpan(watchdogInterval))
	return service, timer
}

func watchdogSystemdSpan(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + "s"
}

func installWatchdogTimer(stdout io.Writer) error {
	dir, err := watchdogUnitDir()
	if err != nil {
		return err
	}
	base, err := configBase()
	if err != nil {
		return err
	}
	service, timer := watchdogUnits(aiExecutable(), os.Getenv("PATH"), base)
	for name, content := range map[string]string{watchdogUnit + ".service": service, watchdogUnit + ".timer": timer} {
		if err := writeFileAtomic(filepath.Join(dir, name), []byte(content)); err != nil {
			return err
		}
	}
	if err := systemctlUser("daemon-reload"); err != nil {
		return err
	}
	if err := systemctlUser("enable", "--now", watchdogUnit+".timer"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "installed %s.timer in %s: it runs `kizuna watchdog --check` every %s\n", watchdogUnit, dir, watchdogInterval)
	fmt.Fprintf(stdout, "logs: journalctl --user -u %s.service\n", watchdogUnit)
	return nil
}

func uninstallWatchdogTimer(stdout io.Writer) error {
	dir, err := watchdogUnitDir()
	if err != nil {
		return err
	}
	// Disabling a timer that was never enabled fails; the files still go.
	_ = systemctlUser("disable", "--now", watchdogUnit+".timer")
	for _, name := range []string{watchdogUnit + ".timer", watchdogUnit + ".service"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := systemctlUser("daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "removed %s.timer; armed entries stay in %s until disarmed\n", watchdogUnit, watchdogStateFile)
	return nil
}

// --- the command ---------------------------------------------------------------

const watchdogUsage = `usage:
  kizuna watchdog <id|self> --idle 30m [--max 3] ["nudge text"]   arm
  kizuna watchdog --off [<id|self>]                               disarm (self when left out)
  kizuna watchdog --list [--json]                                 what is armed
  kizuna watchdog --check                                         one round (the timer runs this)
  kizuna watchdog --install | --uninstall                         the systemd user timer`

// takeValueFlag removes `--name value` or `--name=value` from args.
func takeValueFlag(args []string, name string) ([]string, string, bool, error) {
	rest := make([]string, 0, len(args))
	value, found := "", false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == name:
			if index+1 >= len(args) {
				return nil, "", false, fmt.Errorf("%s needs a value", name)
			}
			value, found = args[index+1], true
			index++
		case strings.HasPrefix(arg, name+"="):
			value, found = strings.TrimPrefix(arg, name+"="), true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, value, found, nil
}

func watchdogCommand(cfg Config, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(watchdogUsage)
	}
	switch args[0] {
	case "--check":
		return watchdogCheck(cfg, liveWatchdogProbe(), stdout)
	case "--list":
		rest, asJSON := takeFlag(args[1:], "--json")
		if len(rest) > 0 {
			return errors.New(watchdogUsage)
		}
		return watchdogList(stdout, asJSON, time.Now())
	case "--off":
		if len(args) > 2 {
			return errors.New(watchdogUsage)
		}
		target := "self"
		if len(args) == 2 {
			target = args[1]
		}
		summary, err := disarmWatchdog(cfg, func() []peer { return collectPeers(cfg) }, target)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, summary)
		return nil
	case "--install":
		return installWatchdogTimer(stdout)
	case "--uninstall":
		return uninstallWatchdogTimer(stdout)
	}
	arm, err := parseWatchdogArm(args)
	if err != nil {
		return err
	}
	entry, err := armWatchdog(cfg, collectPeers(cfg), arm, sender(cfg), time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, watchdogArmedSummary(entry))
	return nil
}

func parseWatchdogArm(args []string) (watchdogArm, error) {
	rest, idle, hasIdle, err := takeValueFlag(args, "--idle")
	if err != nil {
		return watchdogArm{}, err
	}
	rest, maxText, hasMax, err := takeValueFlag(rest, "--max")
	if err != nil {
		return watchdogArm{}, err
	}
	if !hasIdle || len(rest) < 1 || strings.HasPrefix(rest[0], "-") {
		return watchdogArm{}, errors.New(watchdogUsage)
	}
	arm := watchdogArm{target: rest[0], text: strings.Join(rest[1:], " "), max: watchdogDefaultMax}
	if arm.idle, err = time.ParseDuration(idle); err != nil {
		return watchdogArm{}, fmt.Errorf("--idle %q: %w (write it like 30m or 1h30m)", idle, err)
	}
	if hasMax {
		if arm.max, err = strconv.Atoi(maxText); err != nil || arm.max < 1 {
			return watchdogArm{}, fmt.Errorf("--max %q: want a whole number of nudges, at least 1", maxText)
		}
	}
	return arm, nil
}

func watchdogArmedSummary(e watchdogEntry) string {
	summary := fmt.Sprintf("watchdog armed for %s: idle %s or longer gets a nudge typed at its prompt, at most %d in a row with no busy period between them. Disarm with `kizuna watchdog --off %s`.",
		e.ID, watchdogSpan(e.threshold()), e.Max, e.ID)
	if !watchdogTimerInstalled() {
		summary += fmt.Sprintf(" Warning: the %s.timer is not installed, so nothing checks it yet; the user runs `kizuna watchdog --install` once.", watchdogUnit)
	}
	return summary
}

// watchdogTimerInstalled is whether --install has written the timer. An armed
// watchdog with no checker is the silent failure this exists to prevent, so
// arming says so.
func watchdogTimerInstalled() bool {
	dir, err := watchdogUnitDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, watchdogUnit+".timer"))
	return err == nil
}
