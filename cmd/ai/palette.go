package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The palette replaces the key pane. The bottom bar carries five keys; every
// other one is here, grouped by what it acts on — a conversation, the profile,
// the provider's CLI, or ai-session itself — with what it would act on right
// now written beside it, so the list answers "can I" as well as "how".

// paletteAction is one thing the palette can do. key is what the board shows
// and what a filter matches exactly; press is the keystroke it stands for,
// which is what running it from here sends to the board.
type paletteAction struct {
	key   string
	press string
	desc  string
}

type paletteGroup struct {
	title   string
	actions []paletteAction
}

// paletteColumns is the palette as it is drawn: two columns of groups.
func paletteColumns() [][]paletteGroup {
	return [][]paletteGroup{
		{
			{"CONVERSATION", []paletteAction{
				{"↵", "enter", "run it here"},
				{"p", "p", "run with arguments"},
				{"R", "R", "resume a conversation"},
				{"h", "h", "open a live instance"},
				{"H", "H", "hand off to another account"},
			}},
			{"PROFILE", []paletteAction{
				{"a", "a", "add"},
				{"e", "e", "edit"},
				{"C", "C", "clone its setup"},
				{"x", "x", "delete"},
				{"/", "/", "find"},
			}},
		},
		{
			{"PROVIDER CLI", []paletteAction{
				{"l", "l", "log in"},
				{"i", "i", "install the CLI"},
				{"u", "u", "update the CLI"},
				{"K", "K", "stop an instance"},
				{"m", "m", "install MCP servers"},
				{"s", "s", "install skills"},
			}},
			{"AI-SESSION", []paletteAction{
				{"c", "c", "change launch folder"},
				{"A", "A", "auto-swap on handoff"},
				{"r", "r", "refresh quotas and updates"},
				{"U", "U", "update ai-session"},
				{"q", "q", "quit"},
			}},
		},
	}
}

// paletteMatches is every action the filter lets through, in drawing order.
// A filter that is exactly an action's key matches that action; anything else
// is matched against what the actions do.
func (m tuiModel) paletteMatches() []paletteAction {
	var matches []paletteAction
	for _, column := range paletteColumns() {
		for _, group := range column {
			for _, action := range group.actions {
				if m.paletteAccepts(action) {
					matches = append(matches, action)
				}
			}
		}
	}
	return matches
}

func (m tuiModel) paletteAccepts(action paletteAction) bool {
	query := strings.TrimSpace(m.paletteFilter)
	if query == "" || query == action.key {
		return true
	}
	return strings.Contains(strings.ToLower(action.desc), strings.ToLower(query))
}

func (m *tuiModel) openPalette() {
	m.mode = tuiPalette
	m.paletteFilter, m.paletteRow = "", 0
	m.clearStatus()
}

// settlePaletteRow puts the highlight on the action whose key was typed, when
// one was: typing a key and pressing Enter does what pressing that key on the
// board would have.
func (m *tuiModel) settlePaletteRow() {
	query := strings.TrimSpace(m.paletteFilter)
	m.paletteRow = 0
	for index, action := range m.paletteMatches() {
		if action.key == query {
			m.paletteRow = index
			return
		}
	}
}

func (m tuiModel) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	matches := m.paletteMatches()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = tuiList
		return m, nil
	case "up", "ctrl+p":
		if m.paletteRow > 0 {
			m.paletteRow--
		}
		return m, nil
	case "down", "ctrl+n", "tab":
		if m.paletteRow < len(matches)-1 {
			m.paletteRow++
		}
		return m, nil
	case "enter":
		if m.paletteRow < 0 || m.paletteRow >= len(matches) {
			return m, nil
		}
		m.mode = tuiList
		return m.updateList(pressedKey(matches[m.paletteRow].press))
	case "backspace", "ctrl+h":
		if runes := []rune(m.paletteFilter); len(runes) > 0 {
			m.paletteFilter = string(runes[:len(runes)-1])
		}
	case "ctrl+u":
		m.paletteFilter = ""
	default:
		if msg.Type != tea.KeyRunes && msg.Type != tea.KeySpace {
			return m, nil
		}
		m.paletteFilter += string(msg.Runes)
	}
	m.settlePaletteRow()
	return m, nil
}

// pressedKey is the keystroke an action stands for.
func pressedKey(press string) tea.KeyMsg {
	if press == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(press)}
}

const (
	paletteKeyWidth  = 7
	paletteDescWidth = 28
	// paletteColumnMin is the narrowest a column of the palette is drawn at.
	// Below two of these the columns stack rather than clip, because a key
	// list cut to fit is missing exactly the keys nobody has learned yet.
	paletteColumnMin = 48
)

// paletteContent is the filter on top, what the actions would act on, and the
// groups in two columns — one when the box is too narrow for both.
func (m tuiModel) paletteContent(width int) []string {
	ink := selectedPen(true)
	field := ink.render(helpKeyStyle.Bold(true), "› ") + ink.render(fieldValueStyle, m.paletteFilter) + cursorStyle.Render(" ")
	if m.paletteFilter == "" {
		field += ink.render(dimStyle, "  type to filter, or type a key and press ↵")
	}
	lines := []string{padStyled(ink, truncateStyled(field, width), width)}
	if profile, ok := m.selectedProfile(); ok {
		lines = append(lines, dimStyle.Render("acting on ")+providerStyle(profile.Provider).Bold(true).Render(profile.Name))
	} else {
		lines = append(lines, dimStyle.Render("no profile selected — most of these need one"))
	}
	lines = append(lines, "")

	selected := ""
	if matches := m.paletteMatches(); m.paletteRow >= 0 && m.paletteRow < len(matches) {
		selected = matches[m.paletteRow].key
	}
	columns := paletteColumns()
	if width < 2*paletteColumnMin+dividerWidth {
		for index, column := range columns {
			if index > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.paletteColumn(column, width, selected)...)
		}
	} else {
		left := (width - dividerWidth) / 2
		right := width - dividerWidth - left
		first, second := m.paletteColumn(columns[0], left, selected), m.paletteColumn(columns[1], right, selected)
		lines = append(lines, joinPanes(first, second, left, right, max(len(first), len(second)))...)
	}
	if len(m.paletteMatches()) == 0 {
		lines = append(lines, emptyStateStyle.Render("Nothing matches "+m.paletteFilter))
	}
	return append(lines, "", boxFooter(width, helpEntry{"esc", "close"}, helpEntry{"↑↓", "choose"}, helpEntry{"↵", "do it"}))
}

func (m tuiModel) paletteColumn(groups []paletteGroup, width int, selected string) []string {
	var lines []string
	for _, group := range groups {
		var rows []string
		for _, action := range group.actions {
			if !m.paletteAccepts(action) {
				continue
			}
			ink := selectedPen(action.key == selected)
			note, noteStyle := m.paletteNote(action.key)
			row := ink.render(helpKeyStyle, pad(action.key, paletteKeyWidth)) +
				ink.render(fieldValueStyle, pad(action.desc, paletteDescWidth)) +
				ink.render(noteStyle, truncate(note, max(width-paletteKeyWidth-paletteDescWidth, 1)))
			rows = append(rows, padStyled(ink, row, width))
		}
		if len(rows) == 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, sectionLabelStyle.Render(group.title))
		lines = append(lines, rows...)
	}
	return lines
}

// paletteNote is the live context beside an action: what it would act on, or
// why it would not work right now.
func (m tuiModel) paletteNote(key string) (string, lipgloss.Style) {
	profile, selected := m.selectedProfile()
	switch key {
	case "↵":
		return shortenHome(m.workingDir), dimStyle
	case "R":
		if selected && len(m.recent) > 0 {
			return fmt.Sprintf("%d recorded", len(m.recent)), dimStyle
		}
	case "h", "K":
		if running := m.runningCount(profile.Name); selected && running > 0 {
			return fmt.Sprintf("%d running", running), liveStyle
		}
		return "nothing running", unknownStyle
	case "H":
		if percent := headroom(m.usage[profile.Name]); selected && percent >= 0 {
			return fmt.Sprintf("5h at %d%%", percent), quotaStyle(percent)
		}
	case "C":
		return "no credentials", dimStyle
	case "l":
		if !selected {
			break
		}
		switch profileAuthState(profile) {
		case authPresent:
			return "logged in", liveStyle
		case authAPIKey:
			return "api key", authKeyStyle
		case authMissing:
			return "not logged in", authMissingStyle
		}
	case "i":
		if selected {
			return cliField(profile), dimStyle
		}
	case "A":
		if m.autoSwap {
			return "on", liveStyle
		}
		return "off", sectionLabelStyle
	case "U":
		if m.update.available() {
			return fmt.Sprintf("%d behind %s", m.update.Behind, updateBranch), updateStyle
		}
	}
	return "", dimStyle
}
