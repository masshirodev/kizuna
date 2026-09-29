package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The integrations box (`I`) shows what each profile has of the things
// `ai integrate` writes, and acts on them. It has two scopes, flipped with `a`:
// one profile, as a list beside a pane that explains the row under the cursor,
// and every profile, as a grid with the cell under the cursor explained below
// it. Both draw from integrationsFor, so neither can claim a state the launch
// disagrees with. doc/tui.md, "The boxes".

const (
	integrationsModalWidth = 124
	// integrationListWidth is the per-profile list: a bar, the integration's
	// name, and its state. Everything else goes to the pane that explains it.
	integrationListWidth = 40
	integrationNameWidth = 19
	// integrationDetailKeyWidth is the label column of that pane.
	integrationDetailKeyWidth = 11
	// The grid's columns: the account (bar, name, provider), then one cell per
	// integration, each a marker, the state, and its note.
	integrationAccountWidth  = 37
	integrationAccountName   = 22
	integrationCellWidth     = 27
	integrationCellWordWidth = 12
	// openUsageKeyWidth is the confirm box's label column.
	openUsageKeyWidth = 12
)

// integrationsDraft is the box's state while it is open. The statuses are read
// once, when it opens and after each action, rather than on every frame: they
// read settings files and lock directories, and a frame must not touch disk.
type integrationsDraft struct {
	all      bool
	profiles []Profile
	statuses [][]integrationStatus
	inRanma  bool
	// row is the per-profile cursor, over the rows that scope lists; account
	// and cell are the grid's.
	row     int
	account int
	cell    int
	// target is the profile an OpenUsage install is being confirmed for.
	target Profile
}

// openIntegrations opens the box on the selected profile.
func (m *tuiModel) openIntegrations() {
	if _, ok := m.selectedProfile(); !ok {
		return
	}
	m.integrations = integrationsDraft{}
	m.readIntegrations()
	m.integrations.row = m.firstActionableRow()
	m.mode = tuiIntegrations
	m.clearStatus()
}

// readIntegrations takes the statuses fresh, in the board's order, keeping the
// grid's cursor on the account it was on.
func (m *tuiModel) readIntegrations() {
	env := m.boardIntegrationEnv()
	current := ""
	if m.integrations.account < len(m.integrations.profiles) {
		current = m.integrations.profiles[m.integrations.account].Name
	}
	if selected, ok := m.selectedProfile(); ok && current == "" {
		current = selected.Name
	}
	profiles := boardOrder(m.profiles, m.usage)
	m.integrations.profiles = profiles
	m.integrations.statuses = make([][]integrationStatus, len(profiles))
	m.integrations.inRanma = env.inRanma
	for index, profile := range profiles {
		m.integrations.statuses[index] = integrationsFor(profile, env)
		if profile.Name == current {
			m.integrations.account = index
		}
	}
}

// boardIntegrationEnv is the machine as the board sees it. What is running
// comes from the board's own list rather than the lock directories, so the box
// agrees with the ▶ count beside the row, and a palette line does not read
// disk on every keystroke. The installer still takes the real lock.
func (m tuiModel) boardIntegrationEnv() integrationEnv {
	env := machineIntegrationEnv()
	env.running = func(profile Profile) bool { return m.runningCount(profile.Name) > 0 }
	return env
}

// integrationsOf is the statuses read for one profile.
func (m tuiModel) integrationsOf(name string) []integrationStatus {
	for index, profile := range m.integrations.profiles {
		if profile.Name == name {
			return m.integrations.statuses[index]
		}
	}
	return nil
}

// profileRows is what the per-profile scope lists: every integration that
// applies to the provider. One that does not is not a row there; the grid
// still shows it as n/a, since there it answers "who has it".
func (m tuiModel) profileRows() (Profile, []integrationStatus) {
	profile, ok := m.selectedProfile()
	if !ok {
		return Profile{}, nil
	}
	var rows []integrationStatus
	for _, status := range m.integrationsOf(profile.Name) {
		if status.state != stateNA {
			rows = append(rows, status)
		}
	}
	return profile, rows
}

// rowSelectable is whether the per-profile cursor may land on a row. A locked
// row has nothing to say until the profile stops, so the cursor passes it.
func rowSelectable(status integrationStatus) bool {
	return status.state != stateLocked
}

func (m tuiModel) firstActionableRow() int {
	_, rows := m.profileRows()
	for index, status := range rows {
		if rowSelectable(status) {
			return index
		}
	}
	return 0
}

func (m tuiModel) currentIntegration() (Profile, integrationStatus, bool) {
	if m.integrations.all {
		if m.integrations.account >= len(m.integrations.profiles) {
			return Profile{}, integrationStatus{}, false
		}
		statuses := m.integrations.statuses[m.integrations.account]
		if m.integrations.cell >= len(statuses) {
			return Profile{}, integrationStatus{}, false
		}
		return m.integrations.profiles[m.integrations.account], statuses[m.integrations.cell], true
	}
	profile, rows := m.profileRows()
	if m.integrations.row < 0 || m.integrations.row >= len(rows) {
		return Profile{}, integrationStatus{}, false
	}
	return profile, rows[m.integrations.row], true
}

func (m tuiModel) updateIntegrations(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "ctrl+c":
		m.mode = tuiList
		m.clearStatus()
	case "a", "I":
		m.flipIntegrationScope()
	case "up", "k":
		m.moveIntegrationCursor(-1)
	case "down", "j":
		m.moveIntegrationCursor(1)
	case "left", "h":
		if m.integrations.all && m.integrations.cell > 0 {
			m.integrations.cell--
			m.clearStatus()
		}
	case "right", "l":
		if m.integrations.all && m.integrations.cell < len(integrationKinds)-1 {
			m.integrations.cell++
			m.clearStatus()
		}
	case "m", "s":
		if m.integrations.all {
			return m, nil
		}
		m.mode = tuiList
		if msg.String() == "m" {
			m.openShare(shareMCP)
		} else {
			m.openShare(shareSkills)
		}
	case "enter":
		return m.actOnIntegration()
	}
	return m, nil
}

// flipIntegrationScope switches between one profile and every profile. Coming
// back from the grid, the profile is the one the grid's cursor was on, and the
// board follows it, so the box and the row behind it name the same account.
func (m *tuiModel) flipIntegrationScope() {
	m.clearStatus()
	if !m.integrations.all {
		m.integrations.all = true
		if selected, ok := m.selectedProfile(); ok {
			for index, profile := range m.integrations.profiles {
				if profile.Name == selected.Name {
					m.integrations.account = index
				}
			}
		}
		if _, status, ok := m.currentIntegrationRow(); ok {
			m.integrations.cell = int(status.kind)
		}
		return
	}
	m.integrations.all = false
	if m.integrations.account < len(m.integrations.profiles) {
		m.filter = ""
		m.followSelection(m.integrations.profiles[m.integrations.account].Name)
	}
	m.integrations.row = m.firstActionableRow()
}

// currentIntegrationRow is the per-profile row under the cursor, regardless
// of the scope being shown.
func (m tuiModel) currentIntegrationRow() (Profile, integrationStatus, bool) {
	profile, rows := m.profileRows()
	if m.integrations.row < 0 || m.integrations.row >= len(rows) {
		return Profile{}, integrationStatus{}, false
	}
	return profile, rows[m.integrations.row], true
}

func (m *tuiModel) moveIntegrationCursor(step int) {
	m.clearStatus()
	if m.integrations.all {
		next := m.integrations.account + step
		if next >= 0 && next < len(m.integrations.profiles) {
			m.integrations.account = next
		}
		return
	}
	_, rows := m.profileRows()
	for next := m.integrations.row + step; next >= 0 && next < len(rows); next += step {
		if rowSelectable(rows[next]) {
			m.integrations.row = next
			return
		}
	}
}

// actOnIntegration does what ↵ says on the row or cell: a config write happens
// here and now; the OpenUsage installer is shown before it runs.
func (m tuiModel) actOnIntegration() (tea.Model, tea.Cmd) {
	profile, status, ok := m.currentIntegration()
	if !ok {
		return m, nil
	}
	if status.action == "" {
		if status.reason != "" {
			m.setStatus(statusErr, status.reason)
		}
		return m, nil
	}
	switch status.kind {
	case integrationOpenUsage:
		m.integrations.target = profile
		m.mode = tuiConfirmOpenUsage
		m.clearStatus()
		return m, nil
	case integrationRanma:
		m.writeIntegration(func(cfg *Config) error {
			return setTmuxShim(profile, !profile.TmuxShim, cfg, m.configPath, io.Discard)
		})
		if m.statusKind != statusErr {
			if profile.TmuxShim {
				m.setStatus(statusOK, profile.Name+" no longer launches under ranma's tmux shim")
			} else {
				m.setStatus(statusOK, profile.Name+" launches under ranma's tmux shim from a ranma pane")
			}
		}
	case integrationStatusLine:
		m.writeIntegration(func(cfg *Config) error {
			return installIndicator(profile, cfg, m.configPath, io.Discard)
		})
		if m.statusKind != statusErr {
			if supportsNativeStatusLine(profile.Provider) {
				m.setStatus(statusOK, profile.Name+" renders its own status line now")
			} else {
				m.setStatus(statusOK, profile.Name+" launches inside a tmux status bar now")
			}
		}
	}
	return m, nil
}

// writeIntegration runs one config-writing integration against the file as it
// is on disk now, then rereads everything the box shows.
func (m *tuiModel) writeIntegration(write func(cfg *Config) error) {
	cfg, err := loadConfig(m.configPath)
	if err == nil {
		err = write(&cfg)
	}
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return
	}
	if cfg, err = loadConfig(m.configPath); err == nil {
		m.profiles = sortedProfiles(cfg.Profiles)
	}
	m.clearStatus()
	m.readIntegrations()
}

func (m tuiModel) updateConfirmOpenUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.mode = tuiList
		return m, m.execOpenUsage(m.integrations.target)
	case "n", "N", "esc", "q":
		m.mode = tuiIntegrations
		m.clearStatus()
	}
	return m, nil
}

// execOpenUsage runs OpenUsage's installer the way `ai integrate openusage`
// does: in the profile's environment, under its exclusive lock, so it cannot
// write hooks under a CLI that is running on them.
func (m *tuiModel) execOpenUsage(profile Profile) tea.Cmd {
	workdir, err := ensureProfileState(profile)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	lockDir, unlock, err := acquireExclusiveRunLock(workdir)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	cmd := exec.Command("openusage", "integrations", "install", openUsageIntegration(profile.Provider))
	cmd.Dir = m.workingDir
	cmd.Env = launchEnvironment(profile, workdir, workdir, os.Environ())
	m.running = true
	m.unlock = unlock
	return tea.Exec(trackedExecCommand{cmd: cmd, workdir: lockDir, title: sessionTitle(profile)}, func(err error) tea.Msg {
		return processFinishedMsg{err: err}
	})
}

// ---- drawing --------------------------------------------------------------

// integrationWordStyle colours a state the same way in both scopes: green for
// what ai has written, amber for what is in the way, faint for what is simply
// not there, dim for what cannot be had.
func integrationWordStyle(status integrationStatus) lipgloss.Style {
	switch status.state {
	case stateOn:
		return liveStyle
	case stateAnother, stateRefused, stateNeeds:
		return authMissingStyle
	case stateLocked, stateNA:
		return dimStyle
	}
	return sectionLabelStyle
}

func integrationNoteStyle(status integrationStatus) lipgloss.Style {
	if status.inert {
		return authKeyStyle
	}
	return dimStyle
}

func (m tuiModel) integrationsContent(width, rows int) []string {
	if m.integrations.all {
		return m.integrationGridContent(width, rows)
	}
	return m.integrationProfileContent(width, rows)
}

func (m tuiModel) integrationProfileContent(width, rows int) []string {
	profile, statuses := m.profileRows()
	if profile.Name == "" {
		return nil
	}
	lines := []string{spread(
		boxTitle("integrations")+dimStyle.Render("  for  ")+providerStyle(profile.Provider).Bold(true).Render(profile.Name),
		dimStyle.Render("checked the way a launch checks"), width), ""}
	for _, status := range statuses {
		if status.state == stateLocked {
			lines = append(lines, dimStyle.Render(truncate("running: "+status.reason, width)), "")
		}
	}
	left := []string{sectionLabelStyle.Render("INTEGRATION"), ""}
	for index, status := range statuses {
		left = append(left, integrationListRow(status, index == m.integrations.row))
	}
	var right []string
	current, hasCurrent := integrationStatus{}, false
	if m.integrations.row >= 0 && m.integrations.row < len(statuses) {
		current, hasCurrent = statuses[m.integrations.row], true
		right = integrationDetail(current, width-integrationListWidth-dividerWidth)
	}
	body := max(rows-len(lines)-2, 6)
	lines = append(lines, joinPanes(left, right, integrationListWidth, width-integrationListWidth-dividerWidth, body)...)
	keys := []helpEntry{{"↑↓", "choose"}}
	if hasCurrent && current.action != "" {
		keys = append(keys, helpEntry{"↵", current.action})
	}
	keys = append(keys, helpEntry{"a", "every profile"}, helpEntry{"m", "mcp servers"}, helpEntry{"s", "skills"})
	return append(lines, "", boxFooter(width, helpEntry{"esc", "close"}, keys...))
}

func integrationListRow(status integrationStatus, selected bool) string {
	ink := selectedPen(selected)
	bar, name, word := ink.render(lipgloss.NewStyle(), "  "), nameStyle, integrationWordStyle(status)
	if selected {
		bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
	}
	if !rowSelectable(status) {
		name, word = dimStyle, dimStyle
	}
	line := bar + ink.render(name, pad(status.kind.name(), integrationNameWidth)) + ink.render(word, status.word())
	return padStyled(ink, line, integrationListWidth)
}

// integrationDetail is the pane beside the list: what the row writes, what the
// launch adds, what it gives, how it is undone, whether it applies from here,
// the checks that decide it, and the command it stands for.
func integrationDetail(status integrationStatus, width int) []string {
	kv := func(key, value, note string) string {
		line := fieldLabelStyle.Render(pad(key, integrationDetailKeyWidth)) + fieldValueStyle.Render(value)
		if note != "" {
			line += dimStyle.Render("  " + note)
		}
		return truncateStyled(line, width)
	}
	heading := sectionLabelStyle.Render(strings.ToUpper(status.kind.name())) + "   " + integrationWordStyle(status).Render(status.word())
	if status.note != "" && status.word() != "● ai's line" {
		heading += integrationNoteStyle(status).Render("  " + status.note)
	}
	lines := []string{heading, ""}
	if status.reason != "" {
		lines = append(lines, truncateStyled(fieldValueStyle.Render(status.reason), width))
		if status.hint != "" {
			lines = append(lines, truncateStyled(dimStyle.Render(status.hint), width))
		}
		lines = append(lines, "")
	}
	if status.runs != "" {
		lines = append(lines, kv("runs", status.runs, ""))
	}
	lines = append(lines, kv("writes", status.writes, status.writesNote), kv("launch", status.launch, ""), kv("gives", status.gives, ""))
	if status.undo != "" {
		lines = append(lines, kv("undo", status.undo, status.undoNote))
	}
	if status.here != "" {
		here := dimStyle
		if !status.hereApplies || status.kind == integrationRanma {
			here = authKeyStyle
		}
		lines = append(lines, "", sectionLabelStyle.Render("HERE"),
			truncateStyled(here.Render(status.here), width),
			truncateStyled(dimStyle.Render(status.hereNote), width))
	}
	if len(status.checks) > 0 {
		lines = append(lines, "", sectionLabelStyle.Render("CHECKS"))
		for _, check := range status.checks {
			mark := authMissingStyle.Render("✗ ")
			if check.ok {
				mark = liveStyle.Render("✓ ")
			}
			line := mark + fieldValueStyle.Render(check.text)
			if check.note != "" {
				line += dimStyle.Render("  " + check.note)
			}
			lines = append(lines, truncateStyled(line, width))
		}
	}
	return append(lines, "", sectionLabelStyle.Render("SAME AS"), truncateStyled(fieldValueStyle.Render(status.sameAs), width))
}

func (m tuiModel) integrationGridContent(width, rows int) []string {
	where := "this TUI is not in a ranma pane"
	if m.integrations.inRanma {
		where = "this TUI is in a ranma pane"
	}
	lines := []string{
		spread(boxTitle("integrations")+dimStyle.Render("   every profile"), dimStyle.Render(where), width), "",
		columnHeaderStyle.Render("  " + pad("ACCOUNT", integrationAccountWidth-2) + pad("STATUS LINE", integrationCellWidth) +
			pad("OPENUSAGE", integrationCellWidth) + "RANMA SHIM"), "",
	}
	var table []string
	for index, profile := range m.integrations.profiles {
		table = append(table, m.integrationGridRow(profile, m.integrations.statuses[index], index == m.integrations.account, width))
	}
	explanation := m.integrationCellExplanation(width)
	// The table gets what the explanation and the legend leave, and scrolls
	// inside that rather than pushing them off the box.
	tableRows := max(rows-len(lines)-len(explanation)-6, 3)
	lines = append(lines, windowRows(table, m.integrations.account, tableRows)...)
	lines = append(lines, "", "  "+integrationLegend(), "", "")
	lines = append(lines, explanation...)
	for len(lines) < rows-2 {
		lines = append(lines, "")
	}
	keys := []helpEntry{{"↑↓", "account"}, {"←→", "integration"}}
	if _, status, ok := m.currentIntegration(); ok && status.action != "" {
		keys = append(keys, helpEntry{"↵", status.action})
	}
	keys = append(keys, helpEntry{"a", "this profile only"})
	return append(lines, "", boxFooter(width, helpEntry{"esc", "close"}, keys...))
}

func (m tuiModel) integrationGridRow(profile Profile, statuses []integrationStatus, selected bool, width int) string {
	ink := selectedPen(selected)
	bar, name := ink.render(lipgloss.NewStyle(), "  "), nameStyle
	if selected {
		bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
	}
	line := bar + ink.render(name, pad(truncate(profile.Name, integrationAccountName-1), integrationAccountName)) +
		ink.render(providerStyle(profile.Provider), pad(truncate(profile.Provider, integrationAccountWidth-integrationAccountName-3), integrationAccountWidth-integrationAccountName-2))
	for index, status := range statuses {
		onCell := selected && index == m.integrations.cell
		marker, word := ink.render(lipgloss.NewStyle(), " "), integrationWordStyle(status)
		if onCell {
			marker, word = ink.render(helpKeyStyle, "›"), word.Bold(true)
		}
		note := status.note
		if status.word() == "● ai's line" {
			// The word already says whose line it is.
			note = ""
		}
		cell := marker + ink.render(word, pad(status.word(), integrationCellWordWidth)) + ink.render(integrationNoteStyle(status), note)
		line += padStyled(ink, truncateStyled(cell, integrationCellWidth), integrationCellWidth)
	}
	return padStyled(ink, truncateStyled(line, width), width)
}

func integrationLegend() string {
	entries := []struct {
		text  string
		style lipgloss.Style
	}{
		{"● on", liveStyle}, {"○ off", sectionLabelStyle}, {"· not read", sectionLabelStyle},
		{"▶ locked while running", dimStyle}, {"— not for this provider", dimStyle}, {"✗ refused", authMissingStyle},
	}
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.style.Render(entry.text))
	}
	return strings.Join(parts, keyGap)
}

// integrationCellExplanation is the grid's answer for the cell under the
// cursor: its state in a sentence, what would move it, and the command.
func (m tuiModel) integrationCellExplanation(width int) []string {
	profile, status, ok := m.currentIntegration()
	if !ok {
		return nil
	}
	sentence, next := status.gives, status.hereNote
	if status.reason != "" {
		sentence, next = status.reason, status.hint
	}
	same := fieldLabelStyle.Render(pad("same as", integrationDetailKeyWidth)) + fieldValueStyle.Render(status.sameAs)
	if status.action == "" && status.state != stateOn {
		same += authMissingStyle.Render("  → refused")
	}
	lines := []string{
		sectionLabelStyle.Render(strings.ToUpper(profile.Name)) + dimStyle.Render("  ·  ") + sectionLabelStyle.Render(strings.ToUpper(status.kind.name())),
		truncateStyled(integrationWordStyle(status).Render(status.word())+fieldValueStyle.Render("  "+sentence), width),
	}
	if next != "" {
		lines = append(lines, truncateStyled(dimStyle.Render(next), width))
	}
	return append(lines, "", truncateStyled(same, width))
}

// openUsageConfirmContent shows the installer before it runs, as `i` does, and
// says outright that ai will not be able to tell afterwards that it ran.
func (m tuiModel) openUsageConfirmContent(width int) []string {
	profile := m.integrations.target
	kv := func(key, value, note string) string {
		line := fieldLabelStyle.Render(pad(key, openUsageKeyWidth)) + fieldValueStyle.Render(value)
		if note != "" {
			line += dimStyle.Render("  " + note)
		}
		return truncateStyled(line, width)
	}
	return []string{
		boxTitle("install openusage hooks") + "   " + providerStyle(profile.Provider).Render(profile.Name),
		"",
		kv("runs", "openusage integrations install "+openUsageIntegration(profile.Provider), ""),
		kv("with", profile.Name+"'s environment", strings.Join(profileEnvNames(profile), ", ")),
		kv("writes", "OpenUsage's hooks, beside the profile's own state", ""),
		"",
		confirmBodyStyle.Render("ai does not read these hooks back, so this row keeps saying"),
		confirmBodyStyle.Render("not read after it finishes. There is no undo from ai yet."),
		"",
		boxFooter(width, helpEntry{"n", "cancel"}, helpEntry{"y", "install"}),
	}
}

// profileEnvNames is the variables a launch sets to select the profile, by
// name only: their values are paths, and the names are what tell a reader
// the installer lands in the right profile.
func profileEnvNames(profile Profile) []string {
	names := []string{}
	for _, value := range profileEnv(profile) {
		name, _, _ := strings.Cut(value, "=")
		if name != profileNameEnv && name != profileProviderEnv {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return append(names, profileNameEnv)
}

// integrationsPaletteNote is what the palette says beside `I`: how many are
// on, and the one that is locked, if any.
func (m tuiModel) integrationsPaletteNote(profile Profile) string {
	statuses := integrationsFor(profile, m.boardIntegrationEnv())
	note := fmt.Sprintf("%d on", countOn(statuses))
	for _, status := range statuses {
		if status.state == stateLocked {
			note += " · " + status.kind.name() + " locked"
		}
	}
	return note
}
