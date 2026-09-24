package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadSessionPreviewReadsTheConversationInOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeTranscriptLines(t, root, "claude-personal", "-work-hub", "sid",
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-09-04T11:30:00Z","message":{"content":[{"type":"text","text":"add a preview pane"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"reading the picker first"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"continue"}]}}`,
	)

	preview := readSessionPreview(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "sid"}})
	if preview.problem != "" {
		t.Fatalf("problem = %q, want the transcript read", preview.problem)
	}
	if len(preview.messages) != 3 || preview.earlier {
		t.Fatalf("preview = %+v, want three turns and the whole conversation", preview.messages)
	}
	if !preview.messages[0].fromUser || preview.messages[0].text != "add a preview pane" {
		t.Fatalf("first turn = %+v, want what was asked", preview.messages[0])
	}
	if preview.messages[1].fromUser || preview.messages[1].text != "reading the picker first" {
		t.Fatalf("second turn = %+v, want the model's answer", preview.messages[1])
	}
}

// The pane shows how a conversation ended, not how it opened: a session opened
// with a pasted brief names nothing at the top, while what was last being done
// is what distinguishes it now.
func TestReadSessionPreviewShowsTheEndNotTheOpening(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	lines := []string{`{"type":"user","cwd":"/work/hub","timestamp":"2026-09-04T11:30:00Z","message":{"content":[{"type":"text","text":"the opening question"}]}}`}
	for index := 0; index < previewTurns+5; index++ {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"a filler turn"}]}}`)
	}
	lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"the final answer"}]}}`)
	writeTranscriptLines(t, root, "claude-personal", "-work-hub", "sid", lines...)

	preview := readSessionPreview(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "sid"}})
	if !preview.earlier {
		t.Fatal("the preview did not mark that the conversation carries on above it")
	}
	if got := preview.messages[len(preview.messages)-1].text; got != "the final answer" {
		t.Fatalf("last turn = %q, want the end of the conversation", got)
	}
	if preview.messages[0].text == "the opening question" {
		t.Fatal("the preview still starts at the opening")
	}
}

// A pane that stopped at its bound and one that reached the start of a
// conversation look the same on screen unless the read says which it was.
func TestReadSessionPreviewMarksAConversationItOnlyShowsTheEndOf(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	lines := []string{`{"type":"user","cwd":"/work/hub","timestamp":"2026-09-04T11:30:00Z","message":{"content":[{"type":"text","text":"first"}]}}`}
	for range previewTurns + 4 {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"and then"}]}}`)
	}
	writeTranscriptLines(t, root, "claude-personal", "-work-hub", "sid", lines...)

	preview := readSessionPreview(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "sid"}})
	if len(preview.messages) != previewTurns {
		t.Fatalf("read %d turns, want the bound of %d", len(preview.messages), previewTurns)
	}
	if !preview.earlier {
		t.Fatal("a conversation longer than the bound was not marked as continuing above")
	}
}

func TestReadSessionPreviewClipsALongTurn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeTranscriptLines(t, root, "claude-personal", "-work-hub", "sid",
		`{"type":"user","cwd":"/work/hub","timestamp":"2026-09-04T11:30:00Z","message":{"content":[{"type":"text","text":"`+
			strings.Repeat("a", previewTurnRunes+200)+`"}]}}`)

	preview := readSessionPreview(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "sid"}})
	if len(preview.messages) != 1 {
		t.Fatalf("preview = %+v, want one turn", preview.messages)
	}
	if runes := []rune(preview.messages[0].text); len(runes) != previewTurnRunes+1 {
		t.Fatalf("turn is %d runes, want %d plus the ellipsis", len(runes), previewTurnRunes)
	}
}

// OpenCode is listed and resumed but its conversations are not read back, so
// the pane has to say so rather than sit on "reading the transcript…" forever.
func TestReadSessionPreviewReportsAProviderItCannotRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	preview := readSessionPreview(Profile{Name: "opencode-go", Provider: "opencode"},
		recordedSession{session: instanceSession{id: "ses_1"}})
	if preview.pending() || preview.problem == "" {
		t.Fatalf("preview = %+v, want a stated reason there is nothing to show", preview)
	}
}

func TestReadSessionPreviewReportsATranscriptThatIsGone(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	preview := readSessionPreview(Profile{Name: "claude-personal", Provider: "claude"},
		recordedSession{session: instanceSession{id: "missing"}})
	if preview.pending() || !strings.Contains(preview.problem, "no longer on disk") {
		t.Fatalf("preview = %+v, want the missing transcript reported", preview)
	}
}

func TestWrapTextKeepsTypedLinesAndDropsBlankRuns(t *testing.T) {
	got := wrapText("one two three\n\n- a bullet that is longer than the pane", 16)
	want := []string{"one two three", "- a bullet that", "is longer than", "the pane"}
	if len(got) != len(want) {
		t.Fatalf("wrapped = %q, want %q", got, want)
	}
	for index, line := range want {
		if got[index] != line {
			t.Fatalf("line %d = %q, want %q", index, got[index], line)
		}
	}
}

func TestPreviewLinesFillExactlyTheRowsTheyAreGiven(t *testing.T) {
	m := tuiModel{preview: sessionPreview{session: "sid", messages: []handoffMessage{
		{fromUser: true, text: strings.Repeat("a sentence that wraps ", 20)},
	}}}
	record := recordedSession{session: instanceSession{id: "sid", title: "Preview pane"}, folder: "/work/hub"}
	lines := m.previewLines(Profile{Provider: "claude"}, record, 30, 8)
	if len(lines) != 8 {
		t.Fatalf("pane is %d rows, want the 8 it was given", len(lines))
	}
	if lines[5] != previewCutMarker {
		t.Fatalf("a conversation cut at the top does not mark it:\n%s", strings.Join(lines, "\n"))
	}
}

func TestPreviewLinesSayWhenTheTranscriptIsStillBeingRead(t *testing.T) {
	m := tuiModel{}
	record := recordedSession{session: instanceSession{id: "sid", title: "Preview pane"}}
	pane := strings.Join(m.previewLines(Profile{Provider: "claude"}, record, 40, 8), "\n")
	if !strings.Contains(pane, "reading the transcript") {
		t.Fatalf("pane = %q, want it to say the read is still running", pane)
	}
}

// A session opened with a pasted brief or a batch prompt has one enormous first
// message. Uncapped it spends every row of the pane on itself and the pane shows
// one message instead of a conversation.
func TestPreviewPaneDoesNotLetOneLongMessageFillIt(t *testing.T) {
	m := tuiModel{preview: sessionPreview{session: "sid", messages: []handoffMessage{
		{fromUser: true, text: strings.Repeat("a wall of pasted text ", 300)},
		{fromUser: false, text: "the recognisable answer"},
	}}}
	record := recordedSession{session: instanceSession{id: "sid", title: "Long opener"}, folder: "/work/hub"}
	pane := strings.Join(m.previewLines(Profile{Provider: "claude"}, record, 40, 30), "\n")
	if !strings.Contains(pane, "the recognisable answer") {
		t.Fatalf("the second turn was pushed out by the first:\n%s", pane)
	}
	if !strings.Contains(pane, "…") {
		t.Fatalf("the long turn was not marked as cut:\n%s", pane)
	}
}

// The pane names the account and the conversation id, which is what
// `ai <profile> resume <id>` takes and which was previously nowhere to read.
func TestPreviewPaneShowsTheConversationIdentity(t *testing.T) {
	m := tuiModel{preview: sessionPreview{session: "sid", messages: []handoffMessage{
		{fromUser: true, text: "hi"},
	}}}
	record := recordedSession{session: instanceSession{id: "c87bbb48-4ae7", title: "A session"}, folder: "/work/hub"}
	pane := strings.Join(m.previewLines(Profile{Name: "max", Provider: "claude"}, record, 60, 12), "\n")
	if !strings.Contains(pane, "id c87bbb48-4ae7") || !strings.Contains(pane, "max") {
		t.Fatalf("pane = %q, want the account and the conversation id", pane)
	}
}

// A conversation is dated in the pane by its last activity, the same fact the
// list is ordered by.
func TestPreviewPaneDatesByLastActivity(t *testing.T) {
	start := time.Date(2026, 9, 4, 10, 0, 0, 0, time.Local)
	active := time.Date(2026, 9, 4, 12, 0, 0, 0, time.Local)
	record := recordedSession{session: instanceSession{id: "sid", title: "A session"}, folder: "/work/hub", when: start, lastActive: active}
	got := previewWhere(active, record)
	if !strings.Contains(got, active.Format("15:04")) || strings.Contains(got, start.Format("15:04")) {
		t.Fatalf("previewWhere = %q, want the last-activity time %s", got, active.Format("15:04"))
	}
}

// The tail read must not decode a broken record where its window begins a line
// early, and must still find the last messages of a file larger than the window.
func TestReadTailMessagesReadsOnlyTheEndOfALargeTranscript(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeLargeTranscript(t, 200, 2000)

	if info, err := os.Stat(path); err != nil || info.Size() <= previewTailBytes {
		t.Fatalf("fixture is %d bytes, want it larger than the %d-byte window", statSize(path), previewTailBytes)
	}
	messages, earlier, err := readTailMessages(path, "claude", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !earlier {
		t.Fatal("a transcript longer than what is shown was not marked as continuing above")
	}
	if len(messages) != 5 {
		t.Fatalf("read %d messages, want the last 5", len(messages))
	}
	if got := messages[len(messages)-1].text; got != "the very last thing" {
		t.Fatalf("last message = %q, want the end of the transcript", got)
	}
}

// A single line larger than the read window leaves no complete record in it, so
// the window has to widen past the line rather than give up on the session.
func TestReadTailMessagesWidensPastAnOversizedLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeTranscriptFixture(t, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+
		strings.Repeat("a", previewTailBytes+1000)+`"}]}}`)

	messages, _, err := readTailMessages(path, "claude", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len([]rune(messages[0].text)) != previewTailBytes+1000 {
		t.Fatalf("read %+v, want the oversized single message", messages)
	}
}

// writeLargeTranscript writes turns of roughly size bytes each, ending in one
// short, recognisable message.
func writeLargeTranscript(t *testing.T, turns, size int) string {
	t.Helper()
	lines := make([]string, 0, turns+1)
	for index := range turns {
		text := strings.Repeat("x", size) + fmt.Sprintf(" message %d", index)
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+text+`"}]}}`)
	}
	lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"the very last thing"}]}}`)
	return writeTranscriptFixture(t, lines...)
}

// writeTranscriptFixture writes raw transcript lines somewhere the claude
// reader can reach and returns the path, without dating the file.
func writeTranscriptFixture(t *testing.T, lines ...string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), appName, "profiles", "claude-personal", "claude", "projects", "-work-hub")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func statSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}
