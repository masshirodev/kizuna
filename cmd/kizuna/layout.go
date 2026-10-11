package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	// A terminal that has not reported its size yet still has to render one
	// frame. These are the sizes assumed until the first tea.WindowSizeMsg.
	assumedWidth  = 100
	assumedHeight = 30

	// chromeRows counts the top bar, the rule under it, the rule above the
	// bottom bar, and the bottom bar itself.
	chromeRows = 4

	// dividerWidth is the gutter between two panes: a hairline with a space on
	// each side, so a full-width cell never touches the rule beside it.
	dividerWidth = 3

	// screenMarginX and screenMarginY hold the whole cockpit — frame, rules,
	// and any modal centred over it — off the edge of the terminal. Uneven on
	// purpose: a terminal cell is taller than it is wide, so a symmetric
	// column/row count would look narrower on top and bottom than on the
	// sides.
	screenMarginX = 2
	screenMarginY = 1
)

// layout is the cockpit's geometry for one terminal size. The board is one
// table rather than columns, so all it needs is the frame and the rows left
// for the body between the bars.
type layout struct {
	width  int
	height int
	body   int
}

func frameLayout(width, height int) layout {
	if width <= 0 {
		width = assumedWidth
	}
	if height <= 0 {
		height = assumedHeight
	}
	return layout{width: width, height: height, body: max(height-chromeRows, 1)}
}

// padToRows grows or clips a column to exactly rows lines, inserting the spare
// rows at the flex point when there is one and appending them otherwise.
func padToRows(lines []string, rows, flexAt int) []string {
	if len(lines) > rows {
		return lines[:rows]
	}
	spare := rows - len(lines)
	if spare == 0 {
		return lines
	}
	filler := make([]string, spare)
	if flexAt < 0 || flexAt > len(lines) {
		return append(lines, filler...)
	}
	padded := make([]string, 0, rows)
	padded = append(padded, lines[:flexAt]...)
	padded = append(padded, filler...)
	return append(padded, lines[flexAt:]...)
}

// padLine makes one styled line occupy exactly width cells. Every line in the
// frame is padded, because a short line lets the terminal's own background show
// through the column rule beside it.
func padLine(line string, width int) string {
	current := ansi.StringWidth(line)
	if current > width {
		return ansi.Truncate(line, width, "")
	}
	return line + strings.Repeat(" ", width-current)
}

// withMargin insets screen — already sized to width-2*marginX columns wide —
// inside the full terminal width, and adds marginY blank rows above and
// below. It is the outermost step in View(), applied after the modal (if
// any) is already centred over the cockpit, so the margin frames the whole
// composed screen rather than the cockpit alone. marginX and marginY are
// View()'s own already-clamped values, not the screenMarginX/Y constants
// directly, so this never has to reason about whether they fit.
func withMargin(screen string, width, marginX, marginY int) string {
	inner := max(width-2*marginX, 0)
	pad := strings.Repeat(" ", marginX)
	blank := strings.Repeat(" ", width)
	lines := strings.Split(screen, "\n")
	rows := make([]string, 0, len(lines)+2*marginY)
	for i := 0; i < marginY; i++ {
		rows = append(rows, blank)
	}
	for _, line := range lines {
		rows = append(rows, pad+padLine(line, inner)+pad)
	}
	for i := 0; i < marginY; i++ {
		rows = append(rows, blank)
	}
	return strings.Join(rows, "\n")
}

// overlay draws box over background, its top-left corner at the given row and
// column. Both sides are styled terminal text, so the background is sliced with
// ANSI-aware cuts: a naive string index would land inside an escape sequence
// and leak the modal's colours across the rest of the row.
func overlay(background, box string, top, left int) string {
	rows := strings.Split(background, "\n")
	for index, line := range strings.Split(box, "\n") {
		row := top + index
		if row < 0 || row >= len(rows) {
			continue
		}
		width := ansi.StringWidth(line)
		behind := rows[row]
		behindWidth := ansi.StringWidth(behind)
		prefix := ansi.Cut(behind, 0, left)
		if gap := left - ansi.StringWidth(prefix); gap > 0 {
			prefix += strings.Repeat(" ", gap)
		}
		suffix := ansi.Cut(behind, left+width, behindWidth)
		rows[row] = prefix + closeStyle(prefix) + line + closeStyle(line) + suffix
	}
	return strings.Join(rows, "\n")
}

// closeStyle resets the pen after a segment that set one, so the modal starts
// clean and the background resumes clean after it. Terminals without colour get
// nothing to reset, and emitting one anyway would print the escape as text.
func closeStyle(segment string) string {
	if strings.ContainsRune(segment, 0x1b) {
		return "\x1b[0m"
	}
	return ""
}

// centerBox places a modal over the cockpit the way the design does: centred
// horizontally, and high enough that the profile column stays readable beside
// it rather than sitting behind the middle of the box.
func centerBox(background, box string, frame layout) string {
	width, height := boxSize(box)
	left := max((frame.width-width)/2, 0)
	top := max((frame.height-height)/3, 1)
	if top+height > frame.height {
		top = max(frame.height-height, 0)
	}
	return overlay(background, box, top, left)
}

func boxSize(box string) (int, int) {
	lines := strings.Split(box, "\n")
	width := 0
	for _, line := range lines {
		width = max(width, ansi.StringWidth(line))
	}
	return width, len(lines)
}
