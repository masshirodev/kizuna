package main

import (
	"bufio"
	"bytes"
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
// What is shown is the end of the conversation, not its beginning. The opening
// of a session is often a pasted brief or a batch prompt that names the work
// only indirectly, and what distinguishes one session from another now is what
// was last being done in it. Reading the tail has to be bounded the other way,
// though: a six-megabyte transcript cannot be read from the top on every cursor
// move, so the file is read backwards, a window at a time, until enough of the
// last things said have been collected.

const (
	// previewTurns is how many turns the pane shows. It is more than fits at any
	// terminal size the modal is drawn at, so the pane runs out of room before
	// it runs out of conversation.
	previewTurns = 24
	// previewTailBytes is the first window read from the end of a transcript.
	// The natural place for the last messages is within the last few hundred
	// kilobytes; the window doubles only when it holds too little.
	previewTailBytes = 256 * 1024
	// previewTailMax caps how far back the doubling goes before the read
	// settles for whatever it has. Without it, a transcript whose tail is
	// megabytes of tool output would be read whole on every cursor move.
	previewTailMax = 1024 * 1024
	// previewTurnRunes clips one turn as it is read. A pasted stack trace is a
	// turn too, and reading the whole of one would spend the read on a file that
	// may be six megabytes.
	previewTurnRunes = 600
	// previewTurnRows caps how many rows one turn may take in the pane. Without
	// it a single long message — a pasted brief, a batch prompt — spends every
	// row the pane has on itself, and the pane shows one message instead of a
	// conversation. Capping by rows rather than runes is what makes the bound
	// hold however the message wraps.
	previewTurnRows = 4
	// briefTurnRows is the same cap for the handoff confirmation, which also
	// shows the end of the conversation. It is looser because the last messages
	// are the ones the screen exists to show.
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
	// earlier says something was said before what is shown, which is marked
	// above the first turn. It is the difference between a conversation that
	// begins here and one whose smaller half is on screen.
	earlier bool
	problem string
}

func (p sessionPreview) pending() bool {
	return len(p.messages) == 0 && p.problem == ""
}

// readSessionPreview reads the last turns of a recorded conversation, without
// reading the whole transcript to reach them.
func readSessionPreview(profile Profile, record recordedSession) sessionPreview {
	preview := sessionPreview{session: record.session.id}
	path, err := transcriptPath(profile, record)
	if err != nil {
		preview.problem = err.Error()
		return preview
	}
	messages, earlier, err := readTailMessages(path, profile.Provider, previewTurns)
	if err != nil {
		preview.problem = err.Error()
		return preview
	}
	if len(messages) == 0 {
		preview.problem = "nothing was said in this session"
		return preview
	}
	for index := range messages {
		messages[index].text = clipRunes(messages[index].text, previewTurnRunes)
	}
	preview.messages, preview.earlier = messages, earlier
	return preview
}

// readTailMessages returns the last messages of a transcript in order, and
// whether anything was said before them. It reads windows from the end of the
// file rather than the whole file, doubling the window only when it has not
// collected enough, so an ordinary transcript costs only its tail.
func readTailMessages(path, provider string, limit int) ([]handoffMessage, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()

	window := min(int64(previewTailBytes), size)
	for {
		offset := size - window
		buf := make([]byte, window)
		if _, err := file.ReadAt(buf, offset); err != nil {
			return nil, false, err
		}
		// A window that starts mid-line would decode a broken record, so the
		// first partial line goes. The line before it is only missing because
		// the window began after it.
		if offset > 0 {
			if index := bytes.IndexByte(buf, '\n'); index >= 0 {
				buf = buf[index+1:]
			} else {
				buf = nil
			}
		}
		messages := decodeMessageLines(provider, buf)
		reachedStart := offset == 0
		if len(messages) >= limit || reachedStart || window >= previewTailMax {
			if len(messages) == 0 && !reachedStart {
				// The tail is all tool traffic with no prose in it. Read the
				// whole conversation and take its last messages rather than
				// showing an empty pane for a session that said plenty.
				all, err := readAllMessages(path, provider)
				if err != nil {
					return nil, false, err
				}
				messages, reachedStart = all, true
			}
			earlier := !reachedStart || len(messages) > limit
			if len(messages) > limit {
				messages = messages[len(messages)-limit:]
			}
			return messages, earlier, nil
		}
		window = min(window*2, size)
	}
}

// decodeMessageLines decodes every complete line in a block of transcript.
func decodeMessageLines(provider string, data []byte) []handoffMessage {
	var messages []handoffMessage
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if message, ok := decodeHandoffLine(provider, line); ok {
			messages = append(messages, message)
		}
	}
	return messages
}

// readAllMessages reads a whole transcript into said things. It is the fallback
// for a tail with no prose in it, and what the handoff brief is built from.
func readAllMessages(path, provider string) ([]handoffMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var messages []handoffMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	for scanner.Scan() {
		if message, ok := decodeHandoffLine(provider, scanner.Bytes()); ok {
			messages = append(messages, message)
		}
	}
	return messages, scanner.Err()
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
// header naming the session, then how it ended. It stops at the row it runs out
// of room on rather than wrapping into the list beside it.
func (m tuiModel) previewLines(profile Profile, record recordedSession, width, rows int) []string {
	title, titleStyle := record.session.title, fieldValueStyle
	if title == "" {
		title, titleStyle = "untitled session", unknownStyle
	}
	header := []string{
		sectionLabelStyle.Render("PREVIEW"),
		titleStyle.Render(truncate(title, width)),
		dimStyle.Render(truncate(previewWhere(m.clock(), record), width)),
		dimStyle.Render(truncate(previewIdentity(profile, record), width)),
		"",
	}

	preview := m.preview
	switch {
	case preview.session != record.session.id || preview.pending():
		return fitPreview(append(header, unknownStyle.Render("reading the transcript…")), rows)
	case preview.problem != "":
		return fitPreview(append(header, unknownStyle.Render(truncate(preview.problem, width))), rows)
	}

	// The body is the end of the conversation, so it is cut at the top: what
	// goes is the earliest of the turns shown, and the marker above the first of
	// them says the conversation carries on further back.
	bodyRows := rows - len(header)
	if bodyRows < 1 {
		return fitPreview(header, rows)
	}
	body := fitTail(conversationLines(profile.Provider, preview.messages, width, previewTurnRows), bodyRows, preview.earlier)
	return append(header, body...)
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

// conversationLines lays an exchange out as blocks: who spoke, then what they
// said, wrapped. Reading is what these panes are for, so the turns are
// separated by a blank line rather than packed.
//
// turnRows caps how many rows one turn may take, so a single long message cannot
// spend the whole pane on itself; a turn it cut ends in an ellipsis rather than
// simply stopping. Zero means no cap.
//
// Both callers build the whole exchange here and cut it afterwards — the
// picker's pane and the handoff brief both show how the conversation ended, so
// both drop the earliest turns and mark the cut at the top. That way every route
// to running short of the conversation finishes at the same marker rather than
// at a sentence that merely stops.
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
