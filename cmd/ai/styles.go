package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Adaptive colours keep the TUI readable on both light and dark terminals.
var (
	colorAccent  = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#C4B5FD"}
	colorText    = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	colorMuted   = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#9CA3AF"}
	colorFaint   = lipgloss.AdaptiveColor{Light: "#A1A1AA", Dark: "#52525B"}
	colorSuccess = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#6EE7B7"}
	colorInfo    = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	colorWarn    = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FCD34D"}
	colorDanger  = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#FCA5A5"}

	// The cockpit needs two tones below colorFaint that the stacked layout never
	// did: one for the hairlines that separate its columns, and one for the
	// detail beside a value — a reset time, a folder under a session title —
	// which has to stay subordinate to the thing it annotates.
	colorRule     = lipgloss.AdaptiveColor{Light: "#E4E4E7", Dark: "#1F1F24"}
	colorDim      = lipgloss.AdaptiveColor{Light: "#A1A1AA", Dark: "#3F3F46"}
	colorSelected = lipgloss.AdaptiveColor{Light: "#EDE9FE", Dark: "#181826"}
	colorInverse  = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0B0B0E"}

	// The gauge board adds three more. colorTrack is the unfilled run of a
	// gauge, which has to read as "the rest of the window" without competing
	// with the filled part; colorField is the well an input field sits in when
	// it is not the one being typed into; colorGhost is what the cockpit fades
	// to behind a box, so the question in front is the only thing in colour.
	colorTrack = lipgloss.AdaptiveColor{Light: "#E4E4E7", Dark: "#26262D"}
	colorField = lipgloss.AdaptiveColor{Light: "#F4F4F5", Dark: "#121217"}
	colorGhost = lipgloss.AdaptiveColor{Light: "#D4D4D8", Dark: "#2A2A31"}
)

var (
	appBadgeStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#7C3AED")).
			Padding(0, 1)

	headerTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	headerCountStyle   = lipgloss.NewStyle().Foreground(colorFaint)
	headerProfileStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	ruleStyle          = lipgloss.NewStyle().Foreground(colorRule)

	sectionLabelStyle = lipgloss.NewStyle().Foreground(colorFaint)
	dimStyle          = lipgloss.NewStyle().Foreground(colorDim)
	detailNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	separatorStyle    = lipgloss.NewStyle().Foreground(colorDim)

	columnHeaderStyle  = lipgloss.NewStyle().Foreground(colorFaint)
	authPresentStyle   = lipgloss.NewStyle().Foreground(colorSuccess)
	authKeyStyle       = lipgloss.NewStyle().Foreground(colorInfo)
	authMissingStyle   = lipgloss.NewStyle().Foreground(colorWarn)
	authUnknownStyle   = lipgloss.NewStyle().Foreground(colorFaint)
	modelStyle         = lipgloss.NewStyle().Foreground(colorMuted)
	unknownStyle       = lipgloss.NewStyle().Foreground(colorFaint)
	usageGoodStyle     = lipgloss.NewStyle().Foreground(colorSuccess)
	usageWarningStyle  = lipgloss.NewStyle().Foreground(colorWarn)
	usageCriticalStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDanger)

	cursorBarStyle   = lipgloss.NewStyle().Foreground(colorAccent)
	nameStyle        = lipgloss.NewStyle().Foreground(colorText)
	nameActiveStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	liveStyle        = lipgloss.NewStyle().Foreground(colorSuccess)
	emptyStateStyle  = lipgloss.NewStyle().Foreground(colorMuted).PaddingLeft(2)
	sectionTitle     = lipgloss.NewStyle().Bold(true).Foreground(colorText).PaddingLeft(1)
	fieldLabelStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	fieldLabelActive = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	fieldValueStyle  = lipgloss.NewStyle().Foreground(colorText)
	hintStyle        = lipgloss.NewStyle().Foreground(colorFaint)
	cursorStyle      = lipgloss.NewStyle().Reverse(true)

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorFaint).
			Padding(0, 2)

	dangerPanelStyle = panelStyle.BorderForeground(colorDanger)

	modalStyle = panelStyle.BorderForeground(colorAccent)

	helpKeyStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	helpDescStyle = lipgloss.NewStyle().Foreground(colorMuted)
	helpSepStyle  = lipgloss.NewStyle().Foreground(colorFaint)

	statusOKStyle    = lipgloss.NewStyle().Foreground(colorSuccess)
	statusErrStyle   = lipgloss.NewStyle().Foreground(colorDanger)
	statusInfoStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	updateStyle      = lipgloss.NewStyle().Foreground(colorWarn)
	dangerTextStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorDanger)
	confirmBodyStyle = lipgloss.NewStyle().Foreground(colorText)
)

// providerColor gives each provider a recognisable colour so a long profile
// list can be scanned by shape rather than read word by word.
func providerColor(provider string) lipgloss.TerminalColor {
	switch provider {
	case "codex":
		return lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	case "claude":
		return lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#FDBA74"}
	case "antigravity":
		return lipgloss.AdaptiveColor{Light: "#1A73E8", Dark: "#8AB4F8"}
	case "opencode":
		return lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#D8B4FE"}
	case "deepseek":
		return lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#93C5FD"}
	default:
		return colorMuted
	}
}

func providerStyle(provider string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(providerColor(provider))
}

// providerBadgeStyle reverses the provider colour into a chip. The detail column
// names one profile at a time, so the provider has to carry there without the
// neighbouring rows that make the colour alone legible in the table.
func providerBadgeStyle(provider string) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(colorInverse).Background(providerColor(provider)).Padding(0, 1)
}

// pen carries a row background through the cells of one line. lipgloss closes
// the pen at the end of every span, so a background wrapped around a finished
// row would survive only as far as its first coloured cell; it has to be part
// of each cell instead.
type pen struct {
	background lipgloss.TerminalColor
}

func (p pen) render(style lipgloss.Style, text string) string {
	if p.background != nil {
		style = style.Background(p.background)
	}
	return style.Render(text)
}

func selectedPen(selected bool) pen {
	if selected {
		return pen{background: colorSelected}
	}
	return pen{}
}

type helpEntry struct {
	key  string
	desc string
}

// keyGap spaces key hints apart. The gauge board separates them with space
// rather than dots, so a hint reads as a key and a verb, not as a list.
const keyGap = "   "

// renderKeys lays out key hints the way every box and the cockpit's own bar
// do: key in accent, verb muted, spaced.
func renderKeys(entries ...helpEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, helpKeyStyle.Render(entry.key)+" "+helpDescStyle.Render(entry.desc))
	}
	return strings.Join(parts, keyGap)
}

// renderKeysFit drops whole hints from the end until the row fits, for the
// same reason renderHelpFit does.
func renderKeysFit(entries []helpEntry, width int) string {
	for len(entries) > 1 {
		if rendered := renderKeys(entries...); lipgloss.Width(rendered) <= width {
			return rendered
		}
		entries = entries[:len(entries)-1]
	}
	return truncate(renderKeys(entries...), width)
}

// boxFooter is the last line of every box: what the keys do on the left and
// the way out on the right, always in the same place.
func boxFooter(width int, cancel helpEntry, entries ...helpEntry) string {
	right := renderKeys(cancel)
	return spread(renderKeysFit(entries, max(width-lipgloss.Width(right)-2, 8)), right, width)
}

// boxTitle is the uppercase accent heading every box opens with.
func boxTitle(title string) string {
	return boxTitleStyle.Render(strings.ToUpper(title))
}

var (
	boxTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	boxDangerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorDanger)
	ghostStyle          = lipgloss.NewStyle().Foreground(colorGhost)
	trackStyle          = lipgloss.NewStyle().Foreground(colorTrack)
)

// gauge draws what is left of a quota window as a thin bar: the filled run in
// the colour of how much is left, the rest as a faint track. Width zero draws
// nothing, which is how a narrow board drops its bars and keeps the figures.
func gauge(ink pen, percent, width int) string {
	if width <= 0 {
		return ""
	}
	filled := min(max(percent*width/100, 0), width)
	style := quotaStyle(percent)
	return ink.render(style, strings.Repeat("━", filled)) + ink.render(trackStyle, strings.Repeat("━", width-filled))
}

// ghost fades a rendered screen to one flat tone. The cockpit behind a box is
// context, not something to read, and in full colour it competes with the
// question the box is asking.
func ghost(screen string) string {
	lines := strings.Split(screen, "\n")
	for index, line := range lines {
		lines[index] = ghostStyle.Render(ansi.Strip(line))
	}
	return strings.Join(lines, "\n")
}

func renderHelp(entries []helpEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, helpKeyStyle.Render(entry.key)+" "+helpDescStyle.Render(entry.desc))
	}
	return strings.Join(parts, helpSepStyle.Render(" · "))
}

// renderHelpFit drops whole entries from the end until the bar fits. A key list
// cut mid-word advertises a key that is not there; a shorter list does not.
func renderHelpFit(entries []helpEntry, width int) string {
	for len(entries) > 1 {
		if rendered := renderHelp(entries); lipgloss.Width(rendered) <= width {
			return rendered
		}
		entries = entries[:len(entries)-1]
	}
	return truncate(renderHelp(entries), width)
}

func rule(width int) string {
	if width < 1 {
		return ""
	}
	return ruleStyle.Render(strings.Repeat("─", width))
}

// spread puts left and right on one line of the given width, right-aligned.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func pad(value string, width int) string {
	if gap := width - lipgloss.Width(value); gap > 0 {
		return value + strings.Repeat(" ", gap)
	}
	return value
}

func truncate(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	runes := []rune(value)
	return string(runes[:width-1]) + "…"
}
