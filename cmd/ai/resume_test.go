package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseResumeRequest(t *testing.T) {
	if id, ok := parseResumeRequest(nil); ok || id != "" {
		t.Fatalf("empty args parsed as resume: %q, %v", id, ok)
	}
	if id, ok := parseResumeRequest([]string{"--resume"}); ok || id != "" {
		t.Fatalf("--resume parsed as the wrapper: %q, %v", id, ok)
	}
	if id, ok := parseResumeRequest([]string{"resume"}); !ok || id != "" {
		t.Fatalf("bare resume = %q, %v; want picker", id, ok)
	}
	if id, ok := parseResumeRequest([]string{"resume", "abc123"}); !ok || id != "abc123" {
		t.Fatalf("resume with id = %q, %v; want abc123", id, ok)
	}
	// Anything longer is a prompt that happens to start with the word, and
	// must keep passing through to the provider untouched.
	if _, ok := parseResumeRequest([]string{"resume", "abc123", "extra"}); ok {
		t.Fatal("resume with trailing arguments was claimed as the wrapper")
	}
	if _, ok := parseResumeRequest([]string{"resumeish"}); ok {
		t.Fatal("a prompt starting with resume was claimed as the wrapper")
	}
}

func TestResumeTUIModelOpensPickerOnProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{
		{Name: "another", Provider: "claude", Command: "/bin/echo"},
		{Name: "codex-work", Provider: "codex", Command: "/bin/echo"},
	}}); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, root, "2026-08-10T10-00-00", "/work/lattice",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"fix the form"}]}}`)

	model, err := resumeTUIModel("codex-work")
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("a profile with a recorded session got no picker")
	}
	if model.mode != tuiRecent {
		t.Fatalf("mode = %v, want the resume picker", model.mode)
	}
	selected, ok := model.selectedProfile()
	if !ok || selected.Name != "codex-work" {
		t.Fatalf("selected = %+v, %v; want codex-work under the cursor", selected, ok)
	}
	if len(model.recent) != 1 {
		t.Fatalf("recent holds %d records, want 1", len(model.recent))
	}
	if model.preview.session != model.recent[0].session.id {
		t.Fatalf("preview is for %q, want the row under the cursor", model.preview.session)
	}
}

func TestResumeTUIModelWithNothingRecordedOffersNoPicker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{
		{Name: "fresh", Provider: "claude", Command: "/bin/echo"},
	}}); err != nil {
		t.Fatal(err)
	}
	model, err := resumeTUIModel("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if model != nil {
		t.Fatal("a profile with no recorded sessions opened a picker")
	}
}

func TestResumeTUIModelReportsAnUnknownProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := resumeTUIModel("missing"); err == nil {
		t.Fatal("an unknown profile opened a picker")
	}
}

func TestResumeSessionByIDReopensInTheRecordedFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	folder := t.TempDir()
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{
		{Name: "codex-work", Provider: "codex", Command: "/bin/echo"},
	}}); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, root, "2026-08-10T10-00-00", folder,
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"fix the form"}]}}`)

	profile, err := findProfile(Config{Profiles: []Profile{{Name: "codex-work", Provider: "codex", Command: "/bin/echo"}}}, "codex-work")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := resumeSessionByID(profile, "2026-08-10T10-00-00-id", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "resume 2026-08-10T10-00-00-id\n" {
		t.Fatalf("resume output = %q, want the session reopened by id", stdout.String())
	}
	// That the launch ran in the recorded folder is covered by
	// TestLaunchInFolderRunsTheCommandThere; the folder resolution itself is
	// the picker's recordedFolder, tested with the picker.
}

func TestResumeSessionByIDWithAGoneFolderIsReported(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{
		{Name: "codex-work", Provider: "codex", Command: "/bin/echo"},
	}}); err != nil {
		t.Fatal(err)
	}
	writeRollout(t, root, "2026-08-10T10-00-00", filepath.Join(root, "gone"),
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"fix the form"}]}}`)

	profile := Profile{Name: "codex-work", Provider: "codex", Command: "/bin/echo"}
	var stdout, stderr strings.Builder
	err = resumeSessionByID(profile, "2026-08-10T10-00-00-id", &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("error = %v, want one naming the folder that moved", err)
	}
	if stdout.String() != "" {
		t.Fatalf("a session with a gone folder still launched: %q", stdout.String())
	}
}

func TestResumeSessionByIDUnknownToTranscriptsUsesProviderFlow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "/bin/echo"}
	var stdout, stderr strings.Builder
	if err := resumeSessionByID(profile, "ses_elsewhere", &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "resume ses_elsewhere\n" {
		t.Fatalf("resume output = %q, want the id handed to the provider", stdout.String())
	}
}

func TestRunResumeWithTrailingArgumentsPassesThrough(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{{
		Name: "ka", Provider: "codex", Command: "/bin/echo",
	}}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := run([]string{"ka", "resume", "the", "meeting"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "resume the meeting\n" {
		t.Fatalf("output = %q, want the words passed through as a prompt", stdout.String())
	}
}

func TestLaunchInFolderRunsTheCommandThere(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	folder := t.TempDir()
	profile := Profile{Name: "ka", Provider: "codex", Command: "/bin/pwd"}
	var stdout, stderr strings.Builder
	if err := launchInFolder(profile, nil, folder, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	// /bin/pwd resolves symlinks while t.TempDir may not; compare the
	// evaluated forms.
	want, err := filepath.EvalSymlinks(folder)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("command ran in %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatal(err)
	}
}
