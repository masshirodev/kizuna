package main

import (
	"bufio"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// A title says what a conversation was called; it does not say whether it is
// the one being looked for. The resume and handoff pickers therefore read the
// chosen session beside the list, the way a file picker shows the file it is
// hovering.
//
// What is read is bounded twice over. The panel behind the picker already costs
// a scan of every transcript on every refresh, and moving the cursor must not
// cost more than that: only the opening turns are read, and only the first few
// hundred runes of each. The opening rather than the tail, because what a
// session was for is settled in its first exchanges, while reaching the tail of
// a six-megabyte transcript means reading all of it.

const (
	// previewTurns is how many turns are read. It is more than fits in the pane
	// at any terminal size the modal is drawn at, so the pane runs out of room
	// before it runs out of conversation.
	previewTurns = 24
	// previewTurnRunes clips one turn as it is read. A pasted stack trace is a
	// turn too, and reading the whole of one would spend the read on a file that
	// may be six megabytes.
	previewTurnRunes = 600
	// previewTurnRows caps how many rows one turn may take in the pane. Without
	// it a single long opening message — a pasted brief, a batch prompt — spends
	// every row the pane has on itself, and the pane shows one message instead
	// of a conversation. Capping by rows rather than runes is what makes the
	// bound hold however the message wraps.
	previewTurnRows = 4
	// briefTurnRows is the same cap for the handoff confirmation, which reads
	// the end of the conversation rather than its opening. It is looser because
	// the last messages are the ones the screen exists to show.
	briefTurnRows = 6
)

// sessionPreview is one conversation read far enough to recognise. It names the
// session it was read for because the read happens off the keypress that asked
// for it, and the cursor may have moved on by the time it lands.
//
// An empty preview with no problem is one still being read: readSessionPreview
// always leaves a problem behind when it comes back with nothing, so the pane
// can tell "still reading" from "nothing to read" without a flag of its own.
type sessionPreview struct {
	session  string
	messages []handoffMessage
	// more says the conversation goes on past what was read, which is the
	// difference between a preview that ends and a conversation that did.
	more    bool
	problem string
}

func (p sessionPreview) pending() bool {
	return len(p.messages) == 0 && p.problem == ""
}

// readSessionPreview reads the opening of a recorded conversation.
func readSessionPreview(profile Profile, record recordedSession) sessionPreview {
	preview := sessionPreview{session: record.session.id}
	path, err := transcriptPath(profile, record)
	if err != nil {
		preview.problem = err.Error()
		return preview
	}
	file, err := os.Open(path)
	if err != nil {
		preview.problem = err.Error()
		return preview
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	for scanner.Scan() {
		message, ok := decodeHandoffLine(profile.Provider, scanner.Bytes())
		if !ok {
			continue
		}
		if len(preview.messages) >= previewTurns {
			preview.more = true
			break
		}
		message.text = clipRunes(message.text, previewTurnRunes)
		preview.messages = append(preview.messages, message)
	}
	if len(preview.messages) == 0 {
		preview.problem = "nothing was said in this session"
	}
	return preview
}

// clipRunes shortens a turn without the marker clip() adds for a brief. A pane
// cuts off at its last row anyway, and "[…trimmed]" inside the text would read
// as something the speaker wrote.
func clipRunes(text string, limit int) string {
	if runes := []rune(text); len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit])) + "…"
	}
	return text
}

// previewSpeaker labels a turn. The model's side is named by provider rather
// than "assistant" because the pane sits beside a list of that provider's own
// sessions, and the two labels are there to be told apart at a glance.
func previewSpeaker(provider string, fromUser bool) string {
	if fromUser {
		return "you"
	}
	if provider == "" {
		return "agent"
	}
	return provider
}

// previewLines renders one conversation into a column of at most rows lines: a
// header naming the session, then the exchange itself. It stops at the row it
// runs out of room on rather than wrapping into the list beside it.
func (m tuiModel) previewLines(profile Profile, record recordedSession, width, rows int) []string {
	lines := []string{sectionLabelStyle.Render("PREVIEW")}
	title, titleStyle := record.session.title, fieldValueStyle
	if title == "" {
		title, titleStyle = "untitled session", unknownStyle
	}
	lines = append(lines,
		titleStyle.Render(truncate(title, width)),
		dimStyle.Render(truncate(previewWhere(m.clock(), record), width)),
		dimStyle.Render(truncate(previewIdentity(profile, record), width)),
		"")

	preview := m.preview
	switch {
	case preview.session != record.session.id || preview.pending():
		lines = append(lines, unknownStyle.Render("reading the transcript…"))
	case preview.problem != "":
		lines = append(lines, unknownStyle.Render(truncate(preview.problem, width)))
	default:
		lines = append(lines, previewBody(profile.Provider, preview, width)...)
	}
	return fitPreview(lines, rows)
}

// previewWhere dates a conversation by when it was last spoken in rather than
// when it was opened, because that is the order the list is in and the two can
// be days apart.
func previewWhere(now time.Time, record recordedSession) string {
	where := shortenHome(record.folder)
	if where == "" {
		where = "no folder recorded"
	}
	return formatWhen(now, record.activity()) + " · " + where
}

// previewIdentity names the account and the conversation id. The id is what
// `ai <profile> resume <id>` takes, and there was previously nowhere to read it.
func previewIdentity(profile Profile, record recordedSession) string {
	identity := "id " + record.session.id
	if profile.Name != "" {
		identity = profile.Name + " · " + identity
	}
	return identity
}

// previewBody is the opening of a conversation, marked as going on past what
// was read when it does.
func previewBody(provider string, preview sessionPreview, width int) []string {
	lines := conversationLines(provider, preview.messages, width, previewTurnRows)
	if preview.more {
		lines = append(lines, "", previewCutMarker)
	}
	return lines
}

// conversationLines lays an exchange out as blocks: who spoke, then what they
// said, wrapped. Reading is what these panes are for, so the turns are
// separated by a blank line rather than packed.
//
// turnRows caps how many rows one turn may take, so a single long message cannot
// spend the whole pane on itself; a turn it cut ends in an ellipsis rather than
// simply stopping. Zero means no cap.
//
// Both callers build the whole exchange here and cut it afterwards — the
// picker's pane from the end, the handoff brief from the start, since one is
// reading a conversation from its opening and the other is showing how it
// ended. That way every route to running short of the conversation finishes at
// the same marker rather than at a sentence that merely stops.
func conversationLines(provider string, messages []handoffMessage, width, turnRows int) []string {
	var lines []string
	for index, message := range messages {
		if index > 0 {
			lines = append(lines, "")
		}
		style := providerStyle(provider)
		if message.fromUser {
			style = helpKeyStyle
		}
		lines = append(lines, style.Render(previewSpeaker(provider, message.fromUser)))
		body := hintStyle
		if message.fromUser {
			body = fieldValueStyle
		}
		wrapped := wrapText(message.text, width)
		if turnRows > 0 && len(wrapped) > turnRows {
			wrapped = wrapped[:turnRows]
			wrapped[len(wrapped)-1] += "…"
		}
		for _, line := range wrapped {
			lines = append(lines, body.Render(line))
		}
	}
	return lines
}

// previewCutMarker says the conversation goes on past the pane. A pane that
// simply stops looks like a conversation that did.
var previewCutMarker = dimStyle.Render("…")

// fitPreview settles the pane at exactly its height.
func fitPreview(lines []string, rows int) []string {
	if rows < 1 {
		return nil
	}
	if len(lines) <= rows {
		return lines
	}
	return append(lines[:rows-1], previewCutMarker)
}

// fitTail settles a block that is worth reading from its end rather than its
// start, dropping the earliest rows and saying so above what is left. It is the
// handoff brief's cut: the question that screen asks is whether this is the
// work that was meant to move, and the answer is in the last thing that was
// said rather than the first.
func fitTail(lines []string, rows int, earlier bool) []string {
	if rows < 1 {
		return nil
	}
	if len(lines) <= rows && !earlier {
		return lines
	}
	if len(lines) > rows-1 {
		lines = lines[len(lines)-max(rows-1, 0):]
		// A cut that lands on the blank between two turns would leave the marker
		// with a gap under it, which reads as a missing line rather than as a
		// conversation carrying on above.
		for len(lines) > 0 && lines[0] == "" {
			lines = lines[1:]
		}
	}
	return append([]string{previewCutMarker}, lines...)
}

// wrapText breaks one turn into lines that fit the pane. Newlines the speaker
// typed are kept, because a numbered list read as one paragraph is not what was
// said; blank runs between them are not, because a preview has no rows to spend
// on the spacing of a message it is only showing part of.
func wrapText(text string, width int) []string {
	if width < 1 {
		return nil
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		lines = append(lines, strings.Split(ansi.Wrap(paragraph, width, ""), "\n")...)
	}
	return lines
}
