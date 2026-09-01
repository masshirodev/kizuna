package main

import (
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

func TestSourceDirReadsWhatInstallRecorded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	checkout := fakeCheckout(t)
	if err := recordSourceDir(checkout); err != nil {
		t.Fatal(err)
	}
	got, err := sourceDir()
	if err != nil || got != checkout {
		t.Fatalf("sourceDir() = %q, %v; want %q", got, err, checkout)
	}
	path, err := sourcePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("recorded source mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSourceDirPrefersTheEnvironmentOverTheRecordedPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := recordSourceDir(fakeCheckout(t)); err != nil {
		t.Fatal(err)
	}
	override := fakeCheckout(t)
	t.Setenv(sourceDirEnv, override)
	got, err := sourceDir()
	if err != nil || got != override {
		t.Fatalf("sourceDir() = %q, %v; want the override %q", got, err, override)
	}
}

// The update runs git pull and a build in whatever it is handed, so a directory
// that is not a checkout has to be refused here rather than there.
func TestSourceDirRefusesADirectoryThatIsNotACheckout(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(sourceDirEnv, t.TempDir())
	if _, err := sourceDir(); err == nil || !strings.Contains(err.Error(), "not an ai-session checkout") {
		t.Fatalf("sourceDir() = %v, want a refusal naming the missing marker", err)
	}
}

func TestSourceDirNamesTheFixWhenNothingIsRecorded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := sourceDir()
	if err == nil || !strings.Contains(err.Error(), "self-update --source") {
		t.Fatalf("sourceDir() = %v, want an error naming the one-time fix", err)
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

// stubGit puts a git on PATH that records its arguments and exits with code.
func stubGit(t *testing.T, code int) string {
	t.Helper()
	binDir := t.TempDir()
	log := filepath.Join(binDir, "git.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit %d\n", log, code)
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	scopePATH(t, binDir)
	return log
}

func TestRunSelfUpdatePullsThenRebuilds(t *testing.T) {
	dir := fakeCheckout(t)
	built := filepath.Join(dir, "built")
	if err := os.WriteFile(filepath.Join(dir, "install.sh"),
		[]byte("#!/bin/sh\ntouch "+built+"\necho rebuilt\n"), 0755); err != nil {
		t.Fatal(err)
	}
	log := stubGit(t, 0)

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
	stubGit(t, 1)

	var out strings.Builder
	err := runSelfUpdate(dir, strings.NewReader(""), &out, &out)
	if err == nil || !strings.Contains(err.Error(), "git pull --ff-only failed") {
		t.Fatalf("err = %v, want the failing step named", err)
	}
	if _, err := os.Stat(built); err == nil {
		t.Fatal("the rebuild ran after the pull failed")
	}
}

func TestSelfUpdateCommandRecordsTheCheckoutItWasGiven(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := fakeCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	stubGit(t, 0)

	var out strings.Builder
	if err := selfUpdateCommand([]string{"--source", dir}, &out, &out); err != nil {
		t.Fatal(err)
	}
	// The one-time setup and the update it enables are the same command, so the
	// next run must not need --source again.
	recorded, err := sourceDir()
	if err != nil || recorded != dir {
		t.Fatalf("sourceDir() after --source = %q, %v; want %q", recorded, err, dir)
	}
	if err := selfUpdateCommand([]string{"bogus"}, &out, &out); err == nil {
		t.Fatal("an unrecognised argument was accepted")
	}
}
