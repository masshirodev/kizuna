package main

import (
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The TUI's text fields are plain strings with a caret beside them. The caret
// is stored as tail: how many runes sit after it. Counting from the end is what
// lets every field keep working unchanged where it is filled from code — a
// fresh model, a prefilled edit form, a recalled argument set all leave tail at
// zero, which is the end, where typing should continue. A tail longer than the
// value (the value was shortened elsewhere) is clamped, never trusted.

// caretIndex is the rune offset the caret sits at, clamped into the value.
func caretIndex(runes []rune, tail int) int {
	if tail < 0 || tail > len(runes) {
		tail = 0
	}
	return len(runes) - tail
}

// editLine applies one keystroke to a single-line field. It reports whether
// the key was an editing key; the caller handles anything else (enter, escape,
// moving between rows) as it did before.
//
// The keys are the ones a shell's line editor answers to: the arrows, Home and
// End, their readline spellings, word jumps, Delete, and the kill keys. Ctrl+U
// kills back to the start of the line; with the caret at the end that is the
// whole field, which is what it did before there was a caret.
func editLine(value string, tail int, msg tea.KeyMsg) (string, int, bool) {
	runes := []rune(value)
	at := caretIndex(runes, tail)
	set := func(next []rune, caret int) (string, int, bool) {
		return string(next), len(next) - caret, true
	}
	switch msg.String() {
	case "left", "ctrl+b":
		return set(runes, max(at-1, 0))
	case "right", "ctrl+f":
		return set(runes, min(at+1, len(runes)))
	case "home", "ctrl+a":
		return set(runes, 0)
	case "end", "ctrl+e":
		return set(runes, len(runes))
	case "alt+left", "ctrl+left", "alt+b":
		return set(runes, wordStart(runes, at))
	case "alt+right", "ctrl+right", "alt+f":
		return set(runes, wordEnd(runes, at))
	case "backspace", "ctrl+h":
		if at == 0 {
			return set(runes, at)
		}
		return set(append(runes[:at-1:at-1], runes[at:]...), at-1)
	case "delete", "ctrl+d":
		if at == len(runes) {
			return set(runes, at)
		}
		return set(append(runes[:at:at], runes[at+1:]...), at)
	case "ctrl+u":
		return set(runes[at:], 0)
	case "ctrl+k":
		return set(runes[:at], at)
	case "ctrl+w", "alt+backspace":
		from := wordStart(runes, at)
		return set(append(runes[:from:from], runes[at:]...), from)
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		typed := msg.Runes
		if msg.Type == tea.KeySpace && len(typed) == 0 {
			typed = []rune{' '}
		}
		next := make([]rune, 0, len(runes)+len(typed))
		next = append(append(append(next, runes[:at]...), typed...), runes[at:]...)
		return set(next, at+len(typed))
	}
	return value, tail, false
}

// wordStart is where a word jump or Ctrl+W lands going left: past any spaces
// before the caret, then to the start of the word before them.
func wordStart(runes []rune, at int) int {
	for at > 0 && unicode.IsSpace(runes[at-1]) {
		at--
	}
	for at > 0 && !unicode.IsSpace(runes[at-1]) {
		at--
	}
	return at
}

// wordEnd is where a word jump lands going right: past any spaces, then to the
// end of the word after them.
func wordEnd(runes []rune, at int) int {
	for at < len(runes) && unicode.IsSpace(runes[at]) {
		at++
	}
	for at < len(runes) && !unicode.IsSpace(runes[at]) {
		at++
	}
	return at
}

// caretView draws a field's value with the caret on the rune it sits before,
// or on a blank cell after the last one. A value wider than width is shown as
// the window that holds the caret, so moving to the start of a long path shows
// the start rather than a truncated end.
func caretView(ink pen, style lipgloss.Style, value string, tail, width int) string {
	runes := []rune(value)
	at := caretIndex(runes, tail)
	width = max(width, 1)
	// Positions run 0..len(runes); the last is the blank cell after the text.
	start := 0
	if at >= width {
		start = at - width + 1
	}
	end := min(start+width, len(runes)+1)
	before := string(runes[start:at])
	under := " "
	if at < len(runes) {
		under = string(runes[at])
	}
	after := ""
	if at+1 < end {
		after = string(runes[at+1 : min(end, len(runes))])
	}
	return ink.render(style, before) + cursorStyle.Render(under) + ink.render(style, after)
}
