package main

import (
	"strings"
	"testing"
)

func TestReadSessionPreviewReadsTheOpeningTurnsInOrder(t *testing.T) {
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
	if len(preview.messages) != 3 || preview.more {
		t.Fatalf("preview = %+v, want three turns and no continuation", preview.messages)
	}
	if !preview.messages[0].fromUser || preview.messages[0].text != "add a preview pane" {
		t.Fatalf("first turn = %+v, want what was asked", preview.messages[0])
	}
	if preview.messages[1].fromUser || preview.messages[1].text != "reading the picker first" {
		t.Fatalf("second turn = %+v, want the model's answer", preview.messages[1])
	}
}

// A pane that stopped at its bound and one that reached the end of a
// conversation look the same on screen unless the read says which it was.
func TestReadSessionPreviewMarksAConversationItDidNotFinish(t *testing.T) {
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
	if !preview.more {
		t.Fatal("a conversation that continues past the bound was not marked")
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
	if !strings.Contains(lines[len(lines)-1], "…") {
		t.Fatalf("a cut conversation ends %q, want the continuation marker", lines[len(lines)-1])
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
