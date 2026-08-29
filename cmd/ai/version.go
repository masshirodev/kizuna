package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// updateRepo is deliberately not derived from the module path: the module is
	// named for the author while the repository lives under the organisation.
	updateRepo = "masshirodev/ai-session"
	// updateBranch is the repository's default branch, the line a local build
	// falls behind. Hardcoding it keeps the check to a single request.
	updateBranch = "main"
	// updateCacheTTL keeps a TUI launch from spending a GitHub request every
	// time; the answer changes at the speed commits are pushed.
	updateCacheTTL       = 6 * time.Hour
	updateRequestTimeout = 5 * time.Second
	updateCacheFile      = "update-check.json"
)

// updateAPIBase is a variable so tests can point the check at a stub server.
var updateAPIBase = "https://api.github.com"

// buildStamp is what the Go toolchain recorded about this binary's checkout.
// A binary built with -buildvcs=false carries no revision and cannot be
// compared against the repository.
type buildStamp struct {
	revision string
	modified bool
	time     string
}

func currentBuild() buildStamp {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildStamp{}
	}
	var stamp buildStamp
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			stamp.revision = setting.Value
		case "vcs.modified":
			stamp.modified = setting.Value == "true"
		case "vcs.time":
			stamp.time = setting.Value
		}
	}
	return stamp
}

func (b buildStamp) short() string {
	if b.revision == "" {
		return ""
	}
	revision := b.revision
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if b.modified {
		return revision + "+dirty"
	}
	return revision
}

// updateStatus answers "is this binary behind the repository", and doubles as
// the on-disk cache entry.
type updateStatus struct {
	Revision  string    `json:"revision"`
	Behind    int       `json:"behind"`
	Known     bool      `json:"known"`
	Reason    string    `json:"reason,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func (s updateStatus) available() bool {
	return s.Known && s.Behind > 0
}

func (s updateStatus) message() string {
	switch {
	case !s.Known:
		return "update check unavailable: " + s.Reason
	case s.Behind == 1:
		return "1 commit behind " + updateBranch + " · go install ./..."
	case s.Behind > 1:
		return fmt.Sprintf("%d commits behind %s · go install ./...", s.Behind, updateBranch)
	default:
		return "up to date with " + updateBranch
	}
}

// checkForUpdate answers from the cache unless it is stale, describes a
// different build, or the caller asked for a fresh answer. It never returns an
// error: a check that cannot run is a status carrying its own reason, because
// no failure here is worth interrupting a launcher over.
func checkForUpdate(force bool, now time.Time) updateStatus {
	build := currentBuild()
	if build.revision == "" {
		return updateStatus{
			Reason:    "this binary carries no build revision; reinstall with go install ./...",
			CheckedAt: now,
		}
	}
	if !force {
		if cached, ok := cachedUpdateStatus(build.revision, now); ok {
			return cached
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateRequestTimeout)
	defer cancel()
	status := fetchUpdateStatus(ctx, build.revision, now)
	storeUpdateStatus(status)
	return status
}

func fetchUpdateStatus(ctx context.Context, revision string, now time.Time) updateStatus {
	status := updateStatus{Revision: revision, CheckedAt: now}
	token, err := githubToken(ctx)
	if err != nil {
		status.Reason = err.Error()
		return status
	}
	// base...head reads as "head relative to base", so comparing the branch to
	// this build makes behind_by the number of commits this build is missing.
	// per_page trims the commit list; only the counts are read.
	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s?per_page=1", updateAPIBase, updateRepo, updateBranch, revision)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		status.Reason = "the update request could not be built"
		return status
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		status.Reason = "github could not be reached"
		return status
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		status.Reason = "this build's commit is not on " + updateRepo
		return status
	case http.StatusUnauthorized, http.StatusForbidden:
		status.Reason = "the GitHub token cannot read " + updateRepo
		return status
	default:
		status.Reason = "github answered " + response.Status
		return status
	}
	var payload struct {
		BehindBy int `json:"behind_by"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		status.Reason = "github's answer could not be read"
		return status
	}
	status.Behind, status.Known = payload.BehindBy, true
	return status
}

// githubToken finds a token for the private repository. The value is only ever
// sent to api.github.com in an Authorization header; it is never logged, shown,
// or written to the cache.
func githubToken(ctx context.Context) (string, error) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value, nil
		}
	}
	output, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", errors.New("set GH_TOKEN or GITHUB_TOKEN, or run gh auth login")
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", errors.New("gh auth token returned nothing")
	}
	return token, nil
}

func updateCachePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, updateCacheFile), nil
}

func cachedUpdateStatus(revision string, now time.Time) (updateStatus, bool) {
	path, err := updateCachePath()
	if err != nil {
		return updateStatus{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return updateStatus{}, false
	}
	var status updateStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return updateStatus{}, false
	}
	// A cache entry describing a different build says nothing about this one.
	if status.Revision != revision || now.Sub(status.CheckedAt) >= updateCacheTTL {
		return updateStatus{}, false
	}
	return status, true
}

func storeUpdateStatus(status updateStatus) {
	path, err := updateCachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(status)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

func versionCommand(stdout io.Writer) error {
	build := currentBuild()
	if build.revision == "" {
		fmt.Fprintln(stdout, appName+" (no build revision; reinstall with go install ./...)")
	} else if build.time == "" {
		fmt.Fprintln(stdout, appName+" "+build.short())
	} else {
		fmt.Fprintln(stdout, appName+" "+build.short()+" (built "+build.time+")")
	}
	fmt.Fprintln(stdout, checkForUpdate(true, time.Now()).message())
	return nil
}
