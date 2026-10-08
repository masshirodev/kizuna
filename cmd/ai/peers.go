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
	"text/tabwriter"
	"time"
)

// Agents finding each other. Claude Code's own peer messaging only sees the
// sessions under one CLAUDE_CONFIG_DIR, which ai-session gives each profile on
// purpose, and the other providers have none at all. The launcher is the one
// process that sees every profile, so it keeps the directory: `ai peers` lists
// every live instance with the repository and checkout it is working in, and
// `ai send` (inbox.go) leaves one of them a message. doc/agent-messaging.md is
// the account.

// peer is one live instance as another agent needs to see it: where it works,
// what it is doing, and how to reach it.
type peer struct {
	// ID addresses the instance: "<profile>/<run-dir>" for a concurrent
	// instance, the profile name for one holding its profile's exclusive lock.
	ID       string `json:"id"`
	Profile  string `json:"profile"`
	Provider string `json:"provider"`
	PID      int    `json:"pid"`
	Folder   string `json:"folder,omitempty"`
	// Repo is the repository's name, and RepoRoot its main checkout. A
	// worktree reports the same pair as its main checkout, which is what lets
	// "whoever is in ranma" find an agent in either.
	Repo     string `json:"repo,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	Worktree bool   `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// State is "busy" or "idle" where the provider says so, empty otherwise.
	State   string `json:"state,omitempty"`
	Session string `json:"session,omitempty"`
	Title   string `json:"title,omitempty"`
	// Name is Claude Code's slug for the session (`ranma-18`), the handle its
	// own peer messaging uses.
	Name    string    `json:"name,omitempty"`
	Started time.Time `json:"started,omitempty"`
	// Pane is the ranma pane the instance runs in; TmuxSocket the private tmux
	// server its status bar wraps it in. Either is where text can be typed.
	Pane       string `json:"pane,omitempty"`
	TmuxSocket string `json:"tmux_socket,omitempty"`
	// Delivery says how a message reaches the agent: "hooks" (read at its
	// next tool call or turn end), "mcp" (it can read its inbox when it
	// looks), or empty (only by typing into its pane, or a human).
	Delivery string `json:"delivery,omitempty"`
	Unread   int    `json:"unread,omitempty"`
	// Self marks the instance asking.
	Self bool `json:"self,omitempty"`

	lockDir     string
	ranmaSocket string
}

// collectPeers describes every live instance across every profile. It asks
// `claude agents` once per Claude profile with something running and git
// once per folder, so it is a command's work, not a TUI tick's.
func collectPeers(cfg Config) []peer {
	self := selfInstanceDir(cfg)
	var peers []peer
	gitCache := map[string]gitCheckout{}
	for _, profile := range cfg.Profiles {
		instances, err := activeProfileInstances(profile)
		if err != nil || len(instances) == 0 {
			continue
		}
		delivery := messagingDelivery(profile)
		for _, instance := range describeInstances(profile, instances) {
			p := peer{
				ID:       instanceID(profile, instance.lockDir),
				Profile:  profile.Name,
				Provider: profile.Provider,
				PID:      instance.pid,
				Folder:   instance.folder,
				State:    instance.session.status,
				Session:  instance.session.id,
				Title:    instance.session.title,
				Name:     instance.session.name,
				Started:  instance.started,
				Delivery: delivery,
				Unread:   len(inboxMessages(instance.lockDir)),
				Self:     self != "" && filepath.Clean(self) == filepath.Clean(instance.lockDir),
				lockDir:  instance.lockDir,
			}
			meta := readInstanceMeta(instance.lockDir)
			p.Pane, p.ranmaSocket = meta.RanmaPane, meta.RanmaSocket
			if p.Pane == "" {
				// Instances launched before the pane was recorded: the
				// launcher's own environment still says where it runs.
				p.Pane, p.ranmaSocket = launcherRanmaPane(instance.lockDir)
			}
			if profile.Indicator == tmuxIndicator {
				p.TmuxSocket = tmuxSocketPath(instance.lockDir)
			}
			if instance.folder != "" {
				checkout, seen := gitCache[instance.folder]
				if !seen {
					checkout = readGitCheckout(instance.folder)
					gitCache[instance.folder] = checkout
				}
				p.Repo, p.RepoRoot, p.Worktree, p.Branch = checkout.repo, checkout.root, checkout.worktree, checkout.branch
			}
			peers = append(peers, p)
		}
	}
	sort.SliceStable(peers, func(i, j int) bool {
		if peers[i].Repo != peers[j].Repo {
			return peers[i].Repo < peers[j].Repo
		}
		return peers[i].ID < peers[j].ID
	})
	return peers
}

// instanceID names an instance by the directory that holds its lock, which is
// also where its inbox lives.
func instanceID(profile Profile, lockDir string) string {
	if filepath.Base(filepath.Dir(lockDir)) == instancesDirectory {
		return profile.Name + "/" + filepath.Base(lockDir)
	}
	return profile.Name
}

// launcherRanmaPane reads RANMA_PANE out of the launcher's environment, for an
// instance whose meta predates recording it. Linux only, and best effort.
func launcherRanmaPane(lockDir string) (string, string) {
	pids, err := profileLockPIDs(filepath.Join(lockDir, ".active.lock"))
	if err != nil {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pids[0]), "environ"))
	if err != nil {
		return "", ""
	}
	env := strings.Split(string(data), "\x00")
	if !insideRanma(env) {
		return "", ""
	}
	return envValue(env, ranmaPaneEnv), envValue(env, ranmaSocketEnv)
}

type gitCheckout struct {
	repo, root, branch string
	worktree           bool
}

// gitLookupTimeout bounds each git call, so a folder on a hung mount cannot
// stall the listing.
const gitLookupTimeout = 2 * time.Second

// readGitCheckout says which repository a folder belongs to and whether it is
// that repository's main checkout. The common dir is shared by every worktree
// of one repository, so its parent is the main checkout whichever worktree
// asks. A folder outside any repository returns the zero value.
func readGitCheckout(folder string) gitCheckout {
	ctx, cancel := context.WithTimeout(context.Background(), gitLookupTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "-C", folder, "rev-parse",
		"--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return gitCheckout{}
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 {
		return gitCheckout{}
	}
	top, common, branch := lines[0], lines[1], lines[2]
	root := top
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	}
	if branch == "HEAD" {
		branch = "detached"
	}
	return gitCheckout{
		repo:     filepath.Base(root),
		root:     root,
		branch:   branch,
		worktree: filepath.Clean(top) != filepath.Clean(root),
	}
}

func peersCommand(cfg Config, args []string, stdout io.Writer) error {
	rest, asJSON := takeFlag(args, "--json")
	if len(rest) > 1 {
		return errors.New("usage: ai peers [--json] [repo]")
	}
	peers := collectPeers(cfg)
	if len(rest) == 1 {
		peers = peersInRepo(peers, rest[0])
	}
	if asJSON {
		if peers == nil {
			peers = []peer{}
		}
		data, err := json.MarshalIndent(peers, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(data))
		return err
	}
	if len(peers) == 0 {
		fmt.Fprintln(stdout, "no agents running")
		return nil
	}
	writer := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tPROVIDER\tWHERE\tBRANCH\tSTATE\tPANE\tMAIL\tTITLE")
	for _, p := range peers {
		id := p.ID
		if p.Self {
			id += " (you)"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, p.Provider, p.where(), dash(p.Branch), dash(p.State), dash(p.paneLabel()), p.mailLabel(), p.Title)
	}
	return writer.Flush()
}

// peersInRepo keeps the instances working in a repository, named either by
// its name or by any folder inside it.
func peersInRepo(peers []peer, repo string) []peer {
	var kept []peer
	for _, p := range peers {
		if p.Repo == repo || (p.RepoRoot != "" && pathInside(repo, p.RepoRoot)) || p.Folder == repo {
			kept = append(kept, p)
		}
	}
	return kept
}

func (p peer) where() string {
	switch {
	case p.Repo == "":
		return dash(p.Folder)
	case p.Worktree:
		return p.Repo + " (worktree " + filepath.Base(p.Folder) + ")"
	default:
		return p.Repo + " (main checkout)"
	}
}

func (p peer) paneLabel() string {
	switch {
	case p.Pane != "":
		return "ranma " + p.Pane
	case p.TmuxSocket != "":
		return "tmux"
	}
	return ""
}

func (p peer) mailLabel() string {
	label := dash(p.Delivery)
	if p.Unread > 0 {
		label += fmt.Sprintf(" (%d unread)", p.Unread)
	}
	return label
}

func dash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

// resolvePeer finds the one instance a target names: its id, its profile when
// that profile has exactly one instance running, or Claude Code's slug for the
// session. An ambiguous name is refused with the ids it could mean, because a
// message delivered to the wrong agent is acted on by the wrong agent.
func resolvePeer(peers []peer, target string) (peer, error) {
	for _, p := range peers {
		if p.ID == target {
			return p, nil
		}
	}
	var matches []peer
	for _, p := range peers {
		if p.Profile == target || (p.Name != "" && p.Name == target) {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return peer{}, fmt.Errorf("no running agent is called %q; `ai peers` lists them", target)
	}
	ids := make([]string, len(matches))
	for index, p := range matches {
		ids[index] = p.ID
	}
	return peer{}, fmt.Errorf("%q could mean %s; name one by its id", target, strings.Join(ids, ", "))
}
