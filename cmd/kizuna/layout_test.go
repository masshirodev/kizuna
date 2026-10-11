package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The board gives its gauges whatever the fixed columns leave, up to a length
// past which a longer bar says nothing more, and drops them — keeping the
// figures — once they would be too short to read.
func TestBoardLayoutShrinksGaugesBeforeDroppingColumns(t *testing.T) {
	profiles := []Profile{{Name: "claude-personal", Provider: "claude"}, {Name: "antigravity-personal", Provider: "antigravity"}}
	wide := boardLayout(146, profiles)
	if wide.gauge != boardMaxGauge || !wide.reset || !wide.auth || !wide.live {
		t.Fatalf("wide board = %+v, want full gauges and every column", wide)
	}
	medium := boardLayout(110, profiles)
	if medium.gauge <= boardMinGauge || medium.gauge >= boardMaxGauge {
		t.Fatalf("medium board = %+v, want gauges shortened but kept", medium)
	}
	narrow := boardLayout(70, profiles)
	if narrow.gauge != 0 {
		t.Fatalf("narrow board = %+v, want the gauges dropped", narrow)
	}
	for _, width := range []int{146, 110, 70, 50} {
		c := boardLayout(width, profiles)
		used := 2 + c.name + c.provider + 2*c.cell() + 4
		if c.auth {
			used += boardAuth
		}
		if c.live {
			used += boardLive
		}
		if used > width && width >= 60 {
			t.Fatalf("board at %d uses %d columns: %+v", width, used, c)
		}
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
