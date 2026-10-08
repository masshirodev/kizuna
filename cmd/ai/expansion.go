package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The selected account expands in place, at one of three levels. The glance is
// what moving the cursor shows: enough to tell the account apart and see what
// it is doing. The sheet, on `tab`, is everything the board knows about it,
// laid out in columns. The compact form is the one line a board too short for
// either still has room for. The board asks for the sheet (when it is open),
// then the glance, then the compact line, and draws the first that fits.
type expansionLevel int

const (
	expandCompact expansionLevel = iota
	expandGlance
	expandSheet
)

// The expansion's columns, from the Board expansion handoff at the board's
// full 146-column frame, measured from the indent. They are hand-set in the
// mock and pinned by TestBoardMatchesTheExpansionDesign, so a tidy-up that
// rounds them redraws the screen and fails the test.
const (
	// glanceRight is where the glance's second column (MODEL, ENV, HAS)
	// starts, counted back from the right edge so it holds at any width.
	glanceRightWidth = 62
	glanceLabel      = 6
	glanceModel      = 13
	// paneLeft is the running pane; the recent pane takes the rest.
	paneLeft = 57
	// The narrowest each pane and launch column gets before they stack: the
	// running pane gives way first, down to minRunningPane, and then the
	// recent pane moves underneath rather than being squeezed further.
	minRecentPane  = 48
	minRunningPane = 36
	minLaunchLeft  = 50
	minLaunchRight = 36
	// keysAt is where the key hints start on the sparkline's row.
	keysAt = 64
	// recentArrow is the room left at the end of a recent row for where it
	// came from or went (`← codex-work`).
	recentArrow  = 14
	glanceFolder = 14
	// minRecentTitle is the narrowest a recent title gets before the folder
	// column is dropped to make room for it.
	minRecentTitle = 28
	sheetFolder    = 16
	// The sheet's three columns: launch, integrations, in the profile.
	sheetIntegrations = 36
	sheetProfile      = 42
	sheetLaunchLabel  = 9
	sheetFieldLabel   = 12
	// narrowLaunch is the narrowest the sheet's launch column gets before the
	// three columns stack.
	narrowLaunch = 40
)

var (
	accentTextStyle = lipgloss.NewStyle().Foreground(colorAccent)
	infoTextStyle   = lipgloss.NewStyle().Foreground(colorInfo)
	warnTextStyle   = lipgloss.NewStyle().Foreground(colorWarn)
	dangerTextLight = lipgloss.NewStyle().Foreground(colorDanger)
)

// cellAt pads a styled fragment to exactly width cells, cutting what does not
// fit, so the next column starts where the design put it.
func cellAt(fragment string, width int) string {
	if width <= 0 {
		return ""
	}
	return padLine(fragment, width)
}

// expansion is everything about the account under the cursor at the level
// asked for, every line tinted as part of the selection.
func (m tuiModel) expansion(profile Profile, c boardColumns, width int, level expansionLevel) []string {
	inner := max(width-boardIndent, 8)
	var lines []string
	switch level {
	case expandSheet:
		lines = m.sheetLines(profile, c, inner)
	case expandGlance:
		lines = m.glanceLines(profile, c, inner)
	default:
		lines = []string{modelStyle.Render(truncate(m.launchSummary(profile), inner))}
	}
	indent := strings.Repeat(" ", boardIndent)
	for index, line := range lines {
		if !strings.HasPrefix(line, resetLineMark) {
			line = indent + line
		} else {
			line = strings.TrimPrefix(line, resetLineMark)
		}
		lines[index] = tintLine(line, width)
	}
	return lines
}

// resetLineMark flags the reset line, which is laid out against the board's
// own columns rather than the indent, so it lines up under the gauges.
const resetLineMark = "\x00reset"

// resetLine spells out when each window rolls over, under the gauge it belongs
// to. It is left out for an account with no quota to report.
func (m tuiModel) resetLine(profile Profile, c boardColumns, width int) (string, bool) {
	usage, known := m.usage[profile.Name]
	if !reportsQuota(profile.Provider) || !known {
		return "", false
	}
	first := 2 + c.name + c.provider
	second := first + c.cell() + 2
	// A narrow gauge column leaves no room for the wait; the time is the half
	// worth keeping.
	half := func(window usageWindow, room int) string {
		when, in := resetPhrase(m.clock(), window.Resets)
		if when == "" {
			return ""
		}
		text := sectionLabelStyle.Render("resets ") + fieldValueStyle.Render(when)
		if full := text + modelStyle.Render("  "+in); lipgloss.Width(full) <= room {
			return full
		}
		return text
	}
	five, week := half(usage.FiveHour, second-first-2), half(usage.Weekly, width-second)
	if five == "" && week == "" {
		return "", false
	}
	line := strings.Repeat(" ", first) + cellAt(five, second-first) + week
	return resetLineMark + truncateStyled(line, width), true
}

// glanceLines is the glance: reset times, how it launches in three labelled
// lines, what it is running beside what it was last on, and how busy the day
// has been.
func (m tuiModel) glanceLines(profile Profile, c boardColumns, inner int) []string {
	var lines []string
	if reset, ok := m.resetLine(profile, c, inner+boardIndent); ok {
		lines = append(lines, reset)
	}
	lines = append(lines, m.glanceLaunch(profile, inner)...)
	lines = append(lines, "")
	lines = append(lines, m.panes(profile, inner, expandGlance)...)
	lines = append(lines, "")
	lines = append(lines, m.activityRow(profile, inner, expandGlance)...)
	return append(lines, "")
}

// glanceLaunch is NOTE / RUNS / WITH on the left and MODEL·CLI / ENV / HAS on
// the right. Too narrow for the two side by side, each pair goes on its own
// lines, left first.
func (m tuiModel) glanceLaunch(profile Profile, inner int) []string {
	label := func(text string) string { return sectionLabelStyle.Render(pad(text, glanceLabel)) }
	model, _ := launchModel(profile)
	modelCell := dimStyle.Render("default")
	if model != "" {
		modelCell = fieldValueStyle.Render(model)
	}
	modelCell = pad(modelCell, max(glanceModel, lipgloss.Width(model)+2))
	pairs := [][2]string{
		{label("NOTE") + m.noteCell(profile), label("MODEL") + modelCell + sectionLabelStyle.Render("CLI ") + modelStyle.Render(cliField(profile))},
		{label("RUNS") + fieldValueStyle.Render(runsLine(profile)), label("ENV") + envCell(profile)},
		{label("WITH") + m.integrationsGlance(profile), label("HAS") + m.hasCell(profile)},
	}
	rightWidth := glanceRightWidth
	if inner-rightWidth < minLaunchLeft {
		rightWidth = inner - minLaunchLeft
	}
	leftWidth := inner - rightWidth
	var lines []string
	if rightWidth < minLaunchRight {
		for _, pair := range pairs {
			lines = append(lines, truncateStyled(pair[0], inner))
		}
		for _, pair := range pairs {
			lines = append(lines, truncateStyled(pair[1], inner))
		}
		return lines
	}
	for _, pair := range pairs {
		// The left value gives way two columns short of the right one, so a
		// long command never runs into the label beside it.
		lines = append(lines, cellAt(truncateStyled(pair[0], leftWidth-2), leftWidth)+truncateStyled(pair[1], rightWidth))
	}
	return lines
}

func (m tuiModel) noteCell(profile Profile) string {
	if profile.Notes == "" {
		return dimStyle.Render("none")
	}
	return fieldValueStyle.Render(profile.Notes)
}

// runsLine is the command and its default arguments, without the environment,
// which has a field of its own: it is usually the longest part and the one
// that changes least.
func runsLine(profile Profile) string {
	return formatArguments(append([]string{profile.Command}, profile.DefaultArgs...))
}

func envCell(profile Profile) string {
	if len(profile.DefaultEnv) == 0 {
		return dimStyle.Render("none")
	}
	return modelStyle.Render(formatLaunchLine(profile.DefaultEnv, nil))
}

// facts is what has been read about the profile, or false while it has not
// been — including when the facts on hand are about the account the cursor
// just left.
func (m tuiModel) factsFor(profile Profile) (profileFacts, bool) {
	if !m.facts.loaded || m.facts.profile != profile.Name {
		return profileFacts{}, false
	}
	return m.facts, true
}

// integrationName is an integration as the expansion labels it. The shim is
// "tmux shim" here: the row already sits under an account launched from
// ranma, and the box spells the whole name out.
func integrationName(kind integrationKind) string {
	if kind == integrationRanma {
		return "tmux shim"
	}
	return kind.name()
}

// onExpansion is whether the board's expansion lists an integration. The
// expansion handoff drew three, and its sheet has no spare row for a fourth
// without pushing the frame past its height, so messaging is shown by the
// integrations box (I) and `ai peers` until the expansion is redrawn for it.
func onExpansion(kind integrationKind) bool {
	return kind != integrationMessaging
}

// integrationsGlance is each integration that applies, with its state in a
// word, the way the box reads it.
func (m tuiModel) integrationsGlance(profile Profile) string {
	facts, ok := m.factsFor(profile)
	if !ok {
		return unknownStyle.Render("…")
	}
	var parts []string
	for _, status := range facts.integrations {
		if status.state == stateNA || !onExpansion(status.kind) {
			continue
		}
		parts = append(parts, modelStyle.Render(integrationName(status.kind))+" "+glanceState(status))
	}
	if len(parts) == 0 {
		return dimStyle.Render("none apply")
	}
	return strings.Join(parts, "   ")
}

func glanceState(status integrationStatus) string {
	switch status.state {
	case stateOn:
		switch {
		case status.kind == integrationRanma && status.inert:
			return warnTextStyle.Render("● inert here")
		case status.kind == integrationStatusLine && status.note == "own line":
			return liveStyle.Render("●") + fieldValueStyle.Render(" ai's line")
		case status.kind == integrationStatusLine:
			return liveStyle.Render("●") + fieldValueStyle.Render(" tmux bar")
		}
		return liveStyle.Render("●") + fieldValueStyle.Render(" on")
	case stateAnother:
		return warnTextStyle.Render("●") + fieldValueStyle.Render(" another")
	case stateNotRead, stateLocked:
		// OpenUsage is never read back, running or not; the lock is about
		// installing, which the sheet and the box say.
		return sectionLabelStyle.Render("· not read")
	case stateRefused:
		return dangerTextLight.Render("✗ refused")
	case stateNeeds:
		// OpenUsage needing openusage is OpenUsage not being installed.
		if status.needs == status.kind.word() {
			return sectionLabelStyle.Render("· not installed")
		}
		return sectionLabelStyle.Render("needs " + status.needs)
	}
	return sectionLabelStyle.Render("○ off")
}

// sheetState is the state with its qualifier, for the sheet's wider column.
func sheetState(status integrationStatus) string {
	switch status.state {
	case stateOn:
		switch {
		case status.kind == integrationRanma && status.inert:
			return warnTextStyle.Render("● ") + fieldValueStyle.Render("on") + dimStyle.Render(" · ") + warnTextStyle.Render("inert here")
		case status.kind == integrationStatusLine && status.note == "own line":
			return liveStyle.Render("● ") + fieldValueStyle.Render("ai's line")
		case status.kind == integrationStatusLine:
			return liveStyle.Render("● ") + fieldValueStyle.Render("tmux bar")
		}
		return liveStyle.Render("● ") + fieldValueStyle.Render("on")
	case stateAnother:
		return warnTextStyle.Render("● ") + fieldValueStyle.Render("another") + dimStyle.Render(" · not ai's")
	case stateRefused:
		text := dangerTextLight.Render("✗ refused")
		if status.note != "" {
			text += dimStyle.Render(" · " + status.note)
		}
		return text
	}
	return glanceState(status)
}

// hasCell is what is installed in the profile and the apps it belongs to.
func (m tuiModel) hasCell(profile Profile) string {
	facts, ok := m.factsFor(profile)
	if !ok {
		return unknownStyle.Render("…")
	}
	var parts []string
	if facts.mcpKnown {
		parts = append(parts, fieldValueStyle.Render(fmt.Sprint(len(facts.mcp)))+modelStyle.Render(" mcp"))
	}
	if facts.skillsKnown {
		parts = append(parts, fieldValueStyle.Render(fmt.Sprint(facts.skills))+modelStyle.Render(" skills"))
	}
	for _, app := range facts.apps {
		role := " (member)"
		if app.active {
			role = " (active)"
		}
		parts = append(parts, modelStyle.Render("app ")+accentTextStyle.Render(app.name)+modelStyle.Render(role))
	}
	if len(parts) == 0 {
		return dimStyle.Render("nothing installed")
	}
	return strings.Join(parts, dimStyle.Render("  ·  "))
}

// panes is RUNNING HERE beside RECENT. Too narrow for the two side by side,
// the recent list goes under what is running rather than being squeezed.
func (m tuiModel) panes(profile Profile, inner int, level expansionLevel) []string {
	leftWidth := min(paneLeft, max(inner-minRecentPane, minRunningPane))
	if inner-leftWidth < minRecentPane {
		var lines []string
		left, right := m.runningPane(profile, inner, level), m.recentPane(profile, inner, level)
		for _, line := range append(append(left, ""), right...) {
			lines = append(lines, truncateStyled(line, inner))
		}
		return lines
	}
	rightWidth := inner - leftWidth
	left, right := m.runningPane(profile, leftWidth-1, level), m.recentPane(profile, rightWidth, level)
	lines := make([]string, 0, max(len(left), len(right)))
	for row := range max(len(left), len(right)) {
		first, second := "", ""
		if row < len(left) {
			first = left[row]
		}
		if row < len(right) {
			second = right[row]
		}
		lines = append(lines, cellAt(first, leftWidth)+truncateStyled(second, rightWidth))
	}
	return lines
}

// runningPane is each live instance: what it is on and for how long, then the
// handle Claude knows it by, its PID and folder. The sheet gives the folder a
// line of its own, with the conversation's id beside it.
func (m tuiModel) runningPane(profile Profile, width int, level expansionLevel) []string {
	header := sectionLabelStyle.Render("RUNNING HERE")
	count := m.runningCount(profile.Name)
	if count > 0 {
		header += modelStyle.Render(fmt.Sprintf("  %d", count))
	}
	lines := []string{header}
	for _, instance := range m.live {
		if instance.profile != profile.Name {
			continue
		}
		marker := liveStyle.Render("▶ ")
		if instance.headless {
			marker = infoTextStyle.Render("◇ ")
		}
		uptime := formatUptime(instance.uptime(m.clock()))
		title := truncate(m.instanceTitle(instance), max(width-4-len([]rune(uptime)), 8))
		lines = append(lines, marker+fieldValueStyle.Render(title)+modelStyle.Render("  "+uptime))

		var detail []string
		if instance.headless {
			detail = append(detail, infoTextStyle.Render("headless"))
		}
		if name := m.instanceName(instance); name != "" {
			detail = append(detail, modelStyle.Render(name))
		}
		detail = append(detail, dimStyle.Render(fmt.Sprintf("PID %d", instance.pid)))
		folder := shortenHome(instance.folder)
		if level == expandGlance && !instance.headless && folder != "" {
			detail = append(detail, dimStyle.Render(folder))
		}
		lines = append(lines, "  "+truncateStyled(strings.Join(detail, dimStyle.Render(" · ")), width-2))
		if level == expandSheet {
			where := folder
			if id := shortID(instance.session.id); id != "" {
				where = strings.TrimPrefix(where+" · "+id, " · ")
			}
			if where != "" {
				lines = append(lines, "  "+dimStyle.Render(truncate(where, width-2)))
			}
		}
	}
	if count == 0 {
		lines = append(lines, unknownStyle.Render(m.pending("nothing running")))
	}
	return lines
}

// instanceName is the handle Claude runs the instance under. Only `claude
// agents` knows it, and the board does not start a CLI on every refresh, so it
// is the one the h and k pickers asked for when they last opened, matched by
// PID; until then the row goes without it.
func (m tuiModel) instanceName(instance profileInstance) string {
	if instance.session.name != "" {
		return instance.session.name
	}
	return m.slugs[instance.pid]
}

// shortID is the first eight characters of a conversation id: enough to tell
// two apart and to find one with a search, short enough to sit beside a path.
func shortID(id string) string {
	id = strings.TrimSpace(id)
	if runes := []rune(id); len(runes) > 8 {
		return string(runes[:8])
	}
	return id
}

// recentPane is the account's last conversations, by last message. A title
// shared by another row carries what was asked next, and a conversation that
// moved by handoff says where it came from or went.
func (m tuiModel) recentPane(profile Profile, width int, level expansionLevel) []string {
	rows, folderWidth := glanceRecentRows, glanceFolder
	if level == expandSheet {
		rows, folderWidth = sheetRecentRows, sheetFolder
	}
	recent := m.withoutHeadless(m.recent)
	shown := recent[:min(rows, len(recent))]
	lines := []string{m.recentHeader(profile, len(shown), level)}
	if len(shown) == 0 {
		return append(lines, unknownStyle.Render(m.pending("no recorded sessions")))
	}
	facts, _ := m.factsFor(profile)
	whenWidth := 0
	for _, record := range shown {
		whenWidth = max(whenWidth, len([]rune(formatWhen(m.clock(), record.activity()))))
	}
	whenWidth += 2
	// The arrow's room is kept only when a row has one, and in a narrow pane
	// the folder gives way before the title does: the title says which
	// conversation, the folder only where.
	arrowRoom := 0
	for _, record := range shown {
		_, in := facts.incoming[record.session.id]
		_, out := m.lineage[record.session.id]
		if record.session.id != "" && (in || out) {
			arrowRoom = recentArrow
		}
	}
	available := width - whenWidth - arrowRoom
	folderWidth = min(folderWidth, max(available-minRecentTitle, 0))
	if folderWidth < 6 {
		folderWidth = 0
	}
	titleWidth := available - folderWidth
	arrowAt := whenWidth + titleWidth + folderWidth
	for _, record := range shown {
		line := modelStyle.Render(pad(formatWhen(m.clock(), record.activity()), whenWidth)) +
			cellAt(m.recentTitle(record, facts, titleWidth-1), titleWidth)

		arrow := ""
		if source, ok := facts.incoming[record.session.id]; ok && record.session.id != "" {
			arrow = infoTextStyle.Render("← ") + modelStyle.Render(source)
		}
		if link, passed := m.lineage[record.session.id]; passed && record.session.id != "" {
			if arrow != "" {
				arrow += "  "
			}
			arrow += liveStyle.Render("→ ") + modelStyle.Render(link.TargetProfile)
		}
		if folderWidth > 0 {
			folder := folderLeaf(record.folder)
			room := folderWidth
			if arrow != "" {
				room -= 2
			}
			if level == expandSheet {
				folder = folderShort(record.folder, room)
			}
			line += cellAt(modelStyle.Render(truncate(folder, room)), folderWidth)
		}
		if arrow != "" {
			line += truncateStyled(arrow, max(width-arrowAt, 1))
		}
		lines = append(lines, truncateStyled(line, width))
	}
	return lines
}

// recentHeader says what the list is ordered by and what it leaves out.
func (m tuiModel) recentHeader(profile Profile, shown int, level expansionLevel) string {
	notes := []string{"by last message"}
	facts, ok := m.factsFor(profile)
	if level == expandSheet && ok && facts.counts.known {
		total := max(facts.counts.interactive, shown)
		if m.showHeadless {
			total = max(facts.counts.interactive+facts.counts.headless, shown)
		}
		notes = append(notes, fmt.Sprintf("showing %d of %d", shown, total))
	}
	if !m.showHeadless {
		hidden := 0
		if ok && facts.counts.known {
			hidden = facts.counts.headless
		} else {
			for _, record := range m.recent {
				if record.headless {
					hidden++
				}
			}
		}
		if hidden > 0 {
			notes = append(notes, fmt.Sprintf("%d headless hidden", hidden))
		}
	}
	return sectionLabelStyle.Render("RECENT") + dimStyle.Render("   "+strings.Join(notes, "  ·  "))
}

// recentTitle is the conversation's title, and for a title another listed
// row shares, the second thing that was asked in it.
func (m tuiModel) recentTitle(record recordedSession, facts profileFacts, width int) string {
	title, style := record.session.title, fieldValueStyle
	if title == "" {
		return unknownStyle.Render(truncate("untitled session", width))
	}
	second := facts.seconds[secondPromptKey(record)]
	if second == "" {
		return style.Render(truncate(title, width))
	}
	// The title is what says which conversation this is; the second prompt
	// only separates it from its namesake, so it gives way first.
	title = truncate(title, max(width-8, 8))
	room := width - lipgloss.Width(title) - 4
	if room < 4 {
		return style.Render(title)
	}
	return style.Render(title) + dimStyle.Render("  · ") + modelStyle.Render(truncate(second, room))
}

func sessionsWord(count int) string {
	if count == 1 {
		return " session"
	}
	return " sessions"
}

// activityRow is the sparkline with the day's total and its peak, and the
// expansion's keys on the same row. The sheet adds an hour axis under it.
func (m tuiModel) activityRow(profile Profile, inner int, level expansionLevel) []string {
	left := ""
	if m.activity.known() {
		left = sectionLabelStyle.Render("24h ") + providerStyle(profile.Provider).Render(sparkline(m.activity.counts[:])) +
			fieldValueStyle.Render(fmt.Sprintf("  %d", m.activity.total)) + modelStyle.Render(sessionsWord(m.activity.total))
		if !m.activity.peak.IsZero() {
			left += dimStyle.Render("  ·  ") + modelStyle.Render("peak "+m.activity.peak.Local().Format("15:04"))
		}
	}
	// With the sheet asked for but no room for it, the glance still says tab
	// folds it, so the keypress is seen to have landed.
	toggle := helpEntry{"tab", "sheet"}
	if m.sheet {
		toggle.desc = "less"
	}
	keys := []helpEntry{{"R", "resume"}, {"H", "hand off"}, {"h", "open live"}, toggle}
	if level == expandSheet {
		keys = []helpEntry{{"R", "resume"}, {"H", "hand off"}, {"h", "open live"}, {"I", "integrations"}, {"tab", "less"}}
	}
	rendered := renderKeys(keys...)
	var lines []string
	if left == "" {
		lines = []string{strings.Repeat(" ", min(keysAt, max(inner-lipgloss.Width(rendered), 0))) + renderKeysFit(keys, inner)}
	} else if lipgloss.Width(left)+2 <= keysAt && keysAt+lipgloss.Width(rendered) <= inner {
		lines = []string{cellAt(left, keysAt) + rendered}
	} else {
		lines = []string{truncateStyled(left, inner), renderKeysFit(keys, inner)}
	}
	if level == expandSheet && m.activity.known() {
		lines = append(lines, "    "+dimStyle.Render(activityAxis(m.clock())))
	}
	return lines
}

// sheetLines is the sheet: the launch in full, the integrations as the box
// checks them, what is installed, and longer running and recent lists.
func (m tuiModel) sheetLines(profile Profile, c boardColumns, inner int) []string {
	var lines []string
	if reset, ok := m.resetLine(profile, c, inner+boardIndent); ok {
		lines = append(lines, reset)
	}
	lines = append(lines, "")
	lines = append(lines, m.sheetColumns(profile, inner)...)
	lines = append(lines, "")
	lines = append(lines, m.panes(profile, inner, expandSheet)...)
	lines = append(lines, m.activityRow(profile, inner, expandSheet)...)
	return append(lines, "")
}

// sheetColumns lays LAUNCH, INTEGRATIONS and IN THE PROFILE side by side, the
// launch column giving way first; past narrowLaunch they stack.
func (m tuiModel) sheetColumns(profile Profile, inner int) []string {
	launch := m.sheetLaunch(profile)
	integrations := m.sheetIntegrations(profile)
	inProfile := m.sheetProfile(profile)
	integrationsWidth, profileWidth := sheetIntegrations, sheetProfile
	if inner-integrationsWidth-profileWidth < narrowLaunch+10 {
		integrationsWidth, profileWidth = 32, 36
	}
	launchWidth := inner - integrationsWidth - profileWidth
	if launchWidth < narrowLaunch {
		var lines []string
		for index, column := range [][]string{launch, integrations, inProfile} {
			if index > 0 {
				lines = append(lines, "")
			}
			for _, line := range column {
				lines = append(lines, truncateStyled(line, inner))
			}
		}
		return lines
	}
	rows := max(len(launch), len(integrations), len(inProfile))
	lines := make([]string, 0, rows)
	for row := range rows {
		at := func(column []string) string {
			if row < len(column) {
				return column[row]
			}
			return ""
		}
		lines = append(lines, cellAt(truncateStyled(at(launch), launchWidth-2), launchWidth)+
			cellAt(truncateStyled(at(integrations), integrationsWidth-1), integrationsWidth)+truncateStyled(at(inProfile), profileWidth))
	}
	return lines
}

func (m tuiModel) sheetLaunch(profile Profile) []string {
	label := func(text string) string { return sectionLabelStyle.Render(pad(text, sheetLaunchLabel)) }
	model, source := launchModel(profile)
	modelCell := dimStyle.Render("the CLI's default")
	if model != "" {
		modelCell = fieldValueStyle.Render(model) + dimStyle.Render("   "+source)
	}
	state := "unknown"
	if root, err := profileRoot(); err == nil {
		state = shortenHome(filepath.Join(root, profile.Name))
	}
	return []string{
		sectionLabelStyle.Render("LAUNCH") + dimStyle.Render("   in the order a launch uses it"),
		label("NOTE") + m.noteCell(profile),
		label("ENV") + envCell(profile),
		label("COMMAND") + fieldValueStyle.Render(runsLine(profile)),
		label("CLI") + modelStyle.Render(cliField(profile)),
		label("MODEL") + modelCell,
		label("STATE") + modelStyle.Render(state),
	}
}

// sheetIntegrations lists each integration that applies, then what qualifies
// them: where the shim takes effect, and that OpenUsage cannot be installed
// while the profile runs. The last note sits on the launch column's last row,
// where the handoff drew it.
func (m tuiModel) sheetIntegrations(profile Profile) []string {
	lines := []string{sectionLabelStyle.Render("INTEGRATIONS") + dimStyle.Render("   as I checks them")}
	facts, ok := m.factsFor(profile)
	if !ok {
		return append(lines, unknownStyle.Render("…"))
	}
	label := func(text string) string { return sectionLabelStyle.Render(pad(text, sheetFieldLabel)) }
	locked := false
	var notes []string
	for _, status := range facts.integrations {
		if status.state == stateNA || !onExpansion(status.kind) {
			continue
		}
		lines = append(lines, label(integrationName(status.kind))+sheetState(status))
		if status.kind == integrationRanma && status.state == stateOn {
			note := "applies here"
			if status.inert {
				note = "only from a ranma pane"
			}
			notes = append(notes, strings.Repeat(" ", sheetFieldLabel)+dimStyle.Render(note))
		}
		locked = locked || status.state == stateLocked
	}
	lines = append(lines, notes...)
	if locked {
		for len(lines) < 6 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.Repeat(" ", sheetFieldLabel)+liveStyle.Render("▶ ")+modelStyle.Render("locked while running"))
	}
	return lines
}

func (m tuiModel) sheetProfile(profile Profile) []string {
	lines := []string{sectionLabelStyle.Render("IN THE PROFILE")}
	label := func(text string) string { return sectionLabelStyle.Render(pad(text, sheetFieldLabel)) }
	facts, ok := m.factsFor(profile)
	if !ok {
		lines = append(lines, label("mcp servers")+unknownStyle.Render("…"), label("skills")+unknownStyle.Render("…"), label("app")+unknownStyle.Render("…"))
	} else {
		mcp := dimStyle.Render("not for " + profile.Provider)
		if facts.mcpKnown {
			mcp = fieldValueStyle.Render(fmt.Sprint(len(facts.mcp)))
			if len(facts.mcp) > 0 {
				mcp += dimStyle.Render("  " + strings.Join(facts.mcp, " · "))
			}
		}
		skills := dimStyle.Render("not for " + profile.Provider)
		if facts.skillsKnown {
			skills = fieldValueStyle.Render(fmt.Sprint(facts.skills))
		}
		app := dimStyle.Render("none")
		if len(facts.apps) > 0 {
			first := facts.apps[0]
			role := " · member"
			if first.active {
				role = " · active member"
			}
			app = accentTextStyle.Render(first.name) + modelStyle.Render(role)
			if more := len(facts.apps) - 1; more > 0 {
				app += dimStyle.Render(fmt.Sprintf("  +%d", more))
			}
		}
		lines = append(lines, label("mcp servers")+mcp, label("skills")+skills, label("app")+app)
	}
	return append(lines, label("auth")+authSheetCell(profile))
}

// authSheetCell is the auth state and what it was read from, which is all ai
// ever learns about a credential: whether a file is there, or a variable set.
func authSheetCell(profile Profile) string {
	switch profileAuthState(profile) {
	case authPresent:
		return authPresentStyle.Render("● ok") + dimStyle.Render("   file present")
	case authAPIKey:
		return authKeyStyle.Render("● key") + dimStyle.Render("   from the environment")
	case authMissing:
		return authMissingStyle.Render("○ login") + dimStyle.Render("   press l")
	default:
		return authUnknownStyle.Render("· ?") + dimStyle.Render("   location unknown")
	}
}
