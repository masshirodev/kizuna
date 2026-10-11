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
	"sync"
	"syscall"
	"text/tabwriter"
	"time"
)

// Coming back from a power cut. Every interactive launch keeps a restore
// record while it runs: the profile, the folder, the conversation it has open,
// whether the agent was busy, and the ranma session and workspace it sat in.
// A launch whose CLI exits on its own deletes its record on the way out. One
// that is cut off -- the power goes, the machine reboots, the launcher is
// killed -- leaves it behind, and `kizuna resume-all` reopens every conversation
// so left, each in a ranma pane where it used to be. doc/restore.md is the
// account.
//
// The records sit outside the instance directories on purpose: those are
// reclaimed by the first scanner that finds their lock dead, which after a
// reboot is whichever `ai` runs first.

const (
	restoreDirectory = "restore"
	// restoreFirstRefresh is when a launch first looks for its conversation:
	// the CLI needs a moment to start one and report it.
	restoreFirstRefresh = 5 * time.Second
	// restoreRefreshInterval is how often a running launch rewrites its
	// record. A blackout loses at most this much: a conversation switched
	// (`/resume`, `/clear`) since the last write reopens as the one before.
	restoreRefreshInterval = 20 * time.Second
	// restoreLookupTimeout bounds the ranma call each refresh makes.
	restoreLookupTimeout = 2 * time.Second
)

// restoreRecord is one launch as it stood at its last refresh.
type restoreRecord struct {
	ID       string `json:"id"`
	Profile  string `json:"profile"`
	Provider string `json:"provider"`
	Folder   string `json:"folder"`
	// Session is the conversation's id, empty where the provider does not say
	// (OpenCode, Antigravity, DeepSeek), which reopens the folder's latest.
	Session string `json:"session,omitempty"`
	Title   string `json:"title,omitempty"`
	// State is "busy" or "idle" where the provider reports it.
	State string `json:"state,omitempty"`
	// Where the launch sat in ranma, so it can be reopened there.
	RanmaSession       string `json:"ranma_session,omitempty"`
	RanmaWorkspace     int    `json:"ranma_workspace,omitempty"`
	RanmaWorkspaceName string `json:"ranma_workspace_name,omitempty"`
	// Boot and PID say whether the launch is still alive: a record from
	// another boot is dead whatever its pid now names.
	Boot    string    `json:"boot"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Updated time.Time `json:"updated"`

	path string
}

func restoreDir() (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), restoreDirectory), nil
}

// currentBootID names this boot of the machine. Empty where the kernel does
// not say, which leaves liveness to the pid alone.
func currentBootID() string {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// restorable is whether a launch is worth reopening after a power cut: someone
// is typing into it, and it starts a session rather than managing the CLI
// (`claude mcp list` ends by itself and has nothing to come back to).
func restorable(profile Profile, args []string) bool {
	if headlessLaunch(profile.Provider, args) {
		return false
	}
	if index := subcommandIndex(args); index >= 0 {
		if known, takesDefaults := subcommandDisposition(profile.Provider, args[index]); known && !takesDefaults {
			return false
		}
	}
	return true
}

// restoreLedger keeps one launch's record current while it runs.
type restoreLedger struct {
	profile Profile
	child   int
	env     []string
	pane    string
	done    chan struct{}
	stopped sync.WaitGroup

	mu     sync.Mutex
	record restoreRecord
}

// startRestoreLedger writes the launch's record at once, so a blackout in the
// first seconds still finds the folder, and refreshes it until finish. A
// record that cannot be written costs only the restore, never the launch.
func startRestoreLedger(profile Profile, lockDir string, child int, folder string, env []string) *restoreLedger {
	dir, err := restoreDir()
	if err != nil || os.MkdirAll(dir, 0700) != nil {
		return nil
	}
	id := instanceID(profile, lockDir)
	now := time.Now()
	ledger := &restoreLedger{
		profile: profile,
		child:   child,
		env:     env,
		done:    make(chan struct{}),
		record: restoreRecord{
			ID:       id,
			Profile:  profile.Name,
			Provider: profile.Provider,
			Folder:   folder,
			Boot:     currentBootID(),
			PID:      os.Getpid(),
			Started:  now,
			Updated:  now,
			path:     filepath.Join(dir, strings.ReplaceAll(id, "/", "_")+".json"),
		},
	}
	if insideRanma(env) {
		ledger.pane = envValue(env, ranmaPaneEnv)
	}
	ledger.refresh()
	ledger.stopped.Add(1)
	go ledger.loop()
	return ledger
}

func (ledger *restoreLedger) loop() {
	defer ledger.stopped.Done()
	timer := time.NewTimer(restoreFirstRefresh)
	defer timer.Stop()
	for {
		select {
		case <-ledger.done:
			return
		case <-timer.C:
			ledger.refresh()
			timer.Reset(restoreRefreshInterval)
		}
	}
}

// refresh reads what the launch has open now and rewrites the record.
func (ledger *restoreLedger) refresh() {
	session := liveSession(ledger.profile, ledger.child, ledger.record.Folder)
	ranmaSession, workspace, workspaceName := ranmaPlacement(ledger.env, ledger.pane)

	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	record := &ledger.record
	if session.id != "" {
		if session.id != record.Session || record.Title == "" {
			// The title comes off the transcript, so it is read once per
			// conversation rather than every refresh.
			record.Title = session.title
			if ledger.profile.Provider == "claude" {
				record.Title = titleClaudeSession(ledger.profile, session).title
			}
		}
		record.Session = session.id
	}
	record.State = session.status
	if ranmaSession != "" {
		record.RanmaSession, record.RanmaWorkspace, record.RanmaWorkspaceName = ranmaSession, workspace, workspaceName
	}
	record.Updated = time.Now()
	_ = writeRestoreRecord(*record)
}

// finish stops the refreshes, and drops the record unless the launch was cut
// off: a CLI that exited, or a pane somebody closed, ended on purpose and has
// nothing to come back to.
func (ledger *restoreLedger) finish(cutOff bool) {
	if ledger == nil {
		return
	}
	close(ledger.done)
	ledger.stopped.Wait()
	if !cutOff {
		_ = os.Remove(ledger.record.path)
	}
}

// cutOff is whether the signal that stopped a launch came from the machine
// going down rather than from its user. SIGTERM is what a shutdown sends.
// SIGHUP is what closing a ranma pane or a terminal sends, which is a choice,
// except while the system is stopping, when it is the pane going down with
// everything else.
func cutOff(stoppedBy os.Signal, stopping func() bool) bool {
	switch stoppedBy {
	case syscall.SIGTERM:
		return true
	case syscall.SIGHUP:
		return stopping()
	}
	return false
}

// systemStopping asks systemd whether the machine is shutting down. Anything
// but a clear "stopping" (no systemd, a slow answer) reads as no.
func systemStopping() bool {
	ctx, cancel := context.WithTimeout(context.Background(), restoreLookupTimeout)
	defer cancel()
	output, _ := exec.CommandContext(ctx, "systemctl", "is-system-running").Output()
	return strings.TrimSpace(string(output)) == "stopping"
}

func writeRestoreRecord(record restoreRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	temp := record.path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temp, record.path)
}

// liveSession says which conversation a launch's CLI has open, without asking
// the CLI: Claude Code keeps a file per process under its config dir, and
// Codex's rollouts name the folder they ran in.
func liveSession(profile Profile, child int, folder string) instanceSession {
	switch profile.Provider {
	case "claude":
		root, err := profileRoot()
		if err != nil {
			return instanceSession{}
		}
		return claudeSessionOfProcess(filepath.Join(root, profile.Name, "claude", "sessions"), child)
	case "codex":
		return codexSessionInFolder(profile, folder)
	}
	return instanceSession{}
}

type claudeProcessSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

// claudeSessionOfProcess finds the session file of the Claude Code process the
// launcher started, or of one beneath it when a wrapper (the tmux indicator)
// sits between them.
func claudeSessionOfProcess(dir string, child int) instanceSession {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session claudeProcessSession
		if json.Unmarshal(data, &session) != nil || session.SessionID == "" {
			continue
		}
		if session.PID == child || descendsFrom(session.PID, child) {
			return instanceSession{id: session.SessionID, title: session.Name, name: session.Name, status: session.Status}
		}
	}
	return instanceSession{}
}

func descendsFrom(pid, ancestor int) bool {
	for current, steps := pid, 0; current > 1 && steps < 64; steps++ {
		parent, ok := parentPID(current)
		if !ok {
			return false
		}
		if parent == ancestor {
			return true
		}
		current = parent
	}
	return false
}

type ranmaPane struct {
	ID            int     `json:"id"`
	Session       string  `json:"session"`
	Workspace     int     `json:"workspace"`
	WorkspaceName *string `json:"workspace_name"`
}

// ranmaPlacement asks ranma where a pane is now: panes move between
// workspaces, so the launch's own record of it would go stale.
func ranmaPlacement(env []string, pane string) (string, int, string) {
	if pane == "" {
		return "", 0, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), restoreLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ranma", "panes", "--json")
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		return "", 0, ""
	}
	var panes []ranmaPane
	if json.Unmarshal(output, &panes) != nil {
		return "", 0, ""
	}
	for _, candidate := range panes {
		if strconv.Itoa(candidate.ID) == pane {
			name := ""
			if candidate.WorkspaceName != nil {
				name = *candidate.WorkspaceName
			}
			return candidate.Session, candidate.Workspace, name
		}
	}
	return "", 0, ""
}

// readRestoreRecords loads every record, skipping files that do not parse.
func readRestoreRecords(dir string) []restoreRecord {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var records []restoreRecord
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record restoreRecord
		if json.Unmarshal(data, &record) != nil || record.Profile == "" {
			continue
		}
		record.path = path
		records = append(records, record)
	}
	return records
}

// recordAlive is whether the launch behind a record is still running.
func recordAlive(record restoreRecord, boot string) bool {
	if boot != "" && record.Boot != boot {
		return false
	}
	if record.PID <= 0 {
		return false
	}
	return !errors.Is(syscall.Kill(record.PID, 0), syscall.ESRCH)
}

// restoreCandidates picks the records worth reopening: their launch is gone,
// their conversation is not open again somewhere already, and each
// conversation appears once, as its latest record. The order is where they
// sat, so the panes come back workspace by workspace.
func restoreCandidates(records []restoreRecord, alive func(restoreRecord) bool) (candidates, stale []restoreRecord) {
	open := map[string]bool{}
	var dead []restoreRecord
	for _, record := range records {
		if alive(record) {
			if record.Session != "" {
				open[record.Profile+"/"+record.Session] = true
			}
			continue
		}
		dead = append(dead, record)
	}
	sort.SliceStable(dead, func(i, j int) bool { return dead[i].Updated.After(dead[j].Updated) })
	seen := map[string]bool{}
	for _, record := range dead {
		key := record.Profile + "/" + record.Session
		if record.Session == "" {
			// Without an id the folder is the conversation: its latest.
			key = record.Profile + "@" + record.Folder
		}
		if open[key] || seen[key] {
			stale = append(stale, record)
			continue
		}
		seen[key] = true
		candidates = append(candidates, record)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.RanmaSession != b.RanmaSession {
			return a.RanmaSession < b.RanmaSession
		}
		if a.RanmaWorkspace != b.RanmaWorkspace {
			return a.RanmaWorkspace < b.RanmaWorkspace
		}
		return a.Started.Before(b.Started)
	})
	return candidates, stale
}

// reopenCommand is the shell line that brings a record's conversation back,
// leaving a shell in the pane once the CLI exits, as a pane opened by hand
// would have.
func reopenCommand(record restoreRecord) (string, error) {
	args, err := reopenArgs(record.Provider, instanceSession{id: record.Session})
	if err != nil {
		return "", err
	}
	return formatArguments(append([]string{appName, record.Profile}, args...)) + "; exec \"${SHELL:-sh}\"", nil
}

// ranmaOpenArgs places a reopened conversation where it was: its old session
// and workspace when it was in ranma, a fresh workspace otherwise. Focus stays
// where it is, so a dozen panes opening do not drag the screen around.
func ranmaOpenArgs(record restoreRecord, command string) []string {
	args := []string{"open", "--background", "--cwd", record.Folder}
	if record.RanmaSession != "" && record.RanmaWorkspace > 0 {
		args = append(args, "--session", record.RanmaSession, "--workspace", strconv.Itoa(record.RanmaWorkspace))
		if record.RanmaWorkspaceName != "" {
			args = append(args, "--workspace-name", record.RanmaWorkspaceName)
		}
	} else {
		args = append(args, "--workspace", "empty")
	}
	if name := record.paneName(); name != "" {
		args = append(args, "--name", name)
	}
	return append(args, "--", command)
}

func (record restoreRecord) paneName() string {
	if record.Title != "" {
		return record.Title
	}
	return filepath.Base(record.Folder)
}

func (record restoreRecord) where() string {
	if record.RanmaSession == "" {
		return "—"
	}
	return fmt.Sprintf("%s:%d", record.RanmaSession, record.RanmaWorkspace)
}

func resumeAllCommand(cfg Config, args []string, stdout io.Writer) error {
	rest, dryRun := takeFlag(args, "--dry-run")
	rest, short := takeFlag(rest, "-n")
	rest, busyOnly := takeFlag(rest, "--busy")
	rest, forget := takeFlag(rest, "--forget")
	dryRun = dryRun || short
	if len(rest) > 0 {
		return errors.New("usage: kizuna resume-all [--busy] [--dry-run|-n] [--forget]")
	}
	dir, err := restoreDir()
	if err != nil {
		return err
	}
	boot := currentBootID()
	candidates, stale := restoreCandidates(readRestoreRecords(dir), func(record restoreRecord) bool {
		return recordAlive(record, boot)
	})
	known := map[string]bool{}
	for _, profile := range cfg.Profiles {
		known[profile.Name] = true
	}
	var kept []restoreRecord
	for _, record := range candidates {
		switch {
		case !known[record.Profile]:
			// A profile removed since: nothing left to reopen it under.
			stale = append(stale, record)
		case busyOnly && record.State != "busy":
		default:
			kept = append(kept, record)
		}
	}
	if !dryRun {
		for _, record := range stale {
			_ = os.Remove(record.path)
		}
	}
	if len(kept) == 0 {
		fmt.Fprintln(stdout, "nothing to resume: every recorded session ended on its own or is open again")
		return nil
	}
	if forget {
		if !dryRun {
			for _, record := range kept {
				_ = os.Remove(record.path)
			}
		}
		fmt.Fprintf(stdout, "forgot %d session(s)\n", len(kept))
		return nil
	}

	inRanma := insideRanma(os.Environ())
	if dryRun || !inRanma {
		writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "PROFILE\tSTATE\tWHERE\tFOLDER\tTITLE\tCOMMAND")
		for _, record := range kept {
			command, err := reopenCommand(record)
			if err != nil {
				command = err.Error()
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", record.Profile, dash(record.State), record.where(),
				record.Folder, dash(record.Title), "cd "+formatArguments([]string{record.Folder})+" && "+command)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if !dryRun {
			fmt.Fprintln(stdout, "\nnot in a ranma pane, so nothing was opened: run each line in a terminal of its own, or run kizuna resume-all from ranma")
		}
		return nil
	}

	ranma, err := exec.LookPath("ranma")
	if err != nil {
		return errors.New("ranma is not on PATH; kizuna resume-all --dry-run prints the commands instead")
	}
	opened := 0
	for _, record := range kept {
		command, err := reopenCommand(record)
		if err != nil {
			fmt.Fprintf(stdout, "skipped %s in %s: %v\n", record.Profile, record.Folder, err)
			continue
		}
		if _, err := os.Stat(record.Folder); err != nil {
			fmt.Fprintf(stdout, "skipped %s: %s is gone\n", record.Profile, record.Folder)
			continue
		}
		output, err := exec.Command(ranma, ranmaOpenArgs(record, command)...).CombinedOutput()
		if err != nil {
			fmt.Fprintf(stdout, "could not open %s in %s: %v %s\n", record.Profile, record.Folder, err, strings.TrimSpace(string(output)))
			continue
		}
		// The reopened launch writes a record of its own.
		_ = os.Remove(record.path)
		opened++
		fmt.Fprintf(stdout, "reopened %s %s in %s (%s)\n", record.Profile, dash(record.Title), record.Folder, record.where())
	}
	fmt.Fprintf(stdout, "%d of %d session(s) reopened\n", opened, len(kept))
	return nil
}
