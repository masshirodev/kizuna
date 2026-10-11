package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// nativeClaude lays out the native installer's shape under home: versioned
// binaries, and ~/.local/bin/claude a symlink to the current one.
func nativeClaude(t *testing.T, home string, versions ...string) string {
	t.Helper()
	for _, version := range versions {
		writeExecutable(t, filepath.Join(home, ".local", "share", "claude", "versions", version), "#!/bin/sh\n")
	}
	link := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".local", "share", "claude", "versions", versions[0]), link); err != nil {
		t.Fatal(err)
	}
	return link
}

func retarget(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestFindInstallsListsPathOrderThenKnownLocationsOnce(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	alias := filepath.Join(root, "alias")
	writeExecutable(t, filepath.Join(sys, "claude"), "#!/bin/sh\n")
	// alias is the /bin -> /usr/bin case: a second PATH entry, the same file.
	if err := os.Symlink(sys, alias); err != nil {
		t.Fatal(err)
	}
	native := nativeClaude(t, root, "2")

	installs := findInstalls("claude", strings.Join([]string{sys, alias}, string(os.PathListSeparator)), []string{native})
	if len(installs) != 2 {
		t.Fatalf("findInstalls = %+v, want the package copy and the native one", installs)
	}
	if installs[0].path != filepath.Join(sys, "claude") || installs[1].path != native {
		t.Errorf("findInstalls order = %s, %s", installs[0].path, installs[1].path)
	}
}

func TestShadowWarningNamesTheInstallThatRunsWhenAnotherOneMoved(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	writeExecutable(t, filepath.Join(sys, "claude"), "#!/bin/sh\n")
	native := nativeClaude(t, root, "1", "2")
	find := func() []binaryInstall { return findInstalls("claude", sys, []string{native}) }

	before := find()
	retarget(t, native, filepath.Join(root, ".local", "share", "claude", "versions", "2"))
	warning := shadowWarning("claude", before, find())
	if !strings.Contains(warning, "changed "+native) || !strings.Contains(warning, "runs "+filepath.Join(sys, "claude")) {
		t.Errorf("shadowWarning = %q", warning)
	}
}

func TestShadowWarningPointsAtAShadowedInstallEvenWhenNothingMoved(t *testing.T) {
	// The second press of u: the native install is already current, so the
	// update changes nothing, and the old copy still runs.
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	writeExecutable(t, filepath.Join(sys, "claude"), "#!/bin/sh\n")
	native := nativeClaude(t, root, "2")
	installs := findInstalls("claude", sys, []string{native})

	warning := shadowWarning("claude", installs, installs)
	if !strings.Contains(warning, "shadowed") || !strings.Contains(warning, native) {
		t.Errorf("shadowWarning = %q", warning)
	}
}

func TestShadowWarningIsQuietWhenTheRunningInstallIsTheOneUpdated(t *testing.T) {
	root := t.TempDir()
	native := nativeClaude(t, root, "1", "2")
	sys := filepath.Join(root, "sys")
	writeExecutable(t, filepath.Join(sys, "claude"), "#!/bin/sh\n")
	path := strings.Join([]string{filepath.Dir(native), sys}, string(os.PathListSeparator))
	find := func() []binaryInstall { return findInstalls("claude", path, []string{native}) }

	before := find()
	retarget(t, native, filepath.Join(root, ".local", "share", "claude", "versions", "2"))
	if warning := shadowWarning("claude", before, find()); warning != "" {
		t.Errorf("shadowWarning = %q, want nothing: the install that runs is the one that moved", warning)
	}

	single := findInstalls("claude", sys, nil)
	if warning := shadowWarning("claude", single, single); warning != "" {
		t.Errorf("shadowWarning with one install = %q, want nothing", warning)
	}
}

// The work-PC case end to end: a package copy on PATH whose `update` refreshes
// the native install, which is not on PATH at all.
func TestLaunchUpdateWarnsWhenTheUpdateMissedTheInstallThatRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	native := nativeClaude(t, home, "1", "2")
	sys := filepath.Join(home, "sys")
	writeExecutable(t, filepath.Join(sys, "claude"),
		"#!/bin/sh\nrm \""+native+"\" && ln -s \""+filepath.Join(home, ".local", "share", "claude", "versions", "2")+"\" \""+native+"\"\n")
	scopePATH(t, sys)

	var stdout, stderr bytes.Buffer
	if err := launchUpdate(Profile{Name: "claude", Provider: "claude", Command: "claude"}, &stdout, &stderr); err != nil {
		t.Fatalf("launchUpdate = %v (stderr %q)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "warning: the update changed ~/.local/bin/claude") {
		t.Errorf("stderr = %q, want the shadow warning", stderr.String())
	}
}
