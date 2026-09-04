package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCheckout is a directory that passes validateSourceDir: the two markers it
// looks for, and nothing else.
func fakeCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubGit puts a git on PATH that records its arguments, answers
// `rev-parse --abbrev-ref HEAD` with branch, creates the target directory on
// `clone` (so a later command run with that directory as its cwd doesn't
// fail against a directory that was never really created), and otherwise
// just exits with code.
func stubGit(t *testing.T, code int, branch string) string {
	t.Helper()
	binDir := t.TempDir()
	log := filepath.Join(binDir, "git.log")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + log + "\n" +
		"if [ \"$1\" = clone ]; then\n" +
		"  eval \"target=\\$$#\"\n" +
		"  mkdir -p \"$target\"\n" +
		"fi\n" +
		"if [ \"$1\" = rev-parse ]; then\n" +
		"  echo " + branch + "\n" +
		"fi\n" +
		"exit " + fmt.Sprint(code) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	scopePATH(t, binDir)
	return log
}

// stubReopenAfterUpdate replaces the package-level reopenAfterUpdate for the
// duration of the test, so nothing here can let a real syscall.Exec replace
// the go test binary itself.
func stubReopenAfterUpdate(t *testing.T, fn func() error) {
	t.Helper()
	original := reopenAfterUpdate
	reopenAfterUpdate = fn
	t.Cleanup(func() { reopenAfterUpdate = original })
}

// The update runs git pull and a build in whatever it is handed, so a directory
// that is not a checkout has to be refused here rather than there.
func TestValidateSourceDirRefusesADirectoryThatIsNotACheckout(t *testing.T) {
	if _, err := validateSourceDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not an ai-session checkout") {
		t.Fatalf("validateSourceDir() = %v, want a refusal naming the missing marker", err)
	}
}

func TestPreviewUpdateDirPrefersTheEnvironmentOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	override := fakeCheckout(t)
	t.Setenv(sourceDirEnv, override)
	got, err := previewUpdateDir()
	if err != nil || got != override {
		t.Fatalf("previewUpdateDir() = %q, %v; want the override %q", got, err, override)
	}
}

func TestPreviewUpdateDirDefaultsToTheManagedClonePathWithoutCreatingIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	got, err := previewUpdateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "ai", "repo")
	if got != want {
		t.Fatalf("previewUpdateDir() = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatal("previewUpdateDir must not have side effects; the clone should not exist yet")
	}
}

func TestEnsureRepoClonesWhenMissingAndLeavesAFreshCloneOnTheDefaultBranchAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	log := stubGit(t, 0, updateBranch)

	var out strings.Builder
	dir, err := ensureRepo(&out, &out)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "ai", "repo")
	if dir != want {
		t.Fatalf("ensureRepo() dir = %q, want %q", dir, want)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "clone git@github.com:"+updateRepo+".git "+want) {
		t.Fatalf("git was not asked to clone: %q", calls)
	}
	if strings.Contains(string(calls), "checkout") {
		t.Fatalf("a fresh clone already on the default branch should not be switched: %q", calls)
	}
}

func TestEnsureRepoSwitchesAnExistingCloneBackToTheDefaultBranch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "ai", "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	log := stubGit(t, 0, "some-other-branch")

	var out strings.Builder
	if _, err := ensureRepo(&out, &out); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "checkout "+updateBranch) {
		t.Fatalf("git was not asked to switch back to %s: %q", updateBranch, calls)
	}
	if strings.Contains(string(calls), "clone") {
		t.Fatalf("an existing checkout should not be re-cloned: %q", calls)
	}
}

func TestEnsureRepoRefusesADirectoryThatIsNotAGitCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	if err := os.MkdirAll(filepath.Join(root, "ai", "repo"), 0700); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if _, err := ensureRepo(&out, &out); err == nil || !strings.Contains(err.Error(), "is not a git checkout") {
		t.Fatalf("ensureRepo() = %v, want a refusal naming the problem", err)
	}
}

// install.sh is preferred so there is one definition of how ai is built,
// including the VCS stamp the update check itself depends on.
func TestSelfUpdateStepsPreferTheCheckoutsOwnInstallScript(t *testing.T) {
	dir := fakeCheckout(t)
	steps := selfUpdateSteps(dir)
	if len(steps) != 2 || strings.Join(steps[0], " ") != "git pull --ff-only" {
		t.Fatalf("steps = %v, want a fast-forward pull first", steps)
	}
	if strings.Join(steps[1], " ") != "go install ./cmd/ai" {
		t.Fatalf("without install.sh, step 2 = %v", steps[1])
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if steps := selfUpdateSteps(dir); strings.Join(steps[1], " ") != "sh ./install.sh" {
		t.Fatalf("with install.sh, step 2 = %v", steps[1])
	}
}

func TestRunSelfUpdatePullsThenRebuilds(t *testing.T) {
	dir := fakeCheckout(t)
	built := filepath.Join(dir, "built")
	if err := os.WriteFile(filepath.Join(dir, "install.sh"),
		[]byte("#!/bin/sh\ntouch "+built+"\necho rebuilt\n"), 0755); err != nil {
		t.Fatal(err)
	}
	log := stubGit(t, 0, updateBranch)

	var out strings.Builder
	if err := runSelfUpdate(dir, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	pulled, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(pulled)) != "pull --ff-only" {
		t.Fatalf("git was called as %q, want a fast-forward-only pull", pulled)
	}
	if _, err := os.Stat(built); err != nil {
		t.Fatalf("the rebuild step did not run: %v", err)
	}
	if !strings.Contains(out.String(), "› git pull --ff-only") {
		t.Fatalf("the steps were not echoed:\n%s", out.String())
	}
}

// Rebuilding a checkout whose pull was refused would reinstall the build that is
// already there and report it as an update.
func TestRunSelfUpdateStopsWhenThePullFails(t *testing.T) {
	dir := fakeCheckout(t)
	built := filepath.Join(dir, "built")
	if err := os.WriteFile(filepath.Join(dir, "install.sh"),
		[]byte("#!/bin/sh\ntouch "+built+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	stubGit(t, 1, updateBranch)

	var out strings.Builder
	err := runSelfUpdate(dir, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "git pull --ff-only failed") {
		t.Fatalf("err = %v, want the failing step named", err)
	}
	if _, err := os.Stat(built); err == nil {
		t.Fatal("the rebuild ran after the pull failed")
	}
}

func TestPerformSelfUpdateReopensOnlyOnSuccess(t *testing.T) {
	dir := fakeCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	calls := 0
	stubReopenAfterUpdate(t, func() error { calls++; return nil })

	stubGit(t, 1, updateBranch)
	var out strings.Builder
	if err := performSelfUpdate(dir, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected the failing pull to be reported")
	}
	if calls != 0 {
		t.Fatalf("reopenAfterUpdate was called after a failed update: %d calls", calls)
	}

	stubGit(t, 0, updateBranch)
	if err := performSelfUpdate(dir, strings.NewReader(""), &out, &out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("reopenAfterUpdate was not called after a successful update: %d calls", calls)
	}
}

func TestPerformSelfUpdateWrapsAReopenFailure(t *testing.T) {
	dir := fakeCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	stubGit(t, 0, updateBranch)
	stubReopenAfterUpdate(t, func() error { return errors.New("boom") })

	var out strings.Builder
	err := performSelfUpdate(dir, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "could not reopen ai") {
		t.Fatalf("performSelfUpdate() = %v, want the reopen failure named", err)
	}
}

func TestSelfUpdateCommandUsesTheEnvironmentOverrideWithoutTouchingTheManagedClone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := fakeCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sourceDirEnv, dir)
	stubGit(t, 0, updateBranch)
	called := false
	stubReopenAfterUpdate(t, func() error { called = true; return nil })

	var out strings.Builder
	if err := selfUpdateCommand(nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("performSelfUpdate did not reopen ai on success")
	}
	if !strings.Contains(out.String(), "updating ai from "+dir) {
		t.Fatalf("output did not name the override checkout: %q", out.String())
	}
}

func TestSelfUpdateCommandRejectsArguments(t *testing.T) {
	var out strings.Builder
	if err := selfUpdateCommand([]string{"bogus"}, &out, &out); err == nil {
		t.Fatal("an unrecognised argument was accepted")
	}
}
