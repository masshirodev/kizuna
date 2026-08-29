package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractProfileBundleRestoresManifestAndState(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	manifest := profileBundleManifest{
		Version: bundleVersion,
		Profile: Profile{
			Name:        "ka",
			Provider:    "codex",
			Command:     "codex",
			DefaultArgs: []string{"--search"},
			Notes:       "personal account",
		},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeTestTarFile(t, writer, "manifest.json", manifestData)
	writeTestTarFile(t, writer, "state/codex/auth.json", []byte(`{"tokens":"opaque"}`))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	stage := t.TempDir()
	got, err := extractProfileBundle(&archive, stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBundleManifest(got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Profile.DefaultArgs, "|") != "--search" || got.Profile.Notes != "personal account" {
		t.Fatalf("profile metadata was not restored: %+v", got.Profile)
	}
	data, err := os.ReadFile(filepath.Join(stage, "state", "codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"tokens":"opaque"}` {
		t.Fatalf("restored state = %q", data)
	}
}

func TestExtractProfileBundleRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	writeTestTarFile(t, writer, "state/../../outside", []byte("nope"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := extractProfileBundle(&archive, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe bundle path") {
		t.Fatalf("traversal error = %v", err)
	}
}

func TestWriteProfileBundleSkipsCodexRuntimeTemp(t *testing.T) {
	workdir := t.TempDir()
	authPath := filepath.Join(workdir, "codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"tokens":"opaque"}`), 0600); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(workdir, "codex", "tmp", "arg0", "codex-test")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/true", filepath.Join(runtimeDir, "apply_patch")); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	profile := Profile{Name: "ka", Provider: "codex", Command: "codex"}
	if err := writeProfileBundle(&archive, profile, workdir); err != nil {
		t.Fatal(err)
	}

	reader := tar.NewReader(&archive)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, "state/codex/auth.json") {
		t.Fatalf("archive omitted persistent auth state:\n%s", joined)
	}
	if strings.Contains(joined, "state/codex/tmp") {
		t.Fatalf("archive included Codex runtime temp state:\n%s", joined)
	}
}

func TestWriteProfileBundleSkipsOpenCodeNodeModules(t *testing.T) {
	workdir := t.TempDir()
	configPath := filepath.Join(workdir, "config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"plugin": ["example"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	nodeModules := filepath.Join(workdir, "config", "opencode", "node_modules")
	if err := os.MkdirAll(filepath.Join(nodeModules, ".bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../example/bin.js", filepath.Join(nodeModules, ".bin", "example")); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	if err := writeProfileBundle(&archive, profile, workdir); err != nil {
		t.Fatal(err)
	}

	reader := tar.NewReader(&archive)
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, "state/config/opencode/opencode.jsonc") {
		t.Fatalf("archive omitted OpenCode configuration:\n%s", joined)
	}
	if strings.Contains(joined, "node_modules") {
		t.Fatalf("archive included OpenCode dependencies:\n%s", joined)
	}
}

func TestWriteProfileBundleSkipsRuntimeInstances(t *testing.T) {
	workdir := t.TempDir()
	authPath := filepath.Join(workdir, "claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"oauth":"opaque"}`), 0600); err != nil {
		t.Fatal(err)
	}
	instancePath := filepath.Join(workdir, instancesDirectory, "run-test", ".active.lock")
	if err := os.MkdirAll(filepath.Dir(instancePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instancePath, []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	if err := writeProfileBundle(&archive, profile, workdir); err != nil {
		t.Fatal(err)
	}

	reader := tar.NewReader(&archive)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(header.Name, "/"+instancesDirectory) {
			t.Fatalf("archive included runtime instance entry %q", header.Name)
		}
	}
}

func writeTestTarFile(t *testing.T, writer *tar.Writer, name string, data []byte) {
	t.Helper()
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
}
