package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The cockpit fills the alt screen with one frame: a title bar, three columns —
// the accounts, the selected account, and what is running right now — and a key
// bar. Everything else the TUI does is drawn as a modal over that frame, so
// changing mode never moves what is behind it.
func (m tuiModel) View() string {
	frame := frameLayout(m.width, m.height)
	screen := m.cockpitView(frame)
	if box := m.modalView(frame); box != "" {
		screen = centerBox(screen, box, frame)
	}
	return screen
}

func (m tuiModel) cockpitView(frame layout) string {
	rows := make([]string, 0, frame.height)
	rows = append(rows, padLine(m.topBarView(frame), frame.width), rule(frame.width))
	rows = append(rows, m.bodyView(frame)...)
	rows = append(rows, rule(frame.width), padLine(m.bottomBarView(frame), frame.width))
	return strings.Join(rows, "\n")
}

// bodyView folds columns away rather than shrinking them. A profile table
// squeezed to eight characters is not a narrower cockpit, it is an unreadable
// one, so the live panel merges into the detail column first and the detail
// column stacks under the list after that.
func (m tuiModel) bodyView(frame layout) []string {
	switch frame.columns {
	case 3:
		list := append(m.listBlocks(frame.list), m.folderFooter(frame.list)...)
		return joinColumns(
			fitColumn(list, frame.body, frame.list),
			fitColumn(m.detailBlocks(frame.detail), frame.body, frame.detail),
			fitColumn(m.liveBlocks(frame.live), frame.body, frame.live),
		)
	case 2:
		list := append(m.listBlocks(frame.list), m.folderFooter(frame.list)...)
		detail := append(m.detailBlocks(frame.detail), m.liveBlocks(frame.detail)...)
		return joinColumns(
			fitColumn(list, frame.body, frame.list),
			fitColumn(detail, frame.body, frame.detail),
		)
	default:
		// Stacked, the folder belongs at the end of the whole column rather than
		// between the accounts and the account under the cursor.
		stacked := append(m.listBlocks(frame.list), m.detailBlocks(frame.list)...)
		stacked = append(stacked, m.liveBlocks(frame.list)...)
		return fitColumn(append(stacked, m.folderFooter(frame.list)...), frame.body, frame.list)
	}
}

func joinColumns(columns ...[]string) []string {
	rows := 0
	for _, column := range columns {
		rows = max(rows, len(column))
	}
	divider := " " + ruleStyle.Render("│") + " "
	lines := make([]string, rows)
	for row := range lines {
		parts := make([]string, 0, len(columns))
		for _, column := range columns {
			if row < len(column) {
				parts = append(parts, column[row])
			}
		}
		lines[row] = strings.Join(parts, divider)
	}
	return lines
}

// topBarView names the account in play and the state that applies to the whole
// screen. The update notice lives here rather than on a line of its own, so a
// build that is behind is visible without costing a row of the body.
func (m tuiModel) topBarView(frame layout) string {
	left := appBadgeStyle.Render("ai") + " " + sectionLabelStyle.Render("profiles")
	if profile, ok := m.selectedProfile(); ok {
		left += separatorStyle.Render(" › ") + headerProfileStyle.Render(profile.Name)
		if running := m.runningCount(profile.Name); running > 0 {
			left += separatorStyle.Render(" › ") + liveStyle.Render(plural(running, "instance"))
		}
	}

	count := plural(len(m.profiles), "profile")
	if len(m.profiles) == 0 {
		count = "no profiles"
	}
	right := headerCountStyle.Render(count)
	if folder := shortenHome(m.workingDir); folder != "" {
		if candidate := right + separatorStyle.Render(" · ") + headerCountStyle.Render(folder); fitsBeside(left, candidate, frame.width) {
			right = candidate
		}
	}
	if m.update.available() {
		notice := updateStyle.Render("↑ " + plural(m.update.Behind, "commit") + " behind " + updateBranch + " · U")
		if candidate := right + separatorStyle.Render(" · ") + notice; fitsBeside(left, candidate, frame.width) {
			right = candidate
		}
	}
	return spread(left, right, frame.width)
}

func fitsBeside(left, right string, width int) bool {
	return lipgloss.Width(left)+lipgloss.Width(right)+2 <= width
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

// listBlocks draws the accounts column: the scannable table, the authentication
// each profile has, and the launch folder pinned to the bottom. Auth is its own
// block rather than another table column because at forty characters the table
// can carry either auth or the two quota figures, and the quota is what changes
// hour to hour.
func (m tuiModel) listBlocks(width int) []block {
	visible := m.visibleProfiles()
	blocks := []block{textBlock(dropNever, m.profileTableLines(visible, width)...)}
	if len(visible) > 0 {
		blocks = append(blocks, textBlock(dropAuth, m.authLines(visible, width)...))
	}
	return blocks
}

// folderFooter is pinned to the bottom of its column by the flex before it. The
// launch folder is the one piece of state a launch silently depends on, so it
// sits where the eye lands last rather than scrolling off with the panels above.
func (m tuiModel) folderFooter(width int) []block {
	return []block{flexBlock(), textBlock(dropNever, m.folderLines(width)...)}
}

func (m tuiModel) profileTableLines(visible []Profile, width int) []string {
	heading := sectionLabelStyle.Render("PROFILES")
	if m.searching {
		heading = helpKeyStyle.Render("/") + fieldValueStyle.Render(m.filter) + cursorStyle.Render(" ")
	}
	width = tableWidth(width)
	nameWidth, providerWidth := listColumns(width, m.profiles)
	header := spread(heading, columnHeaderStyle.Render(pad("5H", usageWidth)+" "+pad("7D", usageWidth)), width)
	lines := []string{header}

	if len(visible) == 0 {
		if len(m.profiles) == 0 {
			return append(lines, emptyStateStyle.Render("No profiles yet — press a."))
		}
		return append(lines, emptyStateStyle.Render("Nothing matches "+m.filter))
	}
	for index, profile := range visible {
		selected := index == m.cursor
		ink := selectedPen(selected)
		bar, name := ink.render(lipgloss.NewStyle(), "  "), nameStyle
		if selected {
			bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
		}
		left := bar +
			ink.render(name, pad(truncate(profile.Name, nameWidth), nameWidth)) + " " +
			ink.render(providerStyle(profile.Provider), pad(truncate(profile.Provider, providerWidth), providerWidth))
		right := m.usageCellWith(ink, profile, fiveHourWindow) + " " + m.usageCellWith(ink, profile, weeklyWindow)
		gap := max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)
		lines = append(lines, left+ink.render(lipgloss.NewStyle(), strings.Repeat(" ", gap))+right)
	}
	return lines
}

// tableWidth caps how far the accounts table is stretched. In one-column mode
// the list gets the whole terminal, and a table spread across it puts the
// provider adrift in the middle of a line of whitespace.
func tableWidth(width int) int {
	return min(width, wideListColumn+4)
}

// listColumns splits the accounts column between the name and the provider,
// with the two quota cells taking a fixed share. The name is what identifies an
// account, so it gets whatever the provider does not need.
func listColumns(width int, profiles []Profile) (int, int) {
	provider := 6
	for _, profile := range profiles {
		provider = max(provider, lipgloss.Width(profile.Provider))
	}
	provider = min(provider, 11)
	// Past a point a longer name column is only whitespace: profile names are
	// short, and the quota cells read better against the column's right edge
	// than pushed out by a name box nobody fills.
	name := min(max(width-provider-2*usageWidth-5, 6), 26)
	return name, provider
}

func (m tuiModel) authLines(visible []Profile, width int) []string {
	width = tableWidth(width)
	nameWidth := max(width-authWidth-1, 6)
	lines := []string{sectionLabelStyle.Render("AUTH")}
	for _, profile := range visible {
		lines = append(lines, fieldLabelStyle.Render(pad(truncate(profile.Name, nameWidth), nameWidth))+" "+authCell(profile))
	}
	return lines
}

func (m tuiModel) folderLines(width int) []string {
	folder := shortenHome(m.workingDir)
	if folder == "" {
		folder = "—"
	}
	return []string{
		sectionLabelStyle.Render("FOLDER") + " " + fieldValueStyle.Render(truncate(folder, max(width-7, 1))),
		strings.Repeat(" ", 7) + dimStyle.Render("press ") + helpKeyStyle.Render("c") + dimStyle.Render(" to change"),
	}
}

// detailBlocks draws everything about the account under the cursor: how it
// launches, what quota it has left, how busy it has been, and what it was last
// working on.
func (m tuiModel) detailBlocks(width int) []block {
	profile, ok := m.selectedProfile()
	if !ok {
		return []block{textBlock(dropNever, emptyStateStyle.Render("Nothing selected."))}
	}
	blocks := []block{textBlock(dropNever, m.headlineLines(profile, width)...)}
	blocks = append(blocks, textBlock(dropNever, m.quotaLines(profile, width)...))
	if activity := m.activityLines(width); len(activity) > 0 {
		blocks = append(blocks, textBlock(dropActivity, activity...))
	}
	return append(blocks, textBlock(dropRecent, m.recentLines(width)...))
}

func (m tuiModel) headlineLines(profile Profile, width int) []string {
	title := detailNameStyle.Render(profile.Name) + "  " + providerBadgeStyle(profile.Provider).Render(strings.ToUpper(profile.Provider))
	if running := m.runningCount(profile.Name); running > 0 {
		badge := "▶ running"
		if running > 1 {
			badge = fmt.Sprintf("▶ %d running", running)
		}
		title += "  " + liveStyle.Render(badge)
	}

	indicator := profile.Indicator
	switch {
	case indicator == tmuxIndicator:
		indicator = "tmux status bar"
	case supportsNativeStatusLine(profile.Provider):
		indicator = "own status line"
	case indicator == "":
		indicator = "none"
	}
	fields := [][2]string{
		{"launch", formatArguments(append([]string{profile.Command}, profile.DefaultArgs...))},
		{"cli", cliField(profile)},
		{"model", profileModel(profile)},
		{"indicator", indicator},
		{"note", profile.Notes},
	}
	lines := []string{title, ""}
	for _, field := range fields {
		value, style := field[1], fieldValueStyle
		if value == "" {
			value, style = "—", unknownStyle
		}
		lines = append(lines, fieldLabelStyle.Render(pad(field[0], detailLabelWidth))+
			style.Render(truncate(value, max(width-detailLabelWidth, 1))))
	}
	return lines
}

const detailLabelWidth = 12

// cliField says where the command a launch would run actually is. A profile can
// be fully configured and logged in and still fail at launch because the
// provider's CLI was never installed, and exec's own message for that names
// neither the profile nor the way out.
func cliField(profile Profile) string {
	if path := commandPath(profile.Command); path != "" {
		return shortenHome(path)
	}
	if _, err := providerInstaller(profile.Provider); err == nil {
		return "not installed — press i"
	}
	return "not installed"
}

func (m tuiModel) quotaLines(profile Profile, width int) []string {
	usage, known := m.usage[profile.Name]
	lines := []string{sectionLabelStyle.Render("QUOTA")}
	if m.usage == nil {
		return append(lines, unknownStyle.Render("reading the provider's own quota cache…"))
	}
	if !known || !usage.known() {
		return append(lines, unknownStyle.Render("no quota recorded for this profile"))
	}
	return append(lines,
		m.meterLine("5H", usage.FiveHour, width),
		m.meterLine("7D", usage.Weekly, width))
}

// meterLine draws one quota window as a bar, a percentage, and when it rolls
// over. The percentage is what is left, so the bar fills as the account gets
// more headroom rather than as it is spent.
func (m tuiModel) meterLine(label string, window usageWindow, width int) string {
	if !window.Known {
		return fieldLabelStyle.Render(pad(label, 4)) + unknownStyle.Render("—")
	}
	reset := formatReset(m.clock(), window.Resets)
	bar := max(width-4-usageWidth-2-lipgloss.Width(reset)-1, 6)
	filled := min(max(window.Percent*bar/100, 0), bar)
	style := usageStyle(window)
	line := fieldLabelStyle.Render(pad(label, 4)) +
		style.Render(strings.Repeat("█", filled)+strings.Repeat("░", bar-filled)) +
		" " + style.Render(pad(fmt.Sprintf("%d%%", window.Percent), usageWidth))
	if reset != "" {
		line += " " + dimStyle.Render(reset)
	}
	return line
}

func usageStyle(window usageWindow) lipgloss.Style {
	switch {
	case window.Percent <= 10:
		return usageCriticalStyle
	case window.Percent <= 25:
		return usageWarningStyle
	default:
		return usageGoodStyle
	}
}

// activityLines draws sessions touched per hour over the last day. It is
// labelled ACTIVITY and not quota on purpose: no provider records what a limit
// cost at a given hour, so this counts the one thing that is actually on disk.
func (m tuiModel) activityLines(width int) []string {
	if !m.activity.known() || width < activityHours+8 {
		return nil
	}
	line := dimStyle.Render(pad("24h", 4)) + sectionLabelStyle.Render(sparkline(m.activity.counts[:]))
	if !m.activity.peak.IsZero() {
		line += "  " + dimStyle.Render("peak "+m.activity.peak.Local().Format("15:04"))
	}
	return []string{sectionLabelStyle.Render("ACTIVITY"), line}
}

func (m tuiModel) recentLines(width int) []string {
	lines := []string{sectionLabelStyle.Render("RECENT SESSIONS")}
	if len(m.recent) == 0 {
		return append(lines, unknownStyle.Render(m.pending("no recorded sessions")))
	}
	for _, record := range m.recent {
		lines = append(lines, m.recentRow(pen{}, record, width))
	}
	return append(lines, dimStyle.Render("press ")+helpKeyStyle.Render("R")+dimStyle.Render(" to resume one"))
}

// recentRow lays one recorded conversation out as when, what, and where. The
// panel and the resume picker share it, so the row a key is pressed on is
// exactly the row that was read.
func (m tuiModel) recentRow(ink pen, record recordedSession, width int) string {
	folderWidth := min(max(width/3, 10), 24)
	titleWidth := max(width-recentTimeWidth-folderWidth-2, 8)
	title, style := record.session.title, fieldValueStyle
	if title == "" {
		title, style = "untitled session", unknownStyle
	}
	return ink.render(dimStyle, pad(formatWhen(m.clock(), record.when), recentTimeWidth)) +
		ink.render(dimStyle, " ") +
		ink.render(style, pad(truncate(title, titleWidth), titleWidth)) +
		ink.render(dimStyle, " ") +
		ink.render(dimStyle, pad(truncate(shortenHome(record.folder), folderWidth), folderWidth))
}

const recentTimeWidth = 6

// formatWhen dates a past session in the shortest form that still separates it
// from the others on screen: a clock time today, a word yesterday, a date
// before that.
func formatWhen(now, when time.Time) string {
	if when.IsZero() {
		return "—"
	}
	local := when.Local()
	today := now.Local().Truncate(24 * time.Hour)
	switch day := local.Truncate(24 * time.Hour); {
	case day.Equal(today):
		return local.Format("15:04")
	case day.Equal(today.AddDate(0, 0, -1)):
		return "yest."
	default:
		return local.Format("2 Jan")
	}
}

// liveBlocks answers the question the profile table cannot: not which accounts
// exist, but which of them are running something right now, and where.
func (m tuiModel) liveBlocks(width int) []block {
	return []block{
		textBlock(dropNever, m.runningLines(width)...),
		textBlock(dropLog, m.logLines(width)...),
	}
}

func (m tuiModel) runningLines(width int) []string {
	heading := "RUNNING"
	if len(m.live) > 0 {
		heading = fmt.Sprintf("RUNNING · %d TOTAL", len(m.live))
	}
	lines := []string{sectionLabelStyle.Render(heading)}
	if len(m.live) == 0 {
		return append(lines, unknownStyle.Render(m.pending("nothing running")))
	}
	selected, _ := m.selectedProfile()
	for index, instance := range m.live {
		if index > 0 {
			lines = append(lines, "")
		}
		bar := "  "
		if instance.profile == selected.Name {
			bar = cursorBarStyle.Render("▌ ")
		}
		lines = append(lines,
			bar+providerStyle(m.providerOf(instance.profile)).Render(truncate(instance.profile, max(width-14, 4)))+
				separatorStyle.Render(" · ")+dimStyle.Render(fmt.Sprintf("PID %d", instance.pid)),
			"  "+fieldValueStyle.Render(truncate(m.instanceTitle(instance), max(width-2, 4))),
			"  "+dimStyle.Render(truncate(shortenHome(instance.folder)+" · "+formatUptime(instance.uptime(m.clock())), max(width-2, 4))))
	}
	return lines
}

// runningCount answers from the live panel rather than by counting lock
// directories again. Rendering a frame is not the place to touch the disk, and
// the two would only ever disagree by one refresh anyway.
func (m tuiModel) runningCount(name string) int {
	count := 0
	for _, instance := range m.live {
		if instance.profile == name {
			count++
		}
	}
	return count
}

// pending keeps an opening frame from answering a question it has not asked
// yet. An empty panel before the first read means "still looking", and saying
// "nothing running" then would be a claim rather than a report.
func (m tuiModel) pending(answer string) string {
	if !m.loaded {
		return "…"
	}
	return answer
}

func (m tuiModel) providerOf(name string) string {
	for _, profile := range m.profiles {
		if profile.Name == name {
			return profile.Provider
		}
	}
	return ""
}

// logLines keeps the last few things that happened. A single status line loses
// the reason a launch failed the moment the next key is pressed, which is
// exactly when the user goes looking for it.
func (m tuiModel) logLines(width int) []string {
	lines := []string{sectionLabelStyle.Render("LOG")}
	if len(m.log) == 0 {
		return append(lines, unknownStyle.Render("nothing yet"))
	}
	for _, entry := range m.log {
		icon, style := statusIcon(entry.kind)
		lines = append(lines, style.Render(icon)+" "+statusInfoStyle.Render(truncate(entry.text, max(width-2, 4))))
	}
	return lines
}

func (m tuiModel) bottomBarView(frame layout) string {
	right := renderHelp([]helpEntry{{"q", "quit"}})
	if m.mode == tuiList && !m.searching {
		right = renderHelp([]helpEntry{{"/", "search"}, {"?", "keys"}, {"q", "quit"}})
	}
	left := renderHelpFit(m.helpEntries(), max(frame.width-lipgloss.Width(right)-2, 8))
	return spread(left, right, frame.width)
}

// modalPadding is what modalStyle's own padding takes off the width handed to a
// content builder: lipgloss counts padding inside the width it is given, so a
// line built to the full width is a line that wraps.
const modalPadding = 4

// modalView is what the cockpit is covered with. Every mode but the list is a
// box: the frame behind it stays put, so answering a prompt never costs the
// context that prompted it.
//
// The status line rides inside the box rather than out on the cockpit, because
// a modal's error — a name that already exists, a quote left open — is the
// answer to what was just typed, and a panel it might be covering is the wrong
// place to answer from.
func (m tuiModel) modalView(frame layout) string {
	width := min(max(frame.width-16, 32), 72)
	if m.mode == tuiHelp {
		width = min(max(frame.width-16, 32), helpModalWidth)
	}
	content, style := []string(nil), modalStyle
	switch m.mode {
	case tuiForm:
		content = m.formContent()
	case tuiFolder:
		content = m.folderContent()
	case tuiParams:
		content = m.paramsContent()
	case tuiHijack:
		content = m.confirmContent(width)
	case tuiRecent:
		content = m.recentPicker(width)
	case tuiConfirmInstall:
		content = m.installContent(width)
	case tuiConfirmSelfUpdate:
		content = m.selfUpdateContent(width)
	case tuiHelp:
		content = m.helpPane(width)
	case tuiConfirmDelete, tuiConfirmKill:
		content, style = m.confirmContent(width), dangerPanelStyle
	}
	if len(content) == 0 {
		return ""
	}
	if m.status != "" {
		icon, statusStyle := statusIcon(m.statusKind)
		content = append(content, "", statusStyle.Render(icon+" "+truncate(m.status, max(width-2, 4))))
	}
	return style.Width(width).Render(strings.Join(content, "\n"))
}
