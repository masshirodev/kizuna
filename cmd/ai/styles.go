package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
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
