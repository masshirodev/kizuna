package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// transcriptLine renders one Claude Code transcript entry: an assistant turn
// carrying tool calls when calls are given, a user turn otherwise.
func transcriptLine(t *testing.T, cwd string, calls ...map[string]any) string {
	t.Helper()
	entry := map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": "hello"}}
	if len(calls) > 0 {
		var content []any
		for _, input := range calls {
			content = append(content, map[string]any{"type": "tool_use", "name": "Tool", "input": input})
		}
		entry = map[string]any{"type": "assistant", "cwd": cwd, "message": map[string]any{"content": content}}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptActivityReadsTheNewestCWDAndToolCallsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeJSONL(t, path,
		// A tail that starts mid-line is what a long transcript gives.
		`partial":"line"}`,
		transcriptLine(t, "/work"),
		transcriptLine(t, "/work",
			map[string]any{"file_path": "/work/tsundoku/src/app.ts"},
			map[string]any{"command": "cd /work/kumiko && git -C /work/hajime status"},
		),
		transcriptLine(t, "/work/yomu", map[string]any{"path": "lib"}),
		`{"type":"progress","data":"no cwd here"}`,
	)

	activity := readTranscriptActivity(path)
	if activity.cwd != "/work/yomu" {
		t.Fatalf("cwd = %q, want the newest entry's", activity.cwd)
	}
	want := []string{"/work/yomu/lib", "/work/hajime", "/work/kumiko", "/work/tsundoku/src"}
	if !reflect.DeepEqual(activity.hints, want) {
		t.Fatalf("hints = %v, want %v (newest first, relative paths against the call's cwd)", activity.hints, want)
	}
	if missing := readTranscriptActivity(filepath.Join(t.TempDir(), "absent.jsonl")); missing.cwd != "" || missing.hints != nil {
		t.Fatalf("a missing transcript = %+v, want nothing", missing)
	}
}

func TestTranscriptActivityStopsAtTheHintLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	var lines []string
	for index := 0; index < workFolderHintLimit+10; index++ {
		lines = append(lines, transcriptLine(t, "/work", map[string]any{"file_path": "/work/r" + strconv.Itoa(index) + "/f"}))
	}
	writeJSONL(t, path, lines...)
	activity := readTranscriptActivity(path)
	if len(activity.hints) != workFolderHintLimit {
		t.Fatalf("%d hints, want the newest %d", len(activity.hints), workFolderHintLimit)
	}
	if newest := fmt.Sprintf("/work/r%d", workFolderHintLimit+9); activity.hints[0] != newest {
		t.Fatalf("first hint %q, want the newest call %q", activity.hints[0], newest)
	}
}

func TestCommandFoldersReadsOnlyTargetsItCanResolve(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	cases := map[string][]string{
		"cd /a/b && make":                   {"/a/b"},
		"ls; cd \"/with space\"":            {"/with space"},
		"(cd sub) | cat":                    {"/cwd/sub"},
		"cd ~/projects/x":                   {filepath.Join(home, "projects/x")},
		"git -C '/repo' log":                {"/repo"},
		"cd $DIR && cd `pwd` && cd - && ls": nil,
		"abcd /nope && echo cd":             nil,
		"":                                  nil,
	}
	for command, want := range cases {
		if got := commandFolders(command, "/cwd"); !reflect.DeepEqual(got, want) {
			t.Errorf("commandFolders(%q) = %v, want %v", command, got, want)
		}
	}
	if got := absoluteFolder("relative", ""); got != "" {
		t.Errorf("a relative path with no cwd = %q, want nothing", got)
	}
}

func TestResolveWorkPlacePrefersARepositoryAndCountsByCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	tsundoku, kumiko := filepath.Join(base, "tsundoku"), filepath.Join(base, "kumiko")
	gitInit(t, tsundoku)
	gitInit(t, kumiko)
	worktree := filepath.Join(base, "tsundoku-fix")
	if output, err := exec.Command("git", "-C", tsundoku, "worktree", "add", "-q", "-b", "fix", worktree).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, output)
	}
	lookup := readGitCheckout

	if place := resolveWorkPlace(kumiko, []string{tsundoku}, lookup); place.folder != kumiko || place.inferred || place.checkout.repo != "kumiko" {
		t.Fatalf("a cwd inside a repository = %+v, want it trusted as it is", place)
	}

	// Two calls in tsundoku against one look at kumiko's docs: tsundoku, even
	// though kumiko is the newest. A file since deleted still counts.
	hints := []string{kumiko, filepath.Join(tsundoku, "gone", "deeper"), tsundoku}
	place := resolveWorkPlace(base, hints, lookup)
	if place.folder != tsundoku || !place.inferred || place.checkout.repo != "tsundoku" || place.checkout.worktree {
		t.Fatalf("outside any repository = %+v, want tsundoku's main checkout, inferred", place)
	}

	// A tie goes to the newest, and a worktree is a checkout of its own.
	place = resolveWorkPlace(base, []string{worktree, tsundoku}, lookup)
	if place.folder != worktree || !place.checkout.worktree || place.checkout.repo != "tsundoku" {
		t.Fatalf("a tie = %+v, want the newest checkout, tsundoku's worktree", place)
	}

	if place := resolveWorkPlace(base, []string{base, "/does/not/exist/at/all"}, lookup); place.folder != base || place.inferred || place.checkout.repo != "" {
		t.Fatalf("no call inside a repository = %+v, want the cwd standing", place)
	}
}

func TestPeersPlaceAClaudeSessionWhereItWorksNotWhereItStarted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := isolatePeers(t)
	projects := filepath.Join(t.TempDir(), "projects")
	tsundoku, kumiko := filepath.Join(projects, "tsundoku"), filepath.Join(projects, "kumiko")
	gitInit(t, tsundoku)
	gitInit(t, kumiko)

	// A stand-in for `claude agents --json`, which reports the launch folder
	// as the cwd whatever the session has done since.
	bin := t.TempDir()
	profile := Profile{Name: "pro", Provider: "claude", Command: filepath.Join(bin, "claude")}
	byPath := livePeerInstance(t, root, profile, "run-1", projects)
	byCD := livePeerInstance(t, root, profile, "run-2", projects)
	idle := livePeerInstance(t, root, profile, "run-3", projects)
	var agents []map[string]any
	for index, lockDir := range []string{byPath, byCD, idle} {
		pid, err := profileLockPID(lockDir)
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, map[string]any{"pid": pid, "cwd": projects, "sessionId": fmt.Sprintf("s%d", index+1), "status": "busy"})
	}
	listing, _ := json.Marshal(agents)
	script := "#!/bin/sh\ncat <<'EOF'\n" + string(listing) + "\nEOF\n"
	if err := os.WriteFile(profile.Command, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	transcripts := filepath.Join(root, appName, "profiles", "pro", "claude", "projects", "-projects")
	writeJSONL(t, filepath.Join(transcripts, "s1.jsonl"),
		transcriptLine(t, projects, map[string]any{"file_path": filepath.Join(tsundoku, "README.md")}),
		transcriptLine(t, projects, map[string]any{"command": "git -C " + tsundoku + " status"}),
	)
	writeJSONL(t, filepath.Join(transcripts, "s2.jsonl"),
		transcriptLine(t, projects),
		transcriptLine(t, kumiko),
	)
	writeJSONL(t, filepath.Join(transcripts, "s3.jsonl"), transcriptLine(t, projects))

	peers := collectPeers(Config{Profiles: []Profile{profile}})
	byID := map[string]peer{}
	for _, p := range peers {
		byID[p.ID] = p
	}
	if p := byID["pro/run-1"]; p.Repo != "tsundoku" || p.Folder != tsundoku || !p.FolderInferred || p.LaunchFolder != projects {
		t.Fatalf("absolute-path agent = %+v, want tsundoku inferred, launched in %s", p, projects)
	} else if where := p.where(); where != "tsundoku (main checkout, inferred)" {
		t.Fatalf("where = %q, want it marked as inferred", where)
	}
	if p := byID["pro/run-2"]; p.Repo != "kumiko" || p.Folder != kumiko || p.FolderInferred || p.LaunchFolder != projects {
		t.Fatalf("agent that moved = %+v, want kumiko from its cwd, not inferred", p)
	}
	if p := byID["pro/run-3"]; p.Repo != "" || p.Folder != projects || p.LaunchFolder != "" {
		t.Fatalf("agent that stayed = %+v, want the launch folder and no launch_folder repeat", p)
	}
	if kept := peersInRepo(peers, "tsundoku"); len(kept) != 1 || kept[0].ID != "pro/run-1" {
		t.Fatalf("peers in tsundoku = %+v, want the agent working there", kept)
	}
	if kept := peersInRepo(peers, projects); len(kept) != 3 {
		t.Fatalf("peers by launch folder = %d, want all three still found", len(kept))
	}
}
