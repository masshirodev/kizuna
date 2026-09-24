package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// The cockpit fills the alt screen with one frame: a title bar, the gauge board
// — every account as one row of a table sorted by headroom, with the selected
// one expanded in place — and a key bar. Everything else the TUI does is drawn
// as a box over that frame, so changing mode never moves what is behind it.
func (m tuiModel) View() string {
	width, height := m.width, m.height
	if width <= 0 {
		width = assumedWidth
	}
	if height <= 0 {
		height = assumedHeight
	}
	// The cockpit has its own floor it will not shrink below — chromeRows
	// plus at least one body row — so a terminal at or near that floor has no
	// room left for a margin without breaking the "the view exactly fills the
	// terminal" contract every size has to satisfy (TestViewFillsTheTerminalExactly
	// covers sizes down to 1x5). The margin shrinks toward zero there rather
	// than being reserved unconditionally.
	marginX := min(screenMarginX, max((width-1)/2, 0))
	marginY := min(screenMarginY, max((height-chromeRows-1)/2, 0))

	frame := frameLayout(width-2*marginX, height-2*marginY)
	screen := m.cockpitView(frame)
	if box := m.modalView(frame); box != "" {
		// The board behind a box is context, not something to act on: every key
		// is the box's until it closes, so the board fades rather than competing.
		screen = centerBox(ghost(screen), box, frame)
	}
	return withMargin(screen, width, marginX, marginY)
}

func (m tuiModel) cockpitView(frame layout) string {
	rows := make([]string, 0, frame.height)
	rows = append(rows, padLine(m.topBarView(frame), frame.width), rule(frame.width))
	rows = append(rows, m.boardView(frame)...)
	rows = append(rows, rule(frame.width), padLine(m.bottomBarView(frame), frame.width))
	return strings.Join(rows, "\n")
}

// topBarView answers the question the board exists for — which account has the
// most room, and which is nearly spent — before the eye reaches the table. The
// launch folder and any pending update sit on the right, where they apply to
// the whole screen rather than to a row.
func (m tuiModel) topBarView(frame layout) string {
	left := appBadgeStyle.Render("ai")
	if most, least, ok := m.headroomExtremes(); ok {
		left += "  " + sectionLabelStyle.Render("most headroom ") + m.extremeLabel(most)
		if least.Name != most.Name {
			candidate := left + separatorStyle.Render("   ·   ") + sectionLabelStyle.Render("lowest ") + m.extremeLabel(least)
			if lipgloss.Width(candidate) < frame.width {
				left = candidate
			}
		}
	} else if len(m.profiles) == 0 {
		left += "  " + sectionLabelStyle.Render("no profiles")
	}

	right := ""
	if folder := shortenHome(m.workingDir); m.workingDir != "" && fitsBeside(left, mutedStyle.Render(folder), frame.width) {
		right = mutedStyle.Render(folder)
	}
	if m.update.available() {
		notice := updateStyle.Render("↑ " + fmt.Sprint(m.update.Behind) + " behind " + updateBranch + "  U")
		candidate := notice
		if right != "" {
			candidate = right + separatorStyle.Render("  ·  ") + notice
		}
		if fitsBeside(left, candidate, frame.width) {
			right = candidate
		}
	}
	return spread(left, right, frame.width)
}

var mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)

func (m tuiModel) extremeLabel(profile Profile) string {
	percent := headroom(m.usage[profile.Name])
	return providerStyle(profile.Provider).Bold(true).Render(profile.Name) + " " + quotaStyle(percent).Render(fmt.Sprintf("%d%%", percent))
}

// headroomExtremes is the account with the most quota left and the one with
// the least, among those whose quota is actually known. An unknown remainder
// is neither, so a board with nothing measured names nothing.
func (m tuiModel) headroomExtremes() (Profile, Profile, bool) {
	var most, least Profile
	found := false
	for _, profile := range m.profiles {
		percent := headroom(m.usage[profile.Name])
		if percent < 0 {
			continue
		}
		if !found || percent > headroom(m.usage[most.Name]) {
			most = profile
		}
		if !found || percent < headroom(m.usage[least.Name]) {
			least = profile
		}
		found = true
	}
	return most, least, found
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

// bottomBarView carries five keys and the way to the rest. The old bar listed
// fifteen and dropped them from the end to fit, which hid exactly the keys
// nobody had learned; now every other key lives one space bar away.
func (m tuiModel) bottomBarView(frame layout) string {
	if m.searching {
		return spread(renderKeysFit([]helpEntry{{"type", "filter"}, {"↑↓", "choose"}, {"↵", "keep filter"}}, frame.width-12),
			renderKeys(helpEntry{"esc", "clear"}), frame.width)
	}
	right := renderKeys(helpEntry{"space", "all actions"}, helpEntry{"?", "keys"})
	entries := []helpEntry{{"↵", "run"}, {"R", "resume"}, {"H", "hand off"}, {"p", "args"}, {"/", "find"}}
	if len(m.profiles) == 0 {
		right = renderKeys(helpEntry{"q", "quit"})
		entries = []helpEntry{{"a", "add a profile"}}
	}
	if lipgloss.Width(right)+10 > frame.width {
		right = ""
	}
	left := renderKeysFit(entries, max(frame.width-lipgloss.Width(right)-len(keyGap), 8))
	return spread(left, right, frame.width)
}

// ---- the board ------------------------------------------------------------

// reportsQuota is whether a provider's CLI keeps a quota cache this launcher
// reads. The ones that do are ranked by it; the ones that do not have no
// number to rank by and are listed apart, rather than sorted last as if they
// were empty.
func reportsQuota(provider string) bool {
	return provider == "codex" || provider == "claude"
}

// boardOrder is the order the board lists accounts in, and so the order the
// cursor moves through: the accounts with a quota, most headroom first, then
// the ones without. Within a tie the incoming order (by name) is kept.
func boardOrder(profiles []Profile, usage map[string]usageRemaining) []Profile {
	var rated, unrated []Profile
	for _, profile := range profiles {
		if reportsQuota(profile.Provider) {
			rated = append(rated, profile)
		} else {
			unrated = append(unrated, profile)
		}
	}
	sort.SliceStable(rated, func(i, j int) bool {
		return headroom(usage[rated[i].Name]) > headroom(usage[rated[j].Name])
	})
	return append(rated, unrated...)
}

// boardColumns is the board's geometry at one width. Gauges take whatever the
// fixed columns leave, up to a length past which a longer bar says nothing
// more; below a usable length they go and the figures stay.
type boardColumns struct {
	name     int
	provider int
	gauge    int
	reset    bool
	auth     bool
	live     bool
}

const (
	boardMaxGauge = 32
	boardMinGauge = 6
	boardAuth     = 8
	boardLive     = 5
	boardResetW   = 7
	// boardIndent is where the detail under a row starts: past the cursor bar
	// and two more, so it reads as belonging to the row above.
	boardIndent = 4
)

// cell is one quota window: gauge, figure, and when it rolls over.
func (c boardColumns) cell() int {
	width := usageWidth
	if c.gauge > 0 {
		width += c.gauge + 1
	}
	if c.reset {
		width += 1 + boardResetW
	}
	return width
}

// fixed is everything but the gauges, with room for the space each gauge
// would need before its figure.
func (c boardColumns) fixed() int {
	width := 2 + c.name + c.provider + 2*(1+usageWidth) + 4
	if c.reset {
		width += 2 * (1 + boardResetW)
	}
	if c.auth {
		width += boardAuth
	}
	if c.live {
		width += boardLive
	}
	return width
}

func boardLayout(width int, profiles []Profile) boardColumns {
	longestName, longestProvider := 0, 0
	for _, profile := range profiles {
		longestName = max(longestName, lipgloss.Width(profile.Name))
		longestProvider = max(longestProvider, lipgloss.Width(profile.Provider))
	}
	c := boardColumns{
		name:     min(max(longestName+2, 12), 22),
		provider: min(max(longestProvider+2, 8), 13),
		reset:    true, auth: true, live: true,
	}
	for {
		spare := width - c.fixed()
		if gauge := min(spare/2, boardMaxGauge); gauge >= boardMinGauge {
			c.gauge = gauge
			return c
		}
		// Things go in the order a glance misses them least: the reset time
		// first, since the figure beside it is the useful half; then the live
		// count, which the expanded row repeats; then auth; then name width.
		switch {
		case spare >= 0:
			return c
		case c.reset:
			c.reset = false
		case c.live:
			c.live = false
		case c.auth:
			c.auth = false
		case c.name > 10:
			c.name = max(c.name+spare, 10)
		default:
			return c
		}
	}
}

// boardView is the body of the cockpit: the table, and the status line pinned
// to its bottom. A table taller than the body loses the detail under the
// selected row before it loses rows, and then scrolls to keep the cursor in
// view.
func (m tuiModel) boardView(frame layout) []string {
	visible := m.visibleProfiles()
	columns := boardLayout(frame.width, m.profiles)
	status := m.boardStatus(frame.width)
	rows := frame.body
	if status != "" && rows > 2 {
		rows--
	}

	lines, cursor := m.boardLines(visible, columns, frame.width, true)
	if len(lines) > rows {
		lines, cursor = m.boardLines(visible, columns, frame.width, false)
	}
	lines = windowRows(lines, cursor, rows)
	lines = padToRows(lines, rows, -1)
	if status != "" && frame.body > 2 {
		lines = append(lines, status)
	}
	for index, line := range lines {
		lines[index] = padLine(line, frame.width)
	}
	return lines[:min(len(lines), frame.body)]
}

// boardLines draws the table and reports which line the cursor is on. full
// asks for the selected row's whole expansion; without it the row keeps only
// the one line saying how it launches.
func (m tuiModel) boardLines(visible []Profile, c boardColumns, width int, full bool) ([]string, int) {
	lines := []string{m.boardHeader(c, width), ""}
	if len(visible) == 0 {
		if len(m.profiles) == 0 {
			return append(lines, emptyStateStyle.Render("No profiles yet — press a.")), 0
		}
		return append(lines, emptyStateStyle.Render("Nothing matches "+m.filter)), 0
	}
	cursor := 0
	unratedHeading := false
	for index, profile := range visible {
		if !reportsQuota(profile.Provider) && !unratedHeading {
			unratedHeading = true
			if index > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, "  "+sectionLabelStyle.Render("NO LOCAL QUOTA CACHE")+
				dimStyle.Render(truncate("   these providers keep none this launcher reads", max(width-24, 1))))
		}
		selected := index == m.cursor
		if selected {
			cursor = len(lines)
		}
		lines = append(lines, m.boardRow(profile, selected, c, width))
		if selected {
			lines = append(lines, m.expansion(profile, width, full)...)
		} else {
			lines = append(lines, m.nestedInstances(profile, width)...)
		}
	}
	return lines, cursor
}

func (m tuiModel) boardHeader(c boardColumns, width int) string {
	heading := sectionLabelStyle.Render("ACCOUNT")
	switch {
	case m.searching:
		heading = helpKeyStyle.Render("/") + fieldValueStyle.Render(m.filter) + cursorStyle.Render(" ")
	case m.filter != "":
		heading += "  " + helpKeyStyle.Render("/") + fieldValueStyle.Render(m.filter)
	}
	line := "  " + pad(heading, c.name+c.provider) +
		columnHeaderStyle.Render(pad(boardWindowLabel(c, "5-HOUR LEFT", "5H"), c.cell()+2)+
			pad(boardWindowLabel(c, "7-DAY LEFT", "7D"), c.cell()+2))
	if c.auth {
		line += columnHeaderStyle.Render(pad("AUTH", boardAuth))
	}
	if c.live {
		line += columnHeaderStyle.Render("LIVE")
	}
	return truncate(line, width)
}

func boardWindowLabel(c boardColumns, long, short string) string {
	if c.cell() >= len(long) {
		return long
	}
	return short
}

// boardRow is one account: who it is, what each window has left, whether it
// can log in, and how many instances it is running.
func (m tuiModel) boardRow(profile Profile, selected bool, c boardColumns, width int) string {
	ink := selectedPen(selected)
	bar, name := ink.render(lipgloss.NewStyle(), "  "), nameStyle
	if selected {
		bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
	}
	line := bar +
		ink.render(name, pad(truncate(profile.Name, c.name-1), c.name)) +
		ink.render(providerStyle(profile.Provider), pad(truncate(profile.Provider, c.provider-1), c.provider))
	cells := 2*c.cell() + 4
	if reportsQuota(profile.Provider) {
		usage, known := m.usage[profile.Name]
		line += m.windowCell(ink, usage.FiveHour, known, c) + ink.render(lipgloss.NewStyle(), "  ") +
			m.windowCell(ink, usage.Weekly, known, c) + ink.render(lipgloss.NewStyle(), "  ")
	} else {
		line += ink.render(dimStyle, pad(truncate("· not reported", cells-2), cells))
	}
	if c.auth {
		line += ink.render(lipgloss.NewStyle(), boardAuthCell(ink, profile))
	}
	if c.live {
		if running := m.runningCount(profile.Name); running > 0 {
			line += ink.render(liveStyle, fmt.Sprintf("▶ %d", running))
		}
	}
	return padStyled(ink, line, width)
}

// padStyled pads a row out to the width in the row's own background, so a
// selected row is tinted edge to edge rather than only as far as its text.
func padStyled(ink pen, line string, width int) string {
	if gap := width - lipgloss.Width(line); gap > 0 {
		return line + ink.render(lipgloss.NewStyle(), strings.Repeat(" ", gap))
	}
	return padLine(line, width)
}

// windowCell is one quota window on the board. The figure is what is left, so
// the gauge fills as the account gains headroom rather than as it is spent.
func (m tuiModel) windowCell(ink pen, window usageWindow, known bool, c boardColumns) string {
	cell := ""
	switch {
	case m.usage == nil:
		cell = ink.render(unknownStyle, pad("…", c.cell()))
		return cell
	case !known || !window.Known:
		if c.gauge > 0 {
			cell = ink.render(trackStyle, strings.Repeat("━", c.gauge)) + ink.render(lipgloss.NewStyle(), " ")
		}
		return cell + ink.render(unknownStyle, pad("—", c.cell()-lipgloss.Width(cell)))
	}
	if c.gauge > 0 {
		cell = gauge(ink, window.Percent, c.gauge) + ink.render(lipgloss.NewStyle(), " ")
	}
	cell += ink.render(usageStyle(window), pad(fmt.Sprintf("%d%%", window.Percent), usageWidth))
	if c.reset {
		cell += ink.render(dimStyle, " "+pad(truncate(resetAt(m.clock(), window.Resets), boardResetW), boardResetW))
	}
	return cell
}

// resetAt is formatReset without its verb: on the board the column heading
// already says what the time is.
func resetAt(now, resets time.Time) string {
	return strings.TrimPrefix(formatReset(now, resets), "resets ")
}

// boardAuthCell says whether the account can be launched as it is.
func boardAuthCell(ink pen, profile Profile) string {
	switch profileAuthState(profile) {
	case authPresent:
		return ink.render(authPresentStyle, pad("● ok", boardAuth))
	case authAPIKey:
		return ink.render(authKeyStyle, pad("● key", boardAuth))
	case authMissing:
		return ink.render(authMissingStyle, pad("○ login", boardAuth))
	default:
		return ink.render(authUnknownStyle, pad("· ?", boardAuth))
	}
}

// nestedInstances hangs what an unselected account is running under its row,
// so the live panel's answer sits beside the account it belongs to.
func (m tuiModel) nestedInstances(profile Profile, width int) []string {
	var lines []string
	for _, instance := range m.live {
		if instance.profile != profile.Name {
			continue
		}
		title := m.instanceTitle(instance)
		detail := fmt.Sprintf("   PID %d · %s · %s", instance.pid, shortenHome(instance.folder), formatUptime(instance.uptime(m.clock())))
		line := dimStyle.Render("      └ ") + liveStyle.Render("▶ ") + fieldValueStyle.Render(title) + dimStyle.Render(detail)
		lines = append(lines, truncateStyled(line, width))
	}
	return lines
}

// truncateStyled cuts a styled line to the width without splitting an escape.
func truncateStyled(line string, width int) string {
	if lipgloss.Width(line) <= width {
		return line
	}
	return padLine(line, width)
}

// expansion is everything about the account under the cursor, drawn in place
// under its row rather than in a column of its own: how it launches, what it
// is running, how busy it has been, and what it was last working on.
func (m tuiModel) expansion(profile Profile, width int, full bool) []string {
	ink := selectedPen(true)
	indent := strings.Repeat(" ", boardIndent)
	inner := max(width-boardIndent, 8)
	lines := []string{ink.render(lipgloss.NewStyle(), indent) + ink.render(modelStyle, truncate(m.launchSummary(profile), inner))}
	if full {
		lines = append(lines, "")
		leftWidth := min(60, inner*44/100)
		stacked := inner-leftWidth-3 < 40
		recentWidth := inner - leftWidth - 3
		if stacked {
			recentWidth = inner
		}
		left, right := m.expansionRunning(profile), m.expansionRecent(profile, recentWidth)
		if stacked {
			// Too narrow for the two side by side: the recent list goes under
			// what is running rather than being squeezed beside it.
			for _, line := range append(append(left, ""), right...) {
				lines = append(lines, indent+truncateStyled(line, inner))
			}
		} else {
			rightWidth := inner - leftWidth - 3
			for row := range max(len(left), len(right)) {
				first, second := "", ""
				if row < len(left) {
					first = left[row]
				}
				if row < len(right) {
					second = right[row]
				}
				lines = append(lines, indent+padLine(first, leftWidth)+"   "+padLine(second, rightWidth))
			}
		}
		lines = append(lines, "")
	}
	for index, line := range lines {
		lines[index] = tintLine(line, width)
	}
	return lines
}

// tintLine lays the selection tint under a line built from its own styles.
// Each span closes its pen, so the tint is re-opened after every reset rather
// than set once around the whole line.
func tintLine(line string, width int) string {
	line = padLine(line, width)
	sgr := selectedBackgroundSGR()
	if sgr == "" {
		return line
	}
	open := "\x1b[" + sgr + "m"
	return open + strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+open) + "\x1b[0m"
}

// selectedBackgroundSGR is the escape parameters for colorSelected as a
// background in the current colour profile, or nothing on a terminal without
// colour — where the whole tint is then a no-op.
func selectedBackgroundSGR() string {
	sample := lipgloss.NewStyle().Background(colorSelected).Render(" ")
	start := strings.Index(sample, "\x1b[")
	end := strings.Index(sample, "m")
	if start < 0 || end < start {
		return ""
	}
	return sample[start+2 : end]
}

// launchSummary is the selected account's settings on one line, in the order
// a launch uses them.
func (m tuiModel) launchSummary(profile Profile) string {
	indicator := profile.Indicator
	switch {
	case indicator == tmuxIndicator:
		indicator = "tmux status bar"
	case supportsNativeStatusLine(profile.Provider):
		indicator = "own status line"
	case indicator == "":
		indicator = "no indicator"
	}
	parts := []string{formatArguments(append([]string{profile.Command}, profile.DefaultArgs...)), cliField(profile)}
	if model := profileModel(profile); model != "" {
		parts = append(parts, "model "+model)
	}
	parts = append(parts, indicator)
	if profile.Notes != "" {
		parts = append(parts, profile.Notes)
	}
	return strings.Join(parts, "  ·  ")
}

func (m tuiModel) expansionRunning(profile Profile) []string {
	lines := []string{sectionLabelStyle.Render("RUNNING HERE")}
	running := 0
	for _, instance := range m.live {
		if instance.profile != profile.Name {
			continue
		}
		running++
		lines = append(lines,
			liveStyle.Render("▶ ")+fieldValueStyle.Render(m.instanceTitle(instance)),
			dimStyle.Render(fmt.Sprintf("  PID %d · %s · %s", instance.pid, shortenHome(instance.folder), formatUptime(instance.uptime(m.clock())))))
	}
	if running == 0 {
		lines = append(lines, unknownStyle.Render(m.pending("nothing running")))
	}
	// The histogram is labelled by what it counts, not as quota: no provider
	// records what a limit cost at a given hour, so this measures the one thing
	// that is actually on disk — sessions touched per hour.
	if m.activity.known() {
		spark := dimStyle.Render("24h ") + providerStyle(profile.Provider).Render(sparkline(m.activity.counts[:]))
		if !m.activity.peak.IsZero() {
			spark += dimStyle.Render("  peak " + m.activity.peak.Local().Format("15:04"))
		}
		lines = append(lines, "", spark)
	}
	return lines
}

// expansionRecentRows is how many conversations the expanded row lists. The
// resume picker is where the rest are; this is what the account was last on.
const expansionRecentRows = 4

func (m tuiModel) expansionRecent(profile Profile, width int) []string {
	lines := []string{sectionLabelStyle.Render("RECENT")}
	if len(m.recent) == 0 {
		lines = append(lines, unknownStyle.Render(m.pending("no recorded sessions")))
	}
	for index, record := range m.recent {
		if index == expansionRecentRows {
			break
		}
		title, style := record.session.title, fieldValueStyle
		if title == "" {
			title, style = "untitled session", unknownStyle
		}
		// The folder gives way before the title does: the title is what says
		// which conversation this is, the folder only where.
		folder := min(22, max(width-8-40, 10))
		titleWidth := min(38, max(width-8-folder, 10))
		line := dimStyle.Render(pad(formatWhen(m.clock(), record.activity()), 8)) +
			style.Render(pad(truncate(title, titleWidth-2), titleWidth)) +
			dimStyle.Render(truncate(shortenHome(record.folder), folder))
		// A session that was handed over carries where it went, reading forwards
		// because that is the only direction a pass has.
		if link, passed := m.lineage[record.session.id]; passed && record.session.id != "" {
			line += liveStyle.Render("  → " + link.TargetProfile)
		}
		lines = append(lines, line)
	}
	return append(lines, "", renderKeys(helpEntry{"R", "resume"}, helpEntry{"H", "hand off"}, helpEntry{"h", "open live"}))
}

// boardStatus is what the log shrank to: the last thing that happened, and
// when. A modal's own status is shown in the modal instead.
func (m tuiModel) boardStatus(width int) string {
	if len(m.log) == 0 {
		return ""
	}
	entry := m.log[0]
	icon, style := statusIcon(entry.kind)
	when := ""
	if !entry.at.IsZero() {
		when = "   " + dimStyle.Render(entry.at.Local().Format("15:04"))
	}
	return "  " + style.Render(icon) + " " + statusInfoStyle.Render(truncate(entry.text, max(width-12, 4))) + when
}

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

const detailLabelWidth = 12

func usageStyle(window usageWindow) lipgloss.Style {
	return quotaStyle(window.Percent)
}

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

// ---- boxes ----------------------------------------------------------------

// modalPadding is what modalStyle's own padding takes off the width handed to a
// content builder: lipgloss counts padding inside the width it is given, so a
// line built to the full width is a line that wraps.
const modalPadding = 4

const (
	// modalInset is what a box gives back to the frame it is centred over,
	// rather than what it takes: the board behind a box is the context the
	// question was asked from, and a box reaching the rules on both sides hides
	// it. Everything else is the box's, so a wider terminal grows the box until
	// it meets its own ceiling.
	modalInset = 6
	// modalMinWidth is the narrowest a box is drawn at. Below it the terminal is
	// narrower than the box, which centerBox handles by pinning it to the left
	// edge rather than by shrinking it further.
	modalMinWidth = 32
	// The ceilings each box grows to. A label and the value beside it read
	// worse spread across an ultrawide than they do in a column, so the boxes
	// that ask one question stop soonest; the ones with a list and a
	// conversation side by side go furthest.
	promptModalWidth  = 88
	editorModalWidth  = 92
	argsModalWidth    = 104
	paletteModalWidth = 120
	wizardModalWidth  = 124
	shareModalWidth   = 124
	// modalChromeRows is what a box spends on itself around its body: the
	// border, the heading and the blank under it, the key line at the foot,
	// and the status line with its own blank. A body sized past what is left
	// loses its last rows off the bottom of the frame.
	modalChromeRows = 10
)

// modalWidth sizes a box against the frame behind it: as wide as the terminal
// allows, up to the ceiling that box's own content earns.
func modalWidth(frame layout, ceiling int) int {
	return min(max(frame.width-modalInset, modalMinWidth), ceiling)
}

// modalRows is how many rows a box's body may draw. Unlike the width this is
// not a preference: a taller box is clamped against the bottom of the frame by
// centerBox and has the rows past it dropped.
func modalRows(frame layout) int {
	return max(frame.height-modalChromeRows, 4)
}

// modalCeiling is the width each mode's box grows to.
func modalCeiling(mode tuiMode) int {
	switch mode {
	case tuiRecent:
		return pickerModalWidth
	case tuiHandoff, tuiHandoffTo, tuiHandoffBrief:
		return wizardModalWidth
	case tuiShare:
		return shareModalWidth
	case tuiPalette:
		return paletteModalWidth
	case tuiParams:
		return argsModalWidth
	case tuiForm:
		return editorModalWidth
	}
	return promptModalWidth
}

// modalView is what the cockpit is covered with. Every mode but the list is a
// box: the frame behind it stays put, so answering a prompt never costs the
// context that prompted it.
//
// The status line rides inside the box rather than out on the cockpit, because
// a modal's error — a name that already exists, a quote left open — is the
// answer to what was just typed, and a panel it might be covering is the wrong
// place to answer from.
func (m tuiModel) modalView(frame layout) string {
	width := modalWidth(frame, modalCeiling(m.mode))
	// The boxes that show a conversation or a list are handed the whole height
	// the frame has left and fill it, whether or not what they are showing needs
	// it. They are the boxes whose content changes under the cursor, and a box
	// that sizes itself to the row it is on is a box that resizes on every arrow
	// key — which moves the rows the cursor is heading for.
	rows := modalRows(frame)
	status := m.statusLine(width)
	// The status line comes out of that height rather than being added to it, so
	// a message about the last keypress does not grow the box by two rows.
	if status != "" && fullHeightModal(m.mode) {
		rows = max(rows-statusRows, minBlockRows)
	}
	inner := max(width-modalPadding, 8)
	content, style := []string(nil), modalStyle
	switch m.mode {
	case tuiForm:
		content = m.formContent(inner)
	case tuiFolder:
		content = m.folderContent(inner)
	case tuiParams:
		content = m.paramsContent(inner, rows)
	case tuiClone:
		content = m.cloneContent(inner)
	case tuiShare:
		content = m.shareContent(inner, rows)
	case tuiHijack:
		content = m.confirmContent(inner)
	case tuiRecent:
		content = m.recentPicker(inner, rows)
	case tuiHandoff:
		content = m.handoffPicker(inner, rows)
	case tuiHandoffTo:
		content = m.handoffToContent(inner, rows)
	case tuiHandoffBrief:
		content = m.handoffBriefContent(inner, rows)
	case tuiConfirmInstall:
		content = m.installContent(inner)
	case tuiConfirmSelfUpdate:
		content = m.selfUpdateContent(inner)
	case tuiPalette:
		content = m.paletteContent(inner)
	case tuiConfirmDelete, tuiConfirmKill:
		content, style = m.confirmContent(inner), dangerPanelStyle
	}
	if len(content) == 0 {
		return ""
	}
	if status != "" {
		content = append(content, "", status)
	}
	return style.Width(width).Render(strings.Join(content, "\n"))
}

// statusRows is what a status costs a box: the line itself and the blank that
// separates it from the hint above.
const statusRows = 2

// minBlockRows is the least a list or a conversation is cut to and still says
// something: its heading and one line under it.
const minBlockRows = 2

// statusLine is the box's own report of how the last keypress landed, or
// nothing when there is none to make.
func (m tuiModel) statusLine(width int) string {
	if m.status == "" {
		return ""
	}
	icon, statusStyle := statusIcon(m.statusKind)
	return statusStyle.Render(icon + " " + truncate(m.status, max(width-2, 4)))
}

// fullHeightModal names the boxes that draw at the frame's height rather than
// at their content's: a list with a conversation read out beside it, and the
// steps of a handoff.
func fullHeightModal(mode tuiMode) bool {
	switch mode {
	case tuiRecent, tuiHandoff, tuiHandoffTo, tuiShare, tuiHandoffBrief:
		return true
	}
	return false
}
