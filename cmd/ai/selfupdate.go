package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	// sourceDirEnv points self-update at an arbitrary checkout instead of the
	// managed clone below — for testing self-update itself against a branch
	// that isn't pushed yet. Deliberately an env var and not a persisted file:
	// it can't be set once and forgotten the way a written path could.
	sourceDirEnv = "AI_SOURCE_DIR"
	// repoDirName is the dedicated clone self-update rebuilds from, kept
	// separate from wherever a development checkout of this repository happens
	// to live (mirrors the deploy-checkout/dev-checkout split this launcher's
	// own maintainer uses for other projects).
	repoDirName = "repo"
)

// repoDir is the managed clone self-update rebuilds from by default.
func repoDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, repoDirName), nil
}

// validateSourceDir refuses anything that is not a checkout of this repository.
// The update runs `git pull` and a build in whatever it is handed, so pointing
// it at the wrong directory has to fail here rather than there.
func validateSourceDir(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	for _, marker := range []string{"go.mod", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
			return "", fmt.Errorf("%s is not an ai-session checkout (no %s)", dir, marker)
		}
	}
	return dir, nil
}

// previewUpdateDir reports where a self-update would run from, without any of
// ensureRepo's side effects (cloning, switching branches) — for showing the
// checkout path before the user has confirmed anything.
func previewUpdateDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(sourceDirEnv)); override != "" {
		return validateSourceDir(override)
	}
	return repoDir()
}

// ensureRepo makes sure the managed clone exists and is on updateBranch,
// doing the minimum necessary: silent when there is nothing to do (already
// cloned, already on the right branch), echoing "› git ..." only for what it
// actually runs, matching runSelfUpdate's own transparency convention. An
// existing repoDir that isn't a git checkout is refused by name rather than
// silently reclaimed — it might be someone's own directory.
func ensureRepo(stdout, stderr io.Writer) (string, error) {
	dir, err := repoDir()
	if err != nil {
		return "", err
	}
	switch _, statErr := os.Stat(dir); {
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return "", err
		}
		if err := runGit("", stdout, stderr, "clone", "git@github.com:"+updateRepo+".git", dir); err != nil {
			return "", err
		}
	case statErr != nil:
		return "", statErr
	default:
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			return "", fmt.Errorf("%s exists but is not a git checkout; move it aside and re-run ai self-update to reclone", dir)
		}
	}

	branch, err := gitCurrentBranch(dir)
	if err != nil {
		return "", err
	}
	if branch != updateBranch {
		if err := runGit(dir, stdout, stderr, "checkout", updateBranch); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// gitCurrentBranch reports dir's checked-out branch. Unlike runGit this
// neither echoes nor streams to a caller, since "which branch is this on"
// isn't something a person needs narrated, and unlike handoff.go's own
// gitOutput this surfaces a failure rather than swallowing it into "" — an
// empty string here must mean detached HEAD, never "the command failed."
func gitCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runGit runs one git command for its side effects, echoing it first the
// same way runSelfUpdate echoes its own steps. dir is the working directory;
// empty means the current one, which is what cloning into a not-yet-existing
// directory needs.
func runGit(dir string, stdout, stderr io.Writer, args ...string) error {
	fmt.Fprintln(stdout, "› git "+strings.Join(args, " "))
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

// selfUpdateSteps is what a person would type. install.sh is preferred over a
// bare `go install` so there is one definition of how ai is built — including
// the VCS stamp the update check itself depends on.
func selfUpdateSteps(dir string) [][]string {
	steps := [][]string{{"git", "pull", "--ff-only"}}
	if info, err := os.Stat(filepath.Join(dir, "install.sh")); err == nil && !info.IsDir() {
		return append(steps, []string{"sh", "./install.sh"})
	}
	return append(steps, []string{"go", "install", "./cmd/ai"})
}

// runSelfUpdate fast-forwards the checkout and rebuilds. It stops at the first
// failure: rebuilding a checkout whose pull was refused would reinstall the
// build that is already there and report it as an update.
func runSelfUpdate(dir string, stdin io.Reader, stdout, stderr io.Writer) error {
	for _, step := range selfUpdateSteps(dir) {
		fmt.Fprintln(stdout, "› "+strings.Join(step, " "))
		cmd := exec.Command(step[0], step[1:]...)
		cmd.Dir = dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", strings.Join(step, " "), err)
		}
	}
	return nil
}

// reopenAfterUpdate replaces the current process with a fresh, argument-less
// `ai` — the TUI launch, since stdin is still whatever terminal asked for the
// update. A package variable so tests can stub it out: syscall.Exec replaces
// the entire process image, and letting the real one run inside `go test`
// would replace the test binary itself, not something to hand-wave past.
var reopenAfterUpdate = func() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, []string{exe}, os.Environ())
}

// performSelfUpdate runs the update and, only on success, reopens ai — so the
// session that asked for the update ends up running the binary it just
// built, rather than the one already loaded in memory.
func performSelfUpdate(dir string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := runSelfUpdate(dir, stdin, stdout, stderr); err != nil {
		return err
	}
	if err := reopenAfterUpdate(); err != nil {
		return fmt.Errorf("update succeeded but could not reopen ai: %w", err)
	}
	return nil
}

// selfUpdateCommand is `ai self-update`. It rebuilds from the managed clone
// at repoDir by default — cloned and switched to updateBranch first if
// needed. AI_SOURCE_DIR overrides that with an arbitrary checkout instead,
// for testing self-update itself against a branch that isn't pushed yet.
func selfUpdateCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) != 0 {
		return errors.New("usage: ai self-update")
	}
	var dir string
	if override := strings.TrimSpace(os.Getenv(sourceDirEnv)); override != "" {
		resolved, err := validateSourceDir(override)
		if err != nil {
			return err
		}
		dir = resolved
	} else {
		resolved, err := ensureRepo(stdout, stderr)
		if err != nil {
			return err
		}
		dir = resolved
	}
	fmt.Fprintln(stdout, "updating ai from "+dir)
	return performSelfUpdate(dir, os.Stdin, stdout, stderr)
}
