package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func typed(text string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)}
}

// applyKeys runs keystrokes through editLine from a value with the caret at its
// end, and returns the value and the caret as a rune index from the start.
func applyKeys(t *testing.T, value string, keys ...tea.KeyMsg) (string, int) {
	t.Helper()
	tail := 0
	for _, key := range keys {
		var edited bool
		value, tail, edited = editLine(value, tail, key)
		if !edited {
			t.Fatalf("%q was not treated as an editing key", key.String())
		}
	}
	return value, len([]rune(value)) - tail
}

func TestEditLineMovesTheCaretAndEditsWhereItSits(t *testing.T) {
	left := tea.KeyMsg{Type: tea.KeyLeft}
	right := tea.KeyMsg{Type: tea.KeyRight}
	home := tea.KeyMsg{Type: tea.KeyHome}
	end := tea.KeyMsg{Type: tea.KeyEnd}
	backspace := tea.KeyMsg{Type: tea.KeyBackspace}
	del := tea.KeyMsg{Type: tea.KeyDelete}
	cases := []struct {
		name  string
		start string
		keys  []tea.KeyMsg
		value string
		caret int
	}{
		{"typing continues at the end", "run", []tea.KeyMsg{typed("s")}, "runs", 4},
		{"left then type inserts before the last rune", "abc", []tea.KeyMsg{left, typed("X")}, "abXc", 3},
		{"home then type prepends", "codex", []tea.KeyMsg{home, typed("my-")}, "my-codex", 3},
		{"ctrl+a is home", "codex", []tea.KeyMsg{{Type: tea.KeyCtrlA}, typed(">")}, ">codex", 1},
		{"end after home returns to the end", "abc", []tea.KeyMsg{home, end, typed("d")}, "abcd", 4},
		{"ctrl+e is end", "abc", []tea.KeyMsg{home, {Type: tea.KeyCtrlE}}, "abc", 3},
		{"right stops at the end", "ab", []tea.KeyMsg{right, right}, "ab", 2},
		{"left stops at the start", "ab", []tea.KeyMsg{left, left, left}, "ab", 0},
		{"backspace deletes before the caret", "abcd", []tea.KeyMsg{left, backspace}, "abd", 2},
		{"backspace at the start does nothing", "ab", []tea.KeyMsg{home, backspace}, "ab", 0},
		{"delete removes the rune under the caret", "abcd", []tea.KeyMsg{home, del}, "bcd", 0},
		{"delete at the end does nothing", "ab", []tea.KeyMsg{del}, "ab", 2},
		{"ctrl+u kills back to the start", "--model x", []tea.KeyMsg{left, {Type: tea.KeyCtrlU}}, "x", 0},
		{"ctrl+u at the end clears the field", "abc", []tea.KeyMsg{{Type: tea.KeyCtrlU}}, "", 0},
		{"ctrl+k kills to the end", "--model x", []tea.KeyMsg{home, right, right, {Type: tea.KeyCtrlK}}, "--", 2},
		{"ctrl+w deletes the word before", "run --auto now", []tea.KeyMsg{{Type: tea.KeyCtrlW}}, "run --auto ", 11},
		{"alt+left jumps a word back", "run --auto now", []tea.KeyMsg{{Type: tea.KeyLeft, Alt: true}, typed("X")}, "run --auto Xnow", 12},
		{"ctrl+right jumps a word forward", "a bc de", []tea.KeyMsg{home, {Type: tea.KeyCtrlRight}, typed("!")}, "a! bc de", 2},
		{"space inserts at the caret", "ab", []tea.KeyMsg{left, {Type: tea.KeySpace, Runes: []rune(" ")}}, "a b", 2},
		{"multibyte runes move one at a time", "añb", []tea.KeyMsg{left, left, backspace}, "ñb", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, caret := applyKeys(t, tc.start, tc.keys...)
			if value != tc.value || caret != tc.caret {
				t.Fatalf("got %q with caret %d, want %q with caret %d", value, caret, tc.value, tc.caret)
			}
		})
	}
}

func TestEditLineLeavesOtherKeysToTheCaller(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyEsc}, {Type: tea.KeyUp}, {Type: tea.KeyTab}} {
		if value, tail, edited := editLine("abc", 1, key); edited || value != "abc" || tail != 1 {
			t.Fatalf("%q was handled: %q %d %v", key.String(), value, tail, edited)
		}
	}
}

// A field shortened from somewhere else (a reset, a recalled set) must not
// leave the caret outside it.
func TestEditLineClampsAStaleCaret(t *testing.T) {
	value, tail, _ := editLine("ab", 9, typed("c"))
	if value != "abc" || tail != 0 {
		t.Fatalf("got %q tail %d, want the caret treated as at the end", value, tail)
	}
}

func visible(s string) string { return ansi.Strip(s) }

func TestCaretViewKeepsTheCaretInsideTheWindow(t *testing.T) {
	style := lipgloss.NewStyle()
	value := "/home/masshiro/projects/some/long/path"
	// At the end: the window is the tail of the value plus the blank caret cell.
	if got := visible(caretView(pen{}, style, value, 0, 10)); got != "long/path " {
		t.Fatalf("caret at end shows %q", got)
	}
	// At the start: the window is the head, so Home shows where the path begins.
	if got := visible(caretView(pen{}, style, value, len([]rune(value)), 10)); got != "/home/mass" {
		t.Fatalf("caret at start shows %q", got)
	}
	// Short values are drawn whole, the caret on the rune it sits before.
	if got := visible(caretView(pen{}, style, "abc", 1, 10)); got != "abc" {
		t.Fatalf("short value shows %q", got)
	}
}

// Through the form itself: an edited profile's name opens with the caret at
// its end, Home and typing prefix it, and Tab to another field starts that one
// at its end too.
func TestProfileFormEditsInsideAPrefilledField(t *testing.T) {
	m := tuiModel{mode: tuiForm, form: profileForm{name: "codex-work", command: "codex", field: 0}}
	for _, key := range []tea.KeyMsg{{Type: tea.KeyHome}, typed("my-")} {
		updated, _ := m.updateForm(key)
		m = updated.(tuiModel)
	}
	if m.form.name != "my-codex-work" {
		t.Fatalf("name = %q, want the prefix typed at the start", m.form.name)
	}
	updated, _ := m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	updated, _ = updated.(tuiModel).updateForm(tea.KeyMsg{Type: tea.KeyTab})
	updated, _ = updated.(tuiModel).updateForm(typed("x"))
	m = updated.(tuiModel)
	if m.form.command != "codexx" {
		t.Fatalf("command = %q, want typing at the end of the next field", m.form.command)
	}
}

// Moving the caret through a recalled argument set is not editing it, so the
// recall (what a pin acts on) survives; typing into it makes it a new set.
func TestArgumentPromptKeepsARecallUntilItIsEdited(t *testing.T) {
	m := tuiModel{mode: tuiParams, params: "--model x", argumentRow: 0}
	updated, _ := m.updateParams(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(tuiModel)
	if m.argumentRow != 0 || m.params != "--model x" {
		t.Fatalf("moving the caret dropped the recall: row %d, %q", m.argumentRow, m.params)
	}
	updated, _ = m.updateParams(typed("y"))
	m = updated.(tuiModel)
	if m.argumentRow != -1 || m.params != "--model yx" {
		t.Fatalf("typing = row %d, %q; want a new set with y before x", m.argumentRow, m.params)
	}
}

// The provider chip is not a text field: left and right still cycle it.
func TestProviderChipStillCyclesOnTheArrows(t *testing.T) {
	m := tuiModel{mode: tuiForm, form: profileForm{provider: "codex", command: "codex", field: formProviderField}}
	updated, _ := m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if got := updated.(tuiModel).form.provider; got == "codex" || strings.TrimSpace(got) == "" {
		t.Fatalf("right left the provider at %q", got)
	}
}
