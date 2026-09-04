package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// The cockpit is drawn at a fixed shape and folds columns away rather than
// letting them shrink past readability. The widths come from the design, which
// is 149 columns wide with a 40-column profile list and a 38-column live panel.
const (
	threeColumnWidth = 118
	twoColumnWidth   = 78
	wideListColumn   = 40
	narrowListColumn = 36
	liveColumnWidth  = 38

	// A terminal that has not reported its size yet still has to render one
	// frame. These are the sizes assumed until the first tea.WindowSizeMsg.
	assumedWidth  = 100
	assumedHeight = 30

	// chromeRows counts the top bar, the rule under it, the rule above the
	// bottom bar, and the bottom bar itself.
	chromeRows = 4

	// dividerWidth is the gutter between columns: a hairline with a space on
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

// layout is the cockpit's geometry for one terminal size. A zero right or
// middle width means that column is folded into the one before it rather than
// squeezed, which keeps every remaining column wide enough to read.
type layout struct {
	width   int
	height  int
	body    int
	columns int
	list    int
	detail  int
	live    int
}

func frameLayout(width, height int) layout {
	if width <= 0 {
		width = assumedWidth
	}
	if height <= 0 {
		height = assumedHeight
	}
	frame := layout{width: width, height: height, body: max(height-chromeRows, 1)}
	switch {
	case width >= threeColumnWidth:
		frame.columns = 3
		frame.list = wideListColumn
		frame.live = liveColumnWidth
		frame.detail = width - frame.list - frame.live - 2*dividerWidth
	case width >= twoColumnWidth:
		frame.columns = 2
		frame.list = narrowListColumn
		frame.detail = width - frame.list - dividerWidth
	default:
		frame.columns = 1
		frame.list = width
	}
	return frame
}

// Blocks are dropped highest first when a column is shorter than its content.
// The order is what a glance can most afford to lose: the activity histogram is
// decoration, the log repeats the status line, and the recent list is history,
// while the profile table and the live instances are the reason for the screen.
const (
	dropNever = iota
	dropAuth
	dropRecent
	dropLog
	dropActivity
)

// block is one labelled section of a column.
type block struct {
	lines []string
	drop  int
	// flex absorbs the rows no block claimed, which is what pins the folder
	// footer to the bottom of the profile column.
	flex bool
}

func textBlock(drop int, lines ...string) block {
	return block{lines: lines, drop: drop}
}

func flexBlock() block {
	return block{flex: true}
}

// fitColumn renders blocks top to bottom in exactly rows lines of exactly width
// columns, separated by a blank line. Content that does not fit costs whole
// blocks rather than being clipped mid-section: half a histogram reads as a
// rendering fault, while a missing one reads as a small terminal.
func fitColumn(blocks []block, rows, width int) []string {
	blocks = dropToFit(blocks, rows)
	blocks = truncateToFit(blocks, rows)
	lines := make([]string, 0, rows)
	flexAt := -1
	for index, current := range blocks {
		if current.flex {
			flexAt = len(lines)
			continue
		}
		if len(lines) > 0 && index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, current.lines...)
	}
	lines = padToRows(lines, rows, flexAt)
	for index, line := range lines {
		lines[index] = padLine(line, width)
	}
	return lines
}

// dropToFit removes optional blocks, most droppable first, until the column
// fits. A column that still overflows with nothing left to drop is clipped by
// fitColumn, which is the narrow-terminal case rather than the normal one.
func dropToFit(blocks []block, rows int) []block {
	blocks = append([]block(nil), blocks...)
	for blockRows(blocks) > rows {
		victim, priority := -1, dropNever
		for index, current := range blocks {
			if current.drop > priority {
				victim, priority = index, current.drop
			}
		}
		if victim < 0 {
			break
		}
		blocks = append(blocks[:victim], blocks[victim+1:]...)
	}
	return blocks
}

// minBlockRows is the least a block can be cut to and still say something: its
// heading and one line under it.
const minBlockRows = 2

// truncateToFit settles the last block once every optional one is already gone.
// A block that would be cut below its heading is dropped whole — a QUOTA label
// with nothing under it reads as a rendering fault. A block with room to spare
// is left to be clipped instead, because dropping a twelve-line panel to
// reclaim one row trades the panel for eleven blank lines.
func truncateToFit(blocks []block, rows int) []block {
	for len(blocks) > 1 {
		over := blockRows(blocks) - rows
		if over <= 0 {
			break
		}
		if last := blocks[len(blocks)-1]; len(last.lines)-over >= minBlockRows {
			break
		}
		blocks = blocks[:len(blocks)-1]
	}
	return blocks
}

func blockRows(blocks []block) int {
	total, drawn := 0, 0
	for _, current := range blocks {
		if current.flex {
			continue
		}
		if drawn > 0 {
			total++
		}
		total += len(current.lines)
		drawn++
	}
	return total
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
