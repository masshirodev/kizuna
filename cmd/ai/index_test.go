package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func claudeTranscriptPath(root, profile, project, session string) string {
	return filepath.Join(root, appName, "profiles", profile, "claude", "projects", project, session+".jsonl")
}

// rewriteKeepingFingerprint replaces a file's content with the same number of
// bytes and puts its modification time back, so only a reader that ignores
// the fingerprint would see the change.
func rewriteKeepingFingerprint(t *testing.T, path, from, to string) {
	t.Helper()
	if len(from) != len(to) {
		t.Fatal("rewrite must keep the size")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), from, to, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
}

func TestIndexReadsASourceOnlyWhenItsFingerprintMoves(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	writeTranscript(t, root, "cl", "-work", "ses-1", "/work", "2026-09-28T10:00:00.000Z", "first title")
	path := claudeTranscriptPath(root, "cl", "-work", "ses-1")

	if got := recentSessions(profile, 0); len(got) != 1 || got[0].session.title != "first title" {
		t.Fatalf("first read = %+v", got)
	}
	// Same size, same mtime: the index must not re-read it.
	rewriteKeepingFingerprint(t, path, "first title", "other title")
	if got := recentSessions(profile, 0); got[0].session.title != "first title" {
		t.Fatalf("an unchanged fingerprint was re-read: %q", got[0].session.title)
	}
	// Moving the mtime is a change, and it is read.
	later := time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if got := recentSessions(profile, 0); got[0].session.title != "other title" {
		t.Fatalf("a moved fingerprint was not re-read: %q", got[0].session.title)
	}
	// A deleted source leaves the index.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := recentSessions(profile, 0); len(got) != 0 {
		t.Fatalf("a deleted transcript is still listed: %+v", got)
	}
}

func TestDeletingTheIndexRebuildsTheSameList(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	writeTranscript(t, root, "cl", "-a", "ses-1", "/a", "2026-09-28T10:00:00.000Z", "one")
	writeTranscript(t, root, "cl", "-b", "ses-2", "/b", "2026-09-28T11:00:00.000Z", "two")
	before := recentSessions(profile, 0)
	path, err := sessionIndexPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
	after := recentSessions(profile, 0)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("before %d rows, after %d, want 2 each", len(before), len(after))
	}
	for index := range before {
		if before[index].session != after[index].session || before[index].folder != after[index].folder ||
			!before[index].activity().Equal(after[index].activity()) {
			t.Fatalf("row %d differs after a rebuild: %+v vs %+v", index, before[index], after[index])
		}
	}
}

func TestIndexFallsBackToScanningWhenItCannotOpen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	writeTranscript(t, root, "cl", "-a", "ses-1", "/a", "2026-09-28T10:00:00.000Z", "one")
	path, err := sessionIndexPath()
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the database should be: it cannot open.
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got := recentSessions(profile, 0); len(got) != 1 || got[0].session.title != "one" {
		t.Fatalf("scan fallback = %+v", got)
	}
}

// A night of headless runs must not push the conversations a person had out
// of a list that then hides the runs.
func TestHeadlessOpenCodeSessionsAreMarkedAndLimitedSeparately(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "oc", Provider: "opencode", Command: "opencode"}
	workdir := profileWorkdir(t, root, profile.Name)
	headless := `'[{"permission":"question","pattern":"*","action":"deny"}]'`
	writeRawDB(t, filepath.Join(workdir, "data", "opencode", "opencode.db"),
		`CREATE TABLE session (id text PRIMARY KEY, title text NOT NULL, directory text NOT NULL,
			time_created integer NOT NULL, time_updated integer NOT NULL, permission text)`,
		`INSERT INTO session VALUES ('ses-talk', 'a conversation', '/work', 1000, 1000, NULL)`,
		`INSERT INTO session VALUES ('ses-w1', 'agent/w1-a', '/work', 2000, 2000, `+headless+`)`,
		`INSERT INTO session VALUES ('ses-w2', 'agent/w1-b', '/work', 3000, 3000, `+headless+`)`,
		`INSERT INTO session VALUES ('ses-w3', 'agent/w1-c', '/work', 4000, 4000, `+headless+`)`)

	got := recentSessions(profile, 1)
	if len(got) != 2 {
		t.Fatalf("recent(limit 1) = %+v, want the newest of each kind", got)
	}
	byID := map[string]recordedSession{}
	for _, record := range got {
		byID[record.session.id] = record
	}
	if talk, ok := byID["ses-talk"]; !ok || talk.headless {
		t.Fatalf("the conversation is missing or marked headless: %+v", got)
	}
	if run, ok := byID["ses-w3"]; !ok || !run.headless {
		t.Fatalf("the newest run is missing or not marked headless: %+v", got)
	}
}

func TestHeadlessClaudeTranscriptIsTheSDKEntrypoint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	writeTranscriptLines(t, root, "cl", "-a", "ses-cli",
		`{"type":"user","entrypoint":"cli","cwd":"/a","timestamp":"2026-09-28T10:00:00.000Z","message":{"content":[{"type":"text","text":"typed"}]}}`)
	writeTranscriptLines(t, root, "cl", "-a", "ses-sdk",
		`{"type":"user","entrypoint":"sdk-cli","cwd":"/a","timestamp":"2026-09-28T11:00:00.000Z","message":{"content":[{"type":"text","text":"piped"}]}}`)
	records := recentSessions(profile, 0)
	if len(records) != 2 {
		t.Fatalf("recent = %+v, want both transcripts", records)
	}
	for _, record := range records {
		if want := record.session.id == "ses-sdk"; record.headless != want {
			t.Fatalf("%s headless = %v, want %v", record.session.id, record.headless, want)
		}
	}
}

func TestReadingAConversationWholeCachesItsTurns(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "cl", Provider: "claude", Command: "claude"}
	writeTranscriptLines(t, root, "cl", "-a", "ses-1",
		`{"type":"user","cwd":"/a","timestamp":"2026-09-28T10:00:00.000Z","message":{"content":[{"type":"text","text":"ask one"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"answer one"}]}}`)
	path := claudeTranscriptPath(root, "cl", "-a", "ses-1")
	records := recentSessions(profile, 0)
	if len(records) != 1 {
		t.Fatalf("recent = %+v", records)
	}
	first, _, err := readSessionMessages(profile, records[0])
	if err != nil || len(first) != 2 {
		t.Fatalf("first read = %+v, %v", first, err)
	}
	// Unchanged fingerprint: the second read is the cache, not the file.
	rewriteKeepingFingerprint(t, path, "answer one", "answer two")
	second, _, err := readSessionMessages(profile, records[0])
	if err != nil || second[1].text != "answer one" {
		t.Fatalf("second read = %+v, %v; want the cached turns", second, err)
	}
	if preview := readSessionPreview(profile, records[0]); len(preview.messages) != 2 || preview.messages[1].text != "answer one" {
		t.Fatalf("preview did not read the cached turns: %+v", preview)
	}
	// Once the transcript moves on, the cache is stale and the file is read.
	later := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	records = recentSessions(profile, 0)
	third, _, err := readSessionMessages(profile, records[0])
	if err != nil || third[1].text != "answer two" {
		t.Fatalf("third read = %+v, %v; want the file re-read", third, err)
	}
}

func TestPickerHidesHeadlessUntilDotShowsThem(t *testing.T) {
	m := tuiModel{profiles: testProfiles(), mode: tuiRecent, width: 140, height: 32, recent: []recordedSession{
		{session: instanceSession{id: "ses-talk", title: "a conversation"}, folder: "/work"},
		{session: instanceSession{id: "ses-run", title: "agent/w1-a"}, folder: "/work", headless: true},
	}}
	if got := m.visibleRecent(); len(got) != 1 || got[0].session.id != "ses-talk" {
		t.Fatalf("visible by default = %+v, want the conversation only", got)
	}
	if list := strings.Join(m.pickerList(80, 12), "\n"); !strings.Contains(list, "1 headless hidden, . shows") {
		t.Fatalf("the list does not say what it hides:\n%s", list)
	}
	updated, _ := m.updateRecent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")})
	m = updated.(tuiModel)
	if got := m.visibleRecent(); len(got) != 2 {
		t.Fatalf("visible after . = %+v, want both", got)
	}
	if list := strings.Join(m.pickerList(80, 12), "\n"); !strings.Contains(list, "headless shown, . hides") {
		t.Fatalf("the list does not say headless is shown:\n%s", list)
	}
}

func TestAPickerOfOnlyHeadlessRunsSaysSo(t *testing.T) {
	m := tuiModel{profiles: testProfiles(), mode: tuiRecent, width: 140, height: 32, recent: []recordedSession{
		{session: instanceSession{id: "ses-run", title: "agent/w1-a"}, folder: "/work", headless: true},
	}}
	if list := strings.Join(m.pickerList(80, 12), "\n"); !strings.Contains(list, "Only headless runs recorded here") {
		t.Fatalf("an all-headless picker looks empty:\n%s", list)
	}
}
