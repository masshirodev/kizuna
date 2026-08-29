package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestFrameLayoutFoldsColumnsRatherThanShrinkingThem(t *testing.T) {
	wide := frameLayout(149, 34)
	if wide.columns != 3 || wide.list != wideListColumn || wide.live != liveColumnWidth {
		t.Fatalf("wide frame = %+v, want three columns", wide)
	}
	if got := wide.list + wide.detail + wide.live + 2*dividerWidth; got != 149 {
		t.Fatalf("three columns and their gutters cover %d of 149", got)
	}

	medium := frameLayout(100, 30)
	if medium.columns != 2 || medium.live != 0 {
		t.Fatalf("medium frame = %+v, want the live column folded away", medium)
	}
	if got := medium.list + medium.detail + dividerWidth; got != 100 {
		t.Fatalf("two columns and their gutter cover %d of 100", got)
	}

	if narrow := frameLayout(70, 24); narrow.columns != 1 || narrow.list != 70 {
		t.Fatalf("narrow frame = %+v, want a single full-width column", narrow)
	}
}

func TestFrameLayoutAssumesASizeWhenTheTerminalIsSilent(t *testing.T) {
	frame := frameLayout(0, 0)
	if frame.width != assumedWidth || frame.height != assumedHeight {
		t.Fatalf("frame = %+v, want the assumed size", frame)
	}
	if frame.body != assumedHeight-chromeRows {
		t.Fatalf("body = %d, want the height less the bars and rules", frame.body)
	}
}

func TestFitColumnDropsTheMostExpendableBlockFirst(t *testing.T) {
	blocks := []block{
		textBlock(dropNever, "PROFILES", "one", "two"),
		textBlock(dropAuth, "AUTH", "yes"),
		textBlock(dropRecent, "RECENT", "a session"),
		textBlock(dropActivity, "ACTIVITY", "▁▂▃"),
	}
	// Room for the required block, one gap, and one optional block.
	lines := strings.Join(fitColumn(blocks, 6, 20), "\n")
	if !strings.Contains(lines, "PROFILES") || !strings.Contains(lines, "AUTH") {
		t.Fatalf("fitColumn dropped a block it had room for:\n%s", lines)
	}
	if strings.Contains(lines, "ACTIVITY") || strings.Contains(lines, "RECENT") {
		t.Fatalf("fitColumn kept blocks past the row budget:\n%s", lines)
	}
}

// A heading with nothing under it reads as a rendering fault, so the last block
// is dropped whole rather than cut below its heading.
func TestFitColumnDropsABlockItCannotShowAtAll(t *testing.T) {
	blocks := []block{
		textBlock(dropNever, "PROFILES", "one"),
		textBlock(dropNever, "QUOTA", "5H", "7D"),
	}
	lines := strings.Join(fitColumn(blocks, 4, 20), "\n")
	if strings.Contains(lines, "QUOTA") {
		t.Fatalf("a block was left with only its heading:\n%s", lines)
	}
}

// Dropping a long panel to reclaim a single row would trade the panel for a
// screen of blank lines, so a block with room to spare is clipped instead.
func TestFitColumnClipsALongBlockRatherThanLosingIt(t *testing.T) {
	long := textBlock(dropNever, "RUNNING", "a", "b", "c", "d", "e", "f")
	blocks := []block{textBlock(dropNever, "PROFILES", "one"), long}
	lines := strings.Join(fitColumn(blocks, 9, 20), "\n")
	if !strings.Contains(lines, "RUNNING") || !strings.Contains(lines, "e") {
		t.Fatalf("a long block was dropped to save one row:\n%s", lines)
	}
}

func TestFitColumnPinsTheFooterBelowTheFlexPoint(t *testing.T) {
	blocks := []block{
		textBlock(dropNever, "PROFILES", "one"),
		flexBlock(),
		textBlock(dropNever, "FOLDER"),
	}
	lines := fitColumn(blocks, 8, 20)
	if len(lines) != 8 {
		t.Fatalf("column is %d rows, want 8", len(lines))
	}
	if strings.TrimSpace(lines[7]) != "FOLDER" {
		t.Fatalf("footer is not pinned to the bottom row: %q", lines[7])
	}
	if strings.TrimSpace(lines[3]) != "" {
		t.Fatalf("spare rows did not land at the flex point: %q", lines[3])
	}
	for _, line := range lines {
		if lipgloss.Width(line) != 20 {
			t.Fatalf("line is %d columns wide: %q", lipgloss.Width(line), line)
		}
	}
}

func TestOverlayReplacesOnlyTheRowsAndColumnsTheBoxCovers(t *testing.T) {
	background := strings.Join([]string{
		"aaaaaaaaaa",
		"bbbbbbbbbb",
		"cccccccccc",
	}, "\n")
	got := overlay(background, "XX\nYY", 1, 4)
	want := strings.Join([]string{
		"aaaaaaaaaa",
		"bbbbXXbbbb",
		"ccccYYcccc",
	}, "\n")
	if got != want {
		t.Fatalf("overlay = %q, want %q", got, want)
	}
}

// A naive string index would land inside an escape sequence and leak the box's
// colours across the rest of the row.
func TestOverlayCutsStyledBackgroundWithoutBleeding(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000")).Render("aaaaaaaaaa")
	got := overlay(styled, "XX", 0, 4)
	if lipgloss.Width(got) != 10 {
		t.Fatalf("overlay changed the row width to %d:\n%q", lipgloss.Width(got), got)
	}
	if !strings.Contains(got, "XX") {
		t.Fatalf("overlay did not draw the box:\n%q", got)
	}
}

// A terminal without colour has no pen to close; emitting a reset anyway would
// print the escape as text.
func TestOverlayAddsNoResetToPlainText(t *testing.T) {
	if got := overlay("aaaa", "X", 0, 1); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("overlay emitted an escape into plain text: %q", got)
	}
}

func TestOverlayIgnoresRowsPastTheBackground(t *testing.T) {
	if got := overlay("aaaa", "X\nY\nZ", 3, 0); got != "aaaa" {
		t.Fatalf("overlay = %q, want the background untouched", got)
	}
}

func TestCenterBoxKeepsTheFrameShape(t *testing.T) {
	frame := frameLayout(40, 12)
	rows := make([]string, frame.height)
	for index := range rows {
		rows[index] = strings.Repeat(".", frame.width)
	}
	box := "┌────┐\n│ hi │\n└────┘"
	lines := strings.Split(centerBox(strings.Join(rows, "\n"), box, frame), "\n")
	if len(lines) != frame.height {
		t.Fatalf("centred box changed the frame height to %d", len(lines))
	}
	for index, line := range lines {
		if lipgloss.Width(line) != frame.width {
			t.Fatalf("line %d is %d columns wide: %q", index, lipgloss.Width(line), line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "│ hi │") {
		t.Fatalf("centred box is missing from the frame:\n%s", strings.Join(lines, "\n"))
	}
}

func TestPadLineTrimsAndFillsToTheExactWidth(t *testing.T) {
	if got := padLine("abc", 6); got != "abc   " {
		t.Fatalf("padLine = %q", got)
	}
	if got := padLine("abcdef", 3); lipgloss.Width(got) != 3 {
		t.Fatalf("padLine = %q, want three columns", got)
	}
}
