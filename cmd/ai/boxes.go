package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Every box shares one vocabulary: an accent border, an uppercase accent
// title with the thing it acts on beside it, uppercase faint section labels,
// ▌ and a tinted row for the cursor, and a key line at the foot with the way
// out always on the right. The boxes below are that vocabulary applied to
// each question the cockpit asks.

// ---- profile editor -------------------------------------------------------

const (
	// formLabelWidth is the label column of the editor.
	formLabelWidth = 14
	// formValueWidth caps the well a value is typed into. Past it a field is
	// only a longer empty bar.
	formValueWidth = 60
)

// formContent is one field per row, the provider as a row of chips, and under
// them what the profile will actually launch as — the command, the isolated
// directory it gets, and how it shows its name — before anything is saved.
func (m tuiModel) formContent(width int) []string {
	title, name := "edit profile", m.form.original
	if m.form.isNew {
		title, name = "new profile", m.form.name
	}
	heading := boxTitle(title)
	if name != "" {
		heading += "   " + providerStyle(m.form.provider).Render(name)
	}
	valueWidth := min(formValueWidth, max(width-formLabelWidth, 8))
	lines := []string{
		spread(heading, dimStyle.Render("applies from the next launch"), width), "",
		m.formField(0, "name", m.form.name, valueWidth), "",
		m.formLabel(formProviderField, "provider") + m.providerChips(), "",
		m.formField(2, "command", m.form.command, valueWidth), "",
		m.formField(3, "default args", m.form.defaultArgs, valueWidth),
		strings.Repeat(" ", formLabelWidth) + dimStyle.Render(truncate("shell-style quotes group words; nothing is run through a shell", max(width-formLabelWidth, 8))), "",
		m.formField(4, "note", m.form.notes, valueWidth), "",
	}
	lines = append(lines, m.launchesAs(width)...)
	return append(lines, "", boxFooter(width, helpEntry{"esc", "discard"},
		helpEntry{"tab", "next field"}, helpEntry{"←→", "provider"}, helpEntry{"↵", "save"}))
}

func (m tuiModel) formLabel(field int, label string) string {
	if field == m.form.field {
		return fieldLabelActive.Render(pad(label, formLabelWidth))
	}
	return fieldLabelStyle.Render(pad(label, formLabelWidth))
}

// formField draws a value in its well: tinted and carrying the cursor when it
// is the field being typed into, and in the quieter field tone otherwise, so
// every field reads as editable and only one as active.
func (m tuiModel) formField(field int, label, value string, width int) string {
	active := field == m.form.field
	ink := pen{background: colorField}
	if active {
		ink = selectedPen(true)
	}
	var well string
	if active {
		well = ink.render(fieldValueStyle, " ") + caretView(ink, fieldValueStyle, value, m.form.tail, width-2)
	} else {
		well = ink.render(fieldValueStyle, " "+truncate(value, width-3))
	}
	if gap := width - lipgloss.Width(well); gap > 0 {
		well += ink.render(lipgloss.NewStyle(), strings.Repeat(" ", gap))
	}
	return m.formLabel(field, label) + well
}

// providerChips draws the providers as a row, the chosen one reversed into
// its own colour. A provider the config names that is not one of the known
// ones is still shown, rather than the row claiming nothing is chosen.
func (m tuiModel) providerChips() string {
	providers := knownProviders
	known := false
	for _, provider := range knownProviders {
		known = known || provider == m.form.provider
	}
	if !known && m.form.provider != "" {
		providers = append(append([]string(nil), knownProviders...), m.form.provider)
	}
	chips := make([]string, 0, len(providers))
	for _, provider := range providers {
		if provider == m.form.provider {
			chips = append(chips, providerBadgeStyle(provider).Render(provider))
		} else {
			chips = append(chips, providerStyle(provider).Render(" "+provider+" "))
		}
	}
	return strings.Join(chips, " ")
}

// launchesAs is the profile as the launcher will run it: the real command
// line, the isolated directory the provider is pointed at, and how the account
// names itself once it is running.
func (m tuiModel) launchesAs(width int) []string {
	command := strings.TrimSpace(m.form.command + " " + m.form.defaultArgs)
	if args, err := parseArguments(m.form.defaultArgs); err == nil {
		command = formatArguments(append([]string{m.form.command}, args...))
	}
	lines := []string{
		sectionLabelStyle.Render("LAUNCHES AS"),
		fieldValueStyle.Render(truncate(command, max(width-26, 8))) + dimStyle.Render("  + anything typed with p"),
	}
	const envLabel = 18
	for _, entry := range profileEnv(Profile{Name: m.form.name, Provider: m.form.provider}) {
		key, value, _ := strings.Cut(entry, "=")
		if key == profileNameEnv || key == profileProviderEnv {
			continue
		}
		if value == "" {
			value = "(unset)"
		}
		lines = append(lines, fieldLabelStyle.Render(pad(key, envLabel))+
			dimStyle.Render(truncate(shortenHome(value), max(width-envLabel, 8))))
	}
	return append(lines, fieldLabelStyle.Render(pad("status line", envLabel))+m.indicatorSummary())
}

// indicatorSummary says how the running account will show which one it is.
func (m tuiModel) indicatorSummary() string {
	if supportsNativeStatusLine(m.form.provider) {
		return fieldValueStyle.Render("its own") + dimStyle.Render(" — "+m.form.provider+" renders AI_PROFILE itself")
	}
	if profile := profileNamed(m.profiles, m.form.original); profile.Indicator == tmuxIndicator {
		return fieldValueStyle.Render("tmux status bar") + dimStyle.Render(" — the launcher wraps the session")
	}
	return unknownStyle.Render("none") + dimStyle.Render(" — AI_PROFILE is still set for a prompt to read")
}

// ---- small prompts --------------------------------------------------------

// promptField is a single-line input in the tinted well, with the cursor.
func promptField(label, value string, tail, width int) string {
	ink := selectedPen(true)
	well := ink.render(fieldValueStyle, " ") + caretView(ink, fieldValueStyle, value, tail, max(width-detailLabelWidth-2, 5))
	if gap := width - detailLabelWidth - lipgloss.Width(well); gap > 0 {
		well += ink.render(lipgloss.NewStyle(), strings.Repeat(" ", gap))
	}
	return fieldLabelActive.Render(pad(label, detailLabelWidth)) + well
}

func (m tuiModel) folderContent(width int) []string {
	return []string{
		boxTitle("change launch folder"),
		"",
		promptField("folder", m.folderPath, m.folderTail, width),
		"",
		hintStyle.Render(truncate("Relative paths use the current launch folder. ~ is supported.", width)),
		"",
		boxFooter(width, helpEntry{"esc", "cancel"}, helpEntry{"↵", "set folder"}, helpEntry{"ctrl-u", "clear"}),
	}
}

// cloneContent asks for the name and says what the new profile will and will
// not have. The second half matters more than the first: "clone" reads as a
// duplicate account, and this one is a duplicate setup.
func (m tuiModel) cloneContent(width int) []string {
	source, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	what := "settings, MCP servers, and skills"
	if len(profileConfigPaths(source.Provider)) == 0 {
		what = "launch settings"
	}
	return []string{
		boxTitle("clone") + "   " + providerStyle(source.Provider).Render(source.Name),
		"",
		promptField("name", m.clone, m.cloneTail, width),
		"",
		confirmBodyStyle.Render(truncate("Copies its "+what+".", width)),
		hintStyle.Render(truncate("Credentials do not come with it — the clone starts logged out.", width)),
		hintStyle.Render(truncate("Moving an account instead: ai profile export, then import.", width)),
		"",
		boxFooter(width, helpEntry{"esc", "cancel"}, helpEntry{"↵", "clone"}, helpEntry{"ctrl-u", "clear"}),
	}
}

func (m tuiModel) installContent(width int) []string {
	value := max(width-detailLabelWidth, 8)
	return []string{
		boxTitle("install the " + m.install.provider + " CLI"),
		"",
		fieldLabelStyle.Render(pad("runs", detailLabelWidth)) + fieldValueStyle.Render(truncate(m.install.command(), value)),
		fieldLabelStyle.Render(pad("provides", detailLabelWidth)) + fieldValueStyle.Render(truncate(defaultCommand(m.install.provider)+" on PATH", value)),
		"",
		confirmBodyStyle.Render("The script is downloaded before it is run, and runs in your"),
		confirmBodyStyle.Render("own environment rather than a profile's isolated one."),
		"",
		boxFooter(width, helpEntry{"n", "cancel"}, helpEntry{"y", "install"}),
	}
}

func (m tuiModel) selfUpdateContent(width int) []string {
	value := max(width-detailLabelWidth, 8)
	lines := []string{
		boxTitle("update ai-session"),
		"",
		fieldLabelStyle.Render(pad("checkout", detailLabelWidth)) + fieldValueStyle.Render(truncate(shortenHome(m.source), value)),
		fieldLabelStyle.Render(pad("status", detailLabelWidth)) + fieldValueStyle.Render(truncate(m.update.message(), value)),
		"",
	}
	for _, step := range selfUpdateSteps(m.source) {
		lines = append(lines, hintStyle.Render(truncate("› "+strings.Join(step, " "), width)))
	}
	return append(lines,
		"",
		confirmBodyStyle.Render("ai reopens itself on the rebuilt binary when this finishes."),
		"",
		boxFooter(width, helpEntry{"n", "cancel"}, helpEntry{"y", "update"}))
}

func (m tuiModel) confirmContent(width int) []string {
	profile, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	name := providerStyle(profile.Provider).Render(profile.Name)
	switch m.mode {
	case tuiHijack:
		lines := append([]string{boxTitle("open a running session here") + "   " + name, ""}, m.instanceRows(width)...)
		return append(lines, "",
			hintStyle.Render(truncate("The original instance keeps running; this opens its conversation.", width)),
			"",
			boxFooter(width, helpEntry{"esc", "cancel"}, helpEntry{"↑↓", "choose"}, helpEntry{"↵", "open here"}))
	case tuiConfirmKill:
		lines := append([]string{boxDangerTitleStyle.Render("STOP AN INSTANCE") + "   " + name, ""}, m.instanceRows(width)...)
		return append(lines, "",
			confirmBodyStyle.Render(truncate("Enter stops the selected instance. a or y stops them all.", width)),
			"",
			boxFooter(width, helpEntry{"n", "keep running"}, helpEntry{"↑↓", "choose"}, helpEntry{"↵", "stop this one"}, helpEntry{"a", "stop all"}))
	default:
		return []string{
			boxDangerTitleStyle.Render("DELETE "+strings.ToUpper(profile.Name)) + "   " + dimStyle.Render("this cannot be undone"),
			"",
			confirmBodyStyle.Render("Its isolated state directory and stored credentials"),
			confirmBodyStyle.Render("are removed."),
			"",
			boxFooter(width, helpEntry{"n", "keep"}, helpEntry{"y", "delete"}),
		}
	}
}

// instanceRows lists running instances, two lines each: what it has open, and
// where and for how long. Two instances of one profile are told apart by what
// they are doing rather than by PID.
func (m tuiModel) instanceRows(width int) []string {
	rows := make([]string, 0, len(m.instances)*2)
	for index, instance := range m.instances {
		selected := index == m.instance
		ink := selectedPen(selected)
		bar := ink.render(lipgloss.NewStyle(), "  ")
		title := nameStyle
		if selected {
			bar, title = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
		}
		label := fmt.Sprintf("Instance %d (PID %d)  ", index+1, instance.pid)
		line := bar + ink.render(dimStyle, label) + ink.render(title, truncate(m.instanceTitle(instance), max(width-2-len(label), 4)))
		rows = append(rows, padStyled(ink, line, width),
			"    "+hintStyle.Render(truncate(shortenHome(instance.folder)+" · "+formatUptime(instance.uptime(m.clock())), max(width-4, 4))))
	}
	return rows
}

// ---- arguments prompt -----------------------------------------------------

// paramsContent is the field on top with the whole command it produces under
// it — the defaults placed the way the launch will place them, so the
// subcommand rule is visible before anything runs — and the sets there are to
// reuse below: pinned ones first, then the most recent.
func (m tuiModel) paramsContent(width, rows int) []string {
	profile, _ := m.selectedProfile()
	ink := selectedPen(true)
	field := ink.render(helpKeyStyle.Bold(true), "› ") + caretView(ink, fieldValueStyle, m.params, m.paramsTail, max(width-3, 5))
	field = padStyled(ink, field, width)
	runs := sectionLabelStyle.Render("runs  ") + dimStyle.Render(m.commandPreview(profile)) +
		dimStyle.Render("   defaults placed by subcommand")
	lines := []string{
		spread(boxTitle("run with arguments")+"   "+providerStyle(profile.Provider).Render(profile.Name),
			dimStyle.Render("history is shared by every profile"), width),
		"",
		field,
		truncateStyled(runs, width),
		"",
	}
	list, cursor := m.argumentRows(width, profile)
	lines = append(lines, windowRows(list, cursor, max(rows-4, 4))...)
	return append(lines, "", boxFooter(width, helpEntry{"esc", "cancel"},
		helpEntry{"↑↓", "pick"}, helpEntry{"ctrl-p", "pin / unpin"}, helpEntry{"↵", "run"}))
}

// commandPreview is the command the pending launch will produce. While the
// field is still being typed it may not parse; then it shows the words as they
// stand after the stored defaults, which is the best guess available.
func (m tuiModel) commandPreview(profile Profile) string {
	command := []string{profile.Command}
	if args, err := parseArguments(m.params); err == nil {
		return formatArguments(append(command, profileRunArgs(profile, args, false)...))
	}
	line := formatArguments(append(command, profile.DefaultArgs...))
	if m.params == "" {
		return line
	}
	return line + " " + m.params
}

// argumentRows renders both sections and reports which line the highlight is
// on, so a list taller than the box can be windowed around it. Each row names
// the account it last went to, because the list is shared by every provider
// and a flag is only meaningful to the CLI it was written for — a set last run
// on another CLI is marked as such.
func (m tuiModel) argumentRows(width int, current Profile) ([]string, int) {
	entries := m.arguments.entries()
	if len(entries) == 0 {
		return []string{hintStyle.Render(truncate(fmt.Sprintf("Arguments you run are kept here, the last %d of them — ctrl-p pins a set for good.", recentArgumentLimit), width))}, 0
	}
	const profileWidth, whenWidth, otherWidth = 18, 11, 9
	argsWidth := min(max(width-4-profileWidth-whenWidth-otherWidth, 8), 48)
	var lines []string
	cursor, row := 0, 0
	section := func(title string, sets []argumentSet, pinned bool) {
		lines = append(lines, sectionLabelStyle.Render(title))
		if len(sets) == 0 {
			lines = append(lines, dimStyle.Render("  none"))
		}
		for _, set := range sets {
			selected := row == m.argumentRow
			if selected {
				cursor = len(lines)
			}
			ink := selectedPen(selected)
			bar, args := ink.render(lipgloss.NewStyle(), "  "), fieldValueStyle
			if selected {
				bar, args = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
			}
			pin := ink.render(lipgloss.NewStyle(), "  ")
			if pinned {
				pin = ink.render(helpKeyStyle, "◆ ")
			}
			// A set pinned straight from the field has never been run anywhere,
			// and says so rather than borrowing whichever account was selected
			// when it was pinned.
			account, when := ink.render(dimStyle, pad("—", profileWidth)), "never run"
			if set.Profile != "" {
				account = ink.render(providerStyle(set.Provider), pad(truncate(set.Profile, profileWidth-1), profileWidth))
				when = formatWhen(m.clock(), set.Used)
			}
			line := bar + pin + ink.render(args, pad(truncate(formatArguments(set.Args), argsWidth), argsWidth+2)) +
				account + ink.render(dimStyle, pad(when, whenWidth))
			if set.Provider != "" && set.Provider != current.Provider {
				line += ink.render(authMissingStyle, "other CLI")
			}
			lines = append(lines, padStyled(ink, line, width))
			row++
		}
	}
	section("PINNED", m.arguments.Pinned, true)
	lines = append(lines, "")
	section("RECENT", m.arguments.Recent, false)
	return lines, cursor
}

// ---- MCP / skill picker ---------------------------------------------------

// shareSourceWidth is the lender column. It is a name, a provider and a count;
// everything else in the box goes to what is being lent.
const shareSourceWidth = 36

// shareContent is the whole copy in one box: who could lend on the left, what
// the chosen one has on the right, ticked in place, and — for MCP servers
// crossing between CLIs — how each will be rewritten on the way.
func (m tuiModel) shareContent(width, rows int) []string {
	destination, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	title := "install mcp servers"
	if m.share.kind == shareSkills {
		title = "install skills"
	}
	mcpTab, skillTab := sectionLabelStyle.Render("  mcp "), sectionLabelStyle.Render("  skills")
	if m.share.kind == shareSkills {
		skillTab = "  " + selectedPen(true).render(boxTitleStyle, " skills ")
	} else {
		mcpTab = selectedPen(true).render(boxTitleStyle, " mcp ")
	}
	heading := spread(
		boxTitle(title)+dimStyle.Render("  into  ")+providerStyle(destination.Provider).Bold(true).Render(destination.Name),
		mcpTab+skillTab+helpKeyStyle.Render("  tab"), width)

	right := width - shareSourceWidth - dividerWidth
	body := max(rows-4, 6)
	panes := joinPanes(m.shareSourceLines(shareSourceWidth), m.shareItemLines(destination, right, body), shareSourceWidth, right, body)
	lines := append([]string{heading, ""}, panes...)
	install := "install"
	if ticked := len(m.share.chosenNames()); ticked > 0 {
		install = fmt.Sprintf("install %d", ticked)
	}
	return append(lines, "", boxFooter(width, helpEntry{"esc", "cancel"},
		helpEntry{"space", "tick"}, helpEntry{"a", "tick all new"}, helpEntry{"←→", "pane"}, helpEntry{"↵", install}))
}

func (m tuiModel) shareSourceLines(width int) []string {
	lines := []string{sectionLabelStyle.Render("FROM"), ""}
	for index, source := range m.share.sources {
		selected := index == m.share.source
		ink := selectedPen(selected)
		bar, name := ink.render(lipgloss.NewStyle(), "  "), nameStyle
		if selected {
			bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
		}
		line := bar + ink.render(name, pad(truncate(source.profile.Name, 16), 17)) +
			ink.render(providerStyle(source.profile.Provider), pad(truncate(source.profile.Provider, 8), 9)) +
			ink.render(modelStyle, fmt.Sprint(source.count))
		lines = append(lines, padStyled(ink, line, width))
	}
	if len(m.share.dry) > 0 {
		lines = append(lines, "", dimStyle.Render("  nothing to lend"))
		for _, profile := range m.share.dry {
			lines = append(lines, dimStyle.Render("  "+truncate(profile.Name, width-2)))
		}
	}
	return lines
}

func (m tuiModel) shareItemLines(destination Profile, width, rows int) []string {
	source, ok := m.shareSource()
	if !ok {
		return nil
	}
	noun := "SERVERS"
	if m.share.kind == shareSkills {
		noun = "SKILLS"
	}
	heading := sectionLabelStyle.Render(noun+" IN ") + providerStyle(source.Provider).Render(source.Name)
	if ticked := len(m.share.chosenNames()); ticked > 0 {
		heading += boxTitleStyle.Render(fmt.Sprintf("   %d ticked", ticked))
	}
	var translation []string
	if m.share.kind == shareMCP {
		translation = m.translationLines(source, destination, width)
	}
	items := m.shareItemRows(width)
	listRows := max(rows-2-len(translation), 3)
	lines := append([]string{heading, ""}, windowRows(items, m.share.item, listRows)...)
	return append(lines, translation...)
}

// shareItemRows is the multi-select. A row the destination already has shows
// as installed with no box, so it does not read as one more thing to tick; it
// can still be ticked on purpose, which is how a definition is replaced.
func (m tuiModel) shareItemRows(width int) []string {
	const tickWidth, kindWidth, installedWidth = 4, 7, 10
	nameWidth := min(max(width/5, 10), 20)
	rows := make([]string, 0, len(m.share.items))
	for index, item := range m.share.items {
		selected := index == m.share.item
		ink := selectedPen(selected)
		bar, name := ink.render(lipgloss.NewStyle(), "  "), fieldValueStyle
		if selected {
			bar = ink.render(cursorBarStyle, "▌ ")
			if m.share.focus == shareFocusItems {
				name = nameActiveStyle
			}
		}
		tick := ink.render(sectionLabelStyle, "[ ] ")
		switch {
		case item.chosen:
			tick = ink.render(boxTitleStyle, "[x] ")
		case item.present:
			tick = ink.render(lipgloss.NewStyle(), "    ")
			name = sectionLabelStyle
		}
		kind := ""
		if item.server != nil {
			kind = item.server.transport()
		}
		detail := max(width-2-tickWidth-nameWidth-kindWidth-installedWidth, 4)
		line := bar + tick + ink.render(name, pad(truncate(item.name, nameWidth-1), nameWidth)) +
			ink.render(modelStyle, pad(kind, kindWidth)) +
			ink.render(dimStyle, pad(truncate(item.detail, detail-1), detail))
		if item.present {
			line += ink.render(sectionLabelStyle, "installed")
		}
		rows = append(rows, padStyled(ink, line, width))
	}
	return rows
}

// translationLines says how the ticked servers will be written for the
// destination CLI. A copy between two CLIs rewrites references to the
// environment, and a rewrite nobody was shown is how a server silently stops
// authenticating.
func (m tuiModel) translationLines(source, destination Profile, width int) []string {
	if source.Provider == destination.Provider {
		return nil
	}
	where := "written to its own config"
	if file, err := mcpConfigFile(destination); err == nil {
		where = "written to " + shortenHome(file)
	}
	lines := []string{"", sectionLabelStyle.Render("TRANSLATED ON THE WAY"),
		truncateStyled(fieldValueStyle.Render(source.Provider+" → "+destination.Provider)+dimStyle.Render("  "+where), width)}
	for _, item := range m.share.items {
		if !item.chosen || item.server == nil {
			continue
		}
		for _, note := range mcpTranslationNotes(*item.server, source.Provider, destination.Provider) {
			lines = append(lines, truncateStyled(modelStyle.Render(item.name)+dimStyle.Render("  "+note), width))
		}
	}
	return lines
}

// joinPanes puts two halves side by side at exactly rows lines, each padded to
// its own width. The gutter is the board's hairline, so a box split in two
// reads as the same kind of division as the rules around the frame.
func joinPanes(left, right []string, leftWidth, rightWidth, rows int) []string {
	divider := " " + ruleStyle.Render("│") + " "
	lines := make([]string, 0, rows)
	for row := range rows {
		first, second := "", ""
		if row < len(left) {
			first = left[row]
		}
		if row < len(right) {
			second = right[row]
		}
		lines = append(lines, padLine(first, leftWidth)+divider+padLine(second, rightWidth))
	}
	return lines
}
