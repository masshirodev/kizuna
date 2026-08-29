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
	ruleStyle          = lipgloss.NewStyle().Foreground(colorFaint)

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

// providerStyle gives each provider a recognisable colour so a long profile
// list can be scanned by shape rather than read word by word.
func providerStyle(provider string) lipgloss.Style {
	var color lipgloss.TerminalColor
	switch provider {
	case "codex":
		color = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#67E8F9"}
	case "claude":
		color = lipgloss.AdaptiveColor{Light: "#C2410C", Dark: "#FDBA74"}
	case "opencode":
		color = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#D8B4FE"}
	case "deepseek":
		color = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#93C5FD"}
	default:
		color = colorMuted
	}
	return lipgloss.NewStyle().Foreground(color)
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
