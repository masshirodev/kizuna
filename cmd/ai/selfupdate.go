package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// sourceFile records the checkout this build came from. The update check can
	// say a binary is behind without it — the commit is stamped into the binary —
	// but applying the update needs the source, and no Go binary carries the
	// directory it was built in.
	sourceFile = "source"
	// sourceDirEnv overrides the recorded checkout for one invocation.
	sourceDirEnv = "AI_SOURCE_DIR"
)

// errNoSourceDir names the fix rather than only the problem: a binary installed
// with a bare `go install` never passed through install.sh and so has nothing
// recorded.
var errNoSourceDir = errors.New(
	"this build's checkout is unknown; run 'ai self-update --source <path to the ai-session checkout>' once, or set " + sourceDirEnv)

func sourcePath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName, sourceFile), nil
}

// The path is stored as one plain line rather than JSON so install.sh can write
// it with a printf, without having to escape a directory name into a string
// literal.
func recordSourceDir(dir string) error {
	path, err := sourcePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(dir+"\n"), 0600)
}

func sourceDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(sourceDirEnv)); dir != "" {
		return validateSourceDir(dir)
	}
	path, err := sourcePath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errNoSourceDir
	}
	dir := strings.TrimSpace(string(data))
	if dir == "" {
		return "", errNoSourceDir
	}
	return validateSourceDir(dir)
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

// selfUpdateCommand is `ai self-update`. Passing --source both records the
// checkout for later and uses it now, so the one-time setup and the update it
// enables are the same command.
func selfUpdateCommand(args []string, stdout, stderr io.Writer) error {
	var dir string
	switch {
	case len(args) == 0:
		resolved, err := sourceDir()
		if err != nil {
			return err
		}
		dir = resolved
	case len(args) == 2 && args[0] == "--source":
		resolved, err := validateSourceDir(args[1])
		if err != nil {
			return err
		}
		if err := recordSourceDir(resolved); err != nil {
			return err
		}
		dir = resolved
	default:
		return errors.New("usage: ai self-update [--source <checkout>]")
	}
	fmt.Fprintln(stdout, "updating ai from "+dir)
	return runSelfUpdate(dir, os.Stdin, stdout, stderr)
}
