package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// A picker splits into a list and a preview when the box is wide enough for
// both halves to be read, and stays a plain list when it is not. The minimums
// are what each half needs to say anything: a session row is a time, a title and
// a folder side by side, and a preview much narrower than this wraps every
// sentence into a column of single words.
const (
	pickerModalWidth = 140
	pickerListMin    = 46
	previewPaneMin   = 34
	// pickerListShare is how much of a split picker the list takes. The list
	// carries three fixed columns and the preview only wraps, so the half that
	// cannot fold gets the larger share of a narrow box.
	pickerListShare = 48
	// pickerListMax is where the list stops taking that share: past it a row's
	// folder has all the room it is given, and every column a wider terminal
	// offers goes to the preview, which turns width into sentences that fit on
	// one line.
	pickerListMax = 62
	// pickerFolderWidth and pickerWhenWidth are a row's fixed columns.
	pickerFolderWidth = 18
	pickerWhenWidth   = 7
	// pickerAccountWidth is the account column a row gains when the picker
	// lists every profile: wide enough to tell a name apart by its head.
	pickerAccountWidth = 14
)

// ---- resume picker --------------------------------------------------------

// recentPicker is two panes: conversations on the left, the one under the
// cursor read out on the right. The heading carries the scope — this account
// or all of them — and the search, and the footer says exactly where Enter
// will reopen the row.
func (m tuiModel) recentPicker(width, rows int) []string {
	heading := spread(boxTitle("resume")+"   "+m.scopeToggle(), m.searchCorner(), width)
	lines := append([]string{heading, ""}, m.pickerBody(width, rows)...)
	where := "resume it"
	if record, ok := m.pickedRecord(); ok {
		where = "resume in " + shortenHome(record.folder)
	}
	return append(lines, "", boxFooter(width, helpEntry{"esc", "close"},
		helpEntry{"↑↓", "choose"}, helpEntry{"↵", where}, helpEntry{"H", "hand this off instead"}))
}

func (m tuiModel) pickedRecord() (recordedSession, bool) {
	visible := m.visibleRecent()
	if m.record < 0 || m.record >= len(visible) {
		return recordedSession{}, false
	}
	return visible[m.record], true
}

// scopeToggle is the this-account / all-accounts switch, the current one
// drawn as a chip, and the key that flips it.
func (m tuiModel) scopeToggle() string {
	on := selectedPen(true)
	this, all := on.render(boxTitleStyle, " this account "), sectionLabelStyle.Render("  all accounts")
	if m.pickerAll {
		this, all = sectionLabelStyle.Render("this account  "), on.render(boxTitleStyle, " all accounts ")
	}
	return this + all + helpKeyStyle.Render("  a")
}

// searchCorner is the search as the heading's right-hand side: the key that
// opens it, the query while it is typed, or the filter it left behind.
func (m tuiModel) searchCorner() string {
	switch {
	case m.recentSearching:
		return helpKeyStyle.Render("/") + caretView(pen{}, fieldValueStyle, m.recentFilter, m.recentTail, len([]rune(m.recentFilter))+1)
	case m.recentFilter != "":
		return helpKeyStyle.Render("/ ") + fieldValueStyle.Render(m.recentFilter) + dimStyle.Render("  esc / clears")
	}
	return renderKeys(helpEntry{"/", "search title, folder or id"})
}

// pickerBody is the list of sessions, with the chosen conversation read out
// beside it when there is room for both. Below that width the preview folds away
// rather than being squeezed: the list is what the keys act on, and it keeps
// the space.
func (m tuiModel) pickerBody(width, rows int) []string {
	if width < pickerListMin+dividerWidth+previewPaneMin {
		return padToRows(m.pickerList(width, rows), rows, -1)
	}
	columns := width - dividerWidth
	list := min(max(columns*pickerListShare/100, pickerListMin), columns-previewPaneMin, pickerListMax)
	preview := columns - list
	// Both halves are drawn at the height the frame allows, whatever is under the
	// cursor. Settling on the content instead gives the box a different size for
	// every row: the arrow key that moves the cursor also moves the rows around
	// it, and stepping from a long conversation to a two-message one collapses
	// the list beside it as well.
	return joinPanes(m.pickerList(list, rows), m.previewPane(preview, rows), list, preview, rows)
}

// pickerList is the column heading, the rows windowed around the cursor, and
// a count under them.
func (m tuiModel) pickerList(width, rows int) []string {
	visible := m.visibleRecent()
	header := "  " + pad("WHEN", pickerWhenWidth)
	if m.pickerAll {
		header += pad("ACCOUNT", pickerAccountWidth)
	}
	header += pad("CONVERSATION", max(width-lipgloss.Width(header)-pickerFolderWidth, 8)) + "FOLDER"
	lines := []string{sectionLabelStyle.Render(truncate(header, width)), ""}
	footer := fmt.Sprintf("  %s · newest activity first", plural(len(visible), "conversation"))
	if len(visible) == 0 && m.recentFilter != "" {
		lines = append(lines, emptyStateStyle.Render("Nothing matches "+m.recentFilter))
		return lines
	}
	lines = append(lines, windowRows(m.recentRows(width), m.record, max(rows-4, 1))...)
	return append(lines, "", dimStyle.Render(truncate(footer, width)))
}

// recentRows is one line per conversation: when, whose (when every account is
// listed), what, and where. A row carries where it was handed on to, when it
// was, because that is the only direction a pass has.
func (m tuiModel) recentRows(width int) []string {
	visible := m.visibleRecent()
	rows := make([]string, 0, len(visible))
	for index, record := range visible {
		selected := index == m.record
		ink := selectedPen(selected)
		bar, style := ink.render(lipgloss.NewStyle(), "  "), fieldValueStyle
		if selected {
			bar, style = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
		}
		line := bar + ink.render(dimStyle, pad(formatWhen(m.clock(), record.activity()), pickerWhenWidth))
		if m.pickerAll {
			name := record.profile
			if name == "" {
				if profile, ok := m.profileForRecord(record); ok {
					name = profile.Name
				}
			}
			line += ink.render(providerStyle(m.providerOf(name)), pad(truncate(name, pickerAccountWidth-1), pickerAccountWidth))
		}
		title := record.session.title
		if title == "" {
			title, style = "untitled session", unknownStyle
		}
		marker := ""
		if link, passed := m.lineage[record.session.id]; passed && record.session.id != "" {
			marker = " → " + link.TargetProfile
		}
		cell := max(width-lipgloss.Width(line)-pickerFolderWidth, 8)
		shown := truncate(title, max(cell-lipgloss.Width(marker)-2, 6))
		line += ink.render(style, shown) + ink.render(liveStyle, marker) +
			ink.render(lipgloss.NewStyle(), strings.Repeat(" ", max(cell-lipgloss.Width(shown)-lipgloss.Width(marker), 0))) +
			ink.render(dimStyle, truncate(shortenHome(record.folder), pickerFolderWidth))
		rows = append(rows, padStyled(ink, line, width))
	}
	return rows
}

// previewPane is the conversation under the cursor, read under the account that
// recorded it, or nothing at all when the cursor is not on one.
func (m tuiModel) previewPane(width, rows int) []string {
	record, ok := m.pickedRecord()
	if !ok {
		return nil
	}
	profile, ok := m.profileForRecord(record)
	if !ok {
		return nil
	}
	return m.previewLines(profile, record, width, rows)
}

// ---- handoff: the stepper --------------------------------------------------

// handoffSteps are the four steps a pass goes through. The last is the launch
// itself, which is why no box ever shows it current.
var handoffSteps = []string{"leaving", "going to", "brief", "open"}

// stepper draws where a handoff is: done steps ticked, the current one filled,
// the rest open.
func stepper(current int) string {
	line := boxTitle("hand off") + "      "
	for index, step := range handoffSteps {
		if index > 0 {
			line += dimStyle.Render(" ────── ")
		}
		switch {
		case index < current:
			line += liveStyle.Render("✓ " + step)
		case index == current:
			line += boxTitleStyle.Render("● " + step)
		default:
			line += sectionLabelStyle.Render("○ " + step)
		}
	}
	return line
}

// handoffPicker is the first step: which conversation is leaving. It is the
// resume picker with a different verb on it, deliberately — the row you would
// have resumed is the row you are handing over.
func (m tuiModel) handoffPicker(width, rows int) []string {
	heading := stepper(0)
	corner := m.scopeToggle()
	if fitsBeside(heading, corner, width) {
		heading = spread(heading, corner, width)
	}
	lines := []string{heading, "", truncateStyled(m.searchCorner(), width)}
	lines = append(lines, m.pickerBody(width, max(rows-1, minBlockRows))...)
	next := "choose this one"
	if m.autoSwap {
		next = "write the brief for the roomiest account"
	}
	return append(lines, "", spread(
		renderKeysFit([]helpEntry{{"↑↓", "choose"}, {"↵", next}, {"esc", "cancel"}}, width-20),
		m.autoSwapCorner(), width))
}

// autoSwapCorner is the auto-swap setting where a handoff can change it.
func (m tuiModel) autoSwapCorner() string {
	state := sectionLabelStyle.Render("off")
	if m.autoSwap {
		state = liveStyle.Render("on")
	}
	return dimStyle.Render("auto-swap ") + state + helpKeyStyle.Render("  A")
}

// ---- handoff: going to -----------------------------------------------------

// handoffLeftWidth is the leaving pane. It carries one account's figures and
// how its conversation ended; the destinations beside it take the rest.
const handoffLeftWidth = 46

// handoffToContent is the second step: where the work goes. The conversation
// that is leaving stays in view on the left, with how it ended, so the choice
// on the right is made against the work rather than from memory.
func (m tuiModel) handoffToContent(width, rows int) []string {
	lines := []string{stepper(1), ""}
	body := max(rows-6, 6)
	if width < handoffLeftWidth+dividerWidth+40 {
		lines = append(lines, padToRows(m.destinationLines(width, body), body, -1)...)
	} else {
		right := width - handoffLeftWidth - dividerWidth
		lines = append(lines, joinPanes(m.leavingLines(handoffLeftWidth, body), m.destinationLines(right, body), handoffLeftWidth, right, body)...)
	}
	source, _ := m.profileForRecord(m.handoff.source)
	moves := strings.Join([]string{
		fieldValueStyle.Render("a brief"),
		fieldValueStyle.Render("same folder"),
		fieldValueStyle.Render("git state read fresh"),
		fieldValueStyle.Render("the transcript stays with " + source.Name),
	}, dimStyle.Render("  ·  "))
	return append(lines, "", sectionLabelStyle.Render("WHAT MOVES"), truncateStyled(moves, width), "",
		spread(renderKeysFit([]helpEntry{{"↑↓", "choose"}, {"↵", "write the brief"}, {"←", "back"}, {"esc", "cancel"}}, width-24),
			m.autoSwapCorner(), width))
}

func (m tuiModel) leavingLines(width, rows int) []string {
	record := m.handoff.source
	source, _ := m.profileForRecord(record)
	title, style := record.session.title, fieldValueStyle.Bold(true)
	if title == "" {
		title, style = "untitled session", unknownStyle
	}
	lines := []string{
		sectionLabelStyle.Render("LEAVING"),
		providerStyle(source.Provider).Bold(true).Render(source.Name) + m.windowFigures(source),
		"",
		style.Render(truncate(title, width)),
		dimStyle.Render(truncate(previewWhere(m.clock(), record), width)),
		dimStyle.Render(truncate("id "+record.session.id, width)),
		"",
		sectionLabelStyle.Render("HOW IT ENDED"),
	}
	preview, read := m.previews[record.session.id]
	if !read && m.preview.session == record.session.id {
		preview, read = m.preview, true
	}
	switch {
	case !read || preview.pending():
		return append(lines, unknownStyle.Render("reading the transcript…"))
	case preview.problem != "":
		return append(lines, unknownStyle.Render(truncate(preview.problem, width)))
	}
	body := conversationLines(source.Provider, preview.messages, width, previewTurnRows)
	return append(lines, fitTail(body, max(rows-len(lines), minBlockRows), preview.earlier)...)
}

// windowFigures is an account's two windows as short figures.
func (m tuiModel) windowFigures(profile Profile) string {
	usage, known := m.usage[profile.Name]
	if !known || !usage.known() {
		return dimStyle.Render("   no quota recorded")
	}
	figure := func(label string, window usageWindow) string {
		if !window.Known {
			return sectionLabelStyle.Render("   "+label+" ") + unknownStyle.Render("—")
		}
		return sectionLabelStyle.Render("   "+label+" ") + usageStyle(window).Render(fmt.Sprintf("%d%%", window.Percent))
	}
	return figure("5h", usage.FiveHour) + figure("7d", usage.Weekly)
}

// destinationLines ranks where the work could go by whichever window runs out
// first, each with its gauge and what is holding it back, and names the
// accounts that cannot take a brief at all rather than leaving them out.
func (m tuiModel) destinationLines(width, rows int) []string {
	lines := []string{sectionLabelStyle.Render("GOING TO") + dimStyle.Render("   ranked by whichever window runs out first"), ""}
	var list []string
	cursor := 0
	for index, profile := range m.handoff.destinations {
		if index > 0 {
			list = append(list, "")
		}
		selected := index == m.handoff.target
		if selected {
			cursor = len(list)
		}
		list = append(list, m.destinationRows(profile, selected, width)...)
	}
	blocked := m.cannotTakeBrief()
	tail := 0
	if len(blocked) > 0 {
		tail = 4
	}
	lines = append(lines, windowRows(list, cursor+1, max(rows-2-tail, 2))...)
	if len(blocked) > 0 {
		lines = append(lines, "", sectionLabelStyle.Render("CAN'T TAKE A BRIEF"),
			dimStyle.Render(truncate(strings.Join(blocked, " · "), width)),
			dimStyle.Render("no known way to open these on a prompt"))
	}
	return lines
}

func (m tuiModel) destinationRows(profile Profile, selected bool, width int) []string {
	ink := selectedPen(selected)
	bar, name := ink.render(lipgloss.NewStyle(), "  "), nameStyle
	if selected {
		bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
	}
	usage := m.usage[profile.Name]
	percent := headroom(usage)
	gaugeWidth := min(max(width-2-17-8-5-18, 0), 22)
	line := bar + ink.render(name, pad(truncate(profile.Name, 16), 17)) +
		ink.render(providerStyle(profile.Provider), pad(truncate(profile.Provider, 7), 8))
	note, noteStyle := "no quota recorded", unknownStyle
	if percent >= 0 {
		line += gauge(ink, percent, gaugeWidth) + ink.render(lipgloss.NewStyle(), " ") +
			ink.render(quotaStyle(percent), pad(fmt.Sprintf("%d%%", percent), 5))
		note, noteStyle = limitingNote(usage)
	}
	line += ink.render(noteStyle, note)
	detail := ink.render(dimStyle, "  "+windowDetail(m, "5h", usage.FiveHour)+"  ·  "+windowDetail(m, "7d", usage.Weekly))
	return []string{padStyled(ink, line, width), padStyled(ink, detail, width)}
}

// limitingNote says which window a destination's figure is: the one that runs
// out first, and a warning when that one is nearly gone.
func limitingNote(usage usageRemaining) (string, lipgloss.Style) {
	window := "5h"
	if usage.Weekly.Known && (!usage.FiveHour.Known || usage.Weekly.Percent < usage.FiveHour.Percent) {
		window = "7d"
	}
	if headroom(usage) <= 25 {
		return window + " nearly out", usageWarningStyle
	}
	return "limited by " + window, dimStyle
}

func windowDetail(m tuiModel, label string, window usageWindow) string {
	if !window.Known {
		return label + " —"
	}
	detail := fmt.Sprintf("%s %d%%", label, window.Percent)
	if reset := formatReset(m.clock(), window.Resets); reset != "" {
		detail += " " + reset
	}
	return detail
}

// cannotTakeBrief is every other account whose CLI cannot be started on a
// prompt, so the list of destinations is visibly short of them rather than
// silently.
func (m tuiModel) cannotTakeBrief() []string {
	source, _ := m.profileForRecord(m.handoff.source)
	var names []string
	for _, profile := range m.profiles {
		if profile.Name == source.Name {
			continue
		}
		if _, err := promptArgs(profile.Provider, ""); err != nil {
			names = append(names, profile.Name)
		}
	}
	return names
}

// ---- handoff: the brief ----------------------------------------------------

// briefDocWidth is the share of the brief step the brief itself takes; how the
// conversation ended sits beside it.
const briefDocWidth = 66

// handoffBriefContent is the third step, and with auto-swap on the only one
// shown: the brief as it was written, beside how the conversation ended, with
// the route and both accounts' figures on one line above them.
func (m tuiModel) handoffBriefContent(width, rows int) []string {
	target := Profile{Name: "somewhere"}
	if m.handoff.target < len(m.handoff.destinations) {
		target = m.handoff.destinations[m.handoff.target]
	}
	source, _ := m.profileForRecord(m.handoff.source)
	route := providerStyle(source.Provider).Bold(true).Render(source.Name) + m.windowFigures(source) +
		boxTitleStyle.Render("   ━━━━━━━━━━━━▶   ") +
		providerStyle(target.Provider).Bold(true).Render(target.Name) + m.windowFigures(target)
	// With auto-swap on this is the only step shown, so it names the work as
	// well as the route: the question left is whether this is what was meant
	// to move.
	title, titleStyle := m.handoff.source.session.title, fieldValueStyle.Bold(true)
	if title == "" {
		title, titleStyle = "untitled session", unknownStyle
	}
	what := titleStyle.Render(title) + dimStyle.Render("  ·  in "+shortenHome(m.handoff.source.folder))
	lines := []string{stepper(2), "", truncateStyled(route, width), truncateStyled(what, width), ""}
	body := max(rows-7, 6)
	if width < briefDocWidth+dividerWidth+30 {
		lines = append(lines, padToRows(m.briefLines(width), body, -1)...)
	} else {
		right := width - briefDocWidth - dividerWidth
		lines = append(lines, joinPanes(m.briefLines(briefDocWidth), m.endedLines(right, body), briefDocWidth, right, body)...)
	}
	return append(lines, "", boxFooter(width, helpEntry{"esc", "keep the brief, stay"},
		helpEntry{"↵", "open " + target.Name + " on the brief"}, helpEntry{"e", "edit the brief first"}, helpEntry{"←", "back"}))
}

// briefPromptRows is how many of the asks the brief step lists before saying
// how many more there are. The file has all of them.
const briefPromptRows = 3

// briefLines is what the brief says, section by section, as the incoming
// agent will read it: what was asked, where it was left, and the state of the
// repository.
func (m tuiModel) briefLines(width int) []string {
	accentLine := boxTitleStyle
	size := fmt.Sprintf("%s · %s", formatSize(m.handoff.size), approxTokens(m.handoff.size))
	lines := []string{
		sectionLabelStyle.Render("BRIEF") + dimStyle.Render(truncate("  "+shortenHome(m.handoff.path)+" · "+size, width-5)),
		"",
		accentLine.Render("What was asked"),
	}
	for index, prompt := range m.handoff.prompts {
		if index == briefPromptRows {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("   + %d more", len(m.handoff.prompts)-briefPromptRows)))
			break
		}
		first, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
		lines = append(lines, sectionLabelStyle.Render(pad(strconv.Itoa(index+1), 3))+modelStyle.Render(truncate(first, width-3)))
	}
	if len(m.handoff.notes) > 0 {
		lines = append(lines, "", accentLine.Render("Where it left off"))
		wrapped := wrapText(m.handoff.notes[len(m.handoff.notes)-1], width)
		if len(wrapped) > 2 {
			wrapped = wrapped[:2]
			wrapped[1] = truncate(wrapped[1], width-1) + "…"
		}
		for _, line := range wrapped {
			lines = append(lines, modelStyle.Render(line))
		}
	}
	lines = append(lines, "", accentLine.Render("The repository"))
	lines = append(lines, m.repositoryLines(width)...)
	return append(lines, "", dimStyle.Render("full transcript linked by path, not pasted"))
}

// briefChangedFiles caps the changed files the brief step lists by name.
const briefChangedFiles = 4

func (m tuiModel) repositoryLines(width int) []string {
	state := m.handoff.git
	if !state.repo {
		return []string{dimStyle.Render("not a git repository — no repository state in the brief")}
	}
	summary := fieldValueStyle.Render(state.branch)
	files, added, removed := diffStat(state.diffs)
	if files > 0 {
		summary += dimStyle.Render(" · "+plural(files, "file")+" changed · ") +
			liveStyle.Render(fmt.Sprintf("+%d", added)) + " " + statusErrStyle.Render(fmt.Sprintf("−%d", removed))
	} else {
		summary += dimStyle.Render(" · working tree clean")
	}
	lines := []string{summary}
	for index, entry := range strings.Split(state.status, "\n") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if index == briefChangedFiles {
			lines = append(lines, dimStyle.Render("  …"))
			break
		}
		lines = append(lines, modelStyle.Render(truncate("  "+strings.TrimSpace(entry[:min(2, len(entry))])+" "+strings.TrimSpace(entry[min(3, len(entry)):]), width)))
	}
	return lines
}

// diffStatSummary matches the last line of `git diff --stat`.
var diffStatSummary = regexp.MustCompile(`(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?`)

// diffStat reads the totals `git diff --stat` ends with. The brief keeps the
// whole stat; the box only needs its last line.
func diffStat(stat string) (files, added, removed int) {
	match := diffStatSummary.FindStringSubmatch(stat)
	if match == nil {
		return 0, 0, 0
	}
	files, _ = strconv.Atoi(match[1])
	added, _ = strconv.Atoi(match[2])
	removed, _ = strconv.Atoi(match[3])
	return files, added, removed
}

// formatSize is a byte count as the box shows a file's size.
func formatSize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%d KB", (size+512)/1024)
}

// approxTokens is a rough token count for a file of that size: four bytes a
// token is the usual rule of thumb for English prose and code, and the figure
// is only there to say whether the brief is a paragraph or a book.
func approxTokens(size int) string {
	tokens := size / 4
	if tokens < 1000 {
		return fmt.Sprintf("≈%d tokens", tokens)
	}
	return fmt.Sprintf("≈%.1fk tokens", float64(tokens)/1000)
}

// endedLines is how the conversation being handed over ended, cut to the pane.
// The brief opens with the same four lines every time, and none of them
// answers the question this step asks — whether this is the work that was
// meant to move; the last few things said answer it at a glance.
func (m tuiModel) endedLines(width, rows int) []string {
	lines := []string{sectionLabelStyle.Render("HOW IT ENDED")}
	height := max(rows-1, minBlockRows)
	if len(m.handoff.closing) == 0 {
		return append(lines, unknownStyle.Render("nothing was said in this session"))
	}
	body := conversationLines(m.handoff.provider, m.handoff.closing, width, briefTurnRows)
	return append(lines, fitTail(body, height, m.handoff.earlier)...)
}
