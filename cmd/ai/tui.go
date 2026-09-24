package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tuiMode int

const (
	tuiList tuiMode = iota
	tuiForm
	tuiFolder
	tuiConfirmDelete
	tuiConfirmKill
	tuiHijack
	tuiRecent
	tuiHandoff
	tuiHandoffTo
	tuiHandoffBrief
	tuiParams
	tuiClone
	tuiShare
	tuiConfirmInstall
	tuiConfirmSelfUpdate
	tuiPalette
)

type profileForm struct {
	name        string
	provider    string
	command     string
	defaultArgs string
	notes       string
	field       int
	original    string
	isNew       bool
}

type processFinishedMsg struct {
	err error
}

type usageLoadedMsg map[string]usageRemaining

// updateCheckedMsg carries the update check's answer. A forced check reports
// itself in the status line; the one at startup stays quiet unless it has
// something to offer.
type updateCheckedMsg struct {
	status updateStatus
	forced bool
}

// instancesDescribedMsg carries the session titles resolved for one profile's
// running instances. The lookup talks to the provider, so it happens off the
// keypress that opened the picker.
type instancesDescribedMsg struct {
	profile   string
	instances []profileInstance
}

// sessionPreviewMsg carries one conversation read for the picker pane. It names
// the profile as well as the session, because the read is started off the
// keypress and the account under the cursor can change before it lands.
type sessionPreviewMsg struct {
	profile string
	preview sessionPreview
}

// recentAllLoadedMsg carries every profile's recent sessions read as one union,
// which is what a picker lists after it is switched to all profiles.
type recentAllLoadedMsg struct {
	records []recordedSession
}

type statusKind int

const (
	statusNone statusKind = iota
	statusOK
	statusErr
)

type tuiModel struct {
	configPath string
	profiles   []Profile
	cursor     int
	mode       tuiMode
	form       profileForm
	status     string
	statusKind statusKind
	log        []logEntry
	width      int
	height     int
	running    bool
	unlock     func() string
	usage      map[string]usageRemaining
	workingDir string
	folderPath string
	params     string
	// arguments is what the argument prompt offers to reuse, and argumentRow is
	// the row picked from it, or -1 while the text is being typed. argumentDraft is
	// what was typed before a row was picked, so stepping back up to the field
	// returns it rather than leaving the recalled set in its place.
	arguments     argumentHistory
	argumentRow   int
	argumentDraft string
	instances     []profileInstance
	instance      int
	// record indexes recent while the resume picker is open. It is separate
	// from instance because the two pickers offer different things: one lists
	// processes, the other lists transcripts.
	record     int
	describing bool
	// recentAll is every profile's recent sessions as one union, read when a
	// picker is switched away from the selected profile. pickerAll is that
	// switch; recentFilter and recentSearching are the picker's own search,
	// kept apart from the profile list's filter so a query typed in one is
	// never applied to the other.
	recentAll       []recordedSession
	recentAllLoaded bool
	pickerAll       bool
	recentFilter    string
	recentSearching bool
	// preview is the conversation shown beside the picker list, and previews is
	// what has already been read while this picker has been open. The cache is
	// what makes moving the cursor down and back free: a transcript re-read on
	// every pass is the one thing a pane that follows the cursor must not do.
	// Both are dropped when a picker opens, so a preview is never older than
	// the picker showing it.
	preview  sessionPreview
	previews map[string]sessionPreview
	// handoff is the pass being set up: the session leaving, where it is going,
	// and the brief written for it. It is one struct rather than four fields
	// because the three modals are one decision, and a half-built pass must not
	// survive an escape out of the middle of it.
	handoff handoffDraft
	// clone is the name being typed for a copy of the selected profile, and
	// share is a copy of MCP servers or skills being set up between two of
	// them. Both are kept on the model rather than passed between modes so an
	// escape out of the middle of one leaves nothing half-built behind.
	clone string
	share shareDraft
	// paletteFilter is what has been typed into the all-actions palette, and
	// paletteRow the highlighted action among those it lets through.
	paletteFilter string
	paletteRow    int
	autoSwap      bool
	lineage       map[string]lineageLink
	update        updateStatus
	// install and source are what a pending confirmation is about: the vendor
	// installer that would run, and the checkout ai would rebuild itself from.
	// Both are resolved on the keypress so the box can name them before the
	// answer, rather than after.
	install providerInstall
	source  string
	// filter narrows the accounts column; searching is whether the query is
	// still being typed. The cursor indexes the filtered list, not profiles.
	filter    string
	searching bool
	// live, recent and activity are the cockpit's read-only panels. They are
	// filled by commands so nothing that touches the disk runs on a keypress.
	// loaded says whether the first read has landed, which is what separates
	// "nothing is running" from "not asked yet" on the opening frame.
	loaded   bool
	live     []profileInstance
	recent   []recordedSession
	activity activity
	// now is the clock the frame was rendered against, so uptimes and reset
	// times are stable within a frame and fixed under test.
	now time.Time
}

// logEntry is one line of the log panel.
type logEntry struct {
	kind statusKind
	text string
	// at is when it happened, which the board's status line shows beside it:
	// the last thing that happened is only useful if it is recent.
	at time.Time
}

// logLimit is how many past messages the log panel keeps. It is short on
// purpose: the panel is there to hold the last thing that went wrong long
// enough to read, not to become a transcript.
const logLimit = 8

func (m tuiModel) clock() time.Time {
	if m.now.IsZero() {
		return time.Now()
	}
	return m.now
}

// visibleProfiles is the list the cursor moves through, in the order the board
// draws it. Everything that acts on "the selected profile" goes through it, so
// a filtered or re-ranked list can never run the account that merely happens
// to sit at the same index in another one.
func (m tuiModel) visibleProfiles() []Profile {
	if m.filter == "" {
		return boardOrder(m.profiles, m.usage)
	}
	query := strings.ToLower(m.filter)
	matches := make([]Profile, 0, len(m.profiles))
	for _, profile := range m.profiles {
		if strings.Contains(strings.ToLower(profile.Name), query) ||
			strings.Contains(strings.ToLower(profile.Provider), query) {
			matches = append(matches, profile)
		}
	}
	return boardOrder(matches, m.usage)
}

// followSelection puts the cursor back on the named profile after the board
// has been re-ranked under it. The board sorts by headroom, so a quota refresh
// can move every row; the cursor belongs to the account, not to the index.
func (m *tuiModel) followSelection(name string) {
	for index, profile := range m.visibleProfiles() {
		if profile.Name == name {
			m.cursor = index
			return
		}
	}
	m.clampCursor()
}

func (m tuiModel) selectedProfile() (Profile, bool) {
	visible := m.visibleProfiles()
	if m.cursor < 0 || m.cursor >= len(visible) {
		return Profile{}, false
	}
	return visible[m.cursor], true
}

// clampCursor keeps the cursor on a profile that is actually on screen after
// the list changes underneath it — a filter keystroke, or a deletion.
func (m *tuiModel) clampCursor() {
	if visible := len(m.visibleProfiles()); m.cursor >= visible {
		m.cursor = max(visible-1, 0)
	}
}

// pickerSessions is the list a resume or handoff picker is choosing from: the
// selected profile's own recent list, or every profile's when the picker has
// been switched to all of them.
func (m tuiModel) pickerSessions() []recordedSession {
	if m.pickerAll {
		return m.recentAll
	}
	return m.recent
}

// visibleRecent is the picker list after its own search is applied. The cursor
// indexes this, not the unfiltered list, so an action can never land on a
// session the query filtered out. The search covers what a row shows and what
// it does not: the title, the folder, the account, and the conversation id.
func (m tuiModel) visibleRecent() []recordedSession {
	sessions := m.pickerSessions()
	if m.recentFilter == "" {
		return sessions
	}
	query := strings.ToLower(m.recentFilter)
	matches := make([]recordedSession, 0, len(sessions))
	for _, record := range sessions {
		if strings.Contains(strings.ToLower(record.session.title), query) ||
			strings.Contains(strings.ToLower(record.folder), query) ||
			strings.Contains(strings.ToLower(record.session.id), query) ||
			strings.Contains(strings.ToLower(record.profile), query) {
			matches = append(matches, record)
		}
	}
	return matches
}

// clampRecord keeps the picker cursor on a row that is still listed after a
// search keystroke or a switch to another set of profiles.
func (m *tuiModel) clampRecord() {
	if visible := len(m.visibleRecent()); m.record >= visible {
		m.record = max(visible-1, 0)
	}
}

// profileForRecord resolves the account that recorded a conversation. The
// record carries its profile when it came from the union reader; a record read
// for one known profile falls back to the selection.
func (m tuiModel) profileForRecord(record recordedSession) (Profile, bool) {
	if record.profile != "" {
		if profile := profileNamed(m.profiles, record.profile); profile.Name != "" {
			return profile, true
		}
	}
	return m.selectedProfile()
}

func (m *tuiModel) setStatus(kind statusKind, message string) {
	m.statusKind = kind
	m.status = message
	if message == "" {
		return
	}
	m.log = append([]logEntry{{kind: kind, text: message, at: m.clock()}}, m.log...)
	if len(m.log) > logLimit {
		m.log = m.log[:logLimit]
	}
}

// statusSuffix appends a merge note to a status message, or nothing when the
// merge had nothing worth reporting.
func statusSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}

func (m *tuiModel) clearStatus() {
	m.statusKind = statusNone
	m.status = ""
}

func runTUI() error {
	configPath, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return err
	}
	m := tuiModel{
		configPath: configPath,
		profiles:   sortedProfiles(cfg.Profiles),
		workingDir: workingDir,
		autoSwap:   cfg.Settings.AutoSwap,
		lineage:    handedOff(readLineage()),
	}
	// Same reclaim as the CLI path: a TUI opened after a power-off or a
	// term kill merges whatever the dead launches left behind.
	for _, note := range reclaimStrayIsolatedInstances(cfg) {
		m.log = append([]logEntry{{kind: statusOK, text: note}}, m.log...)
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(loadUsageCmd(m.profiles), checkUpdateCmd(false), m.loadCockpitCmd(), cockpitTickCmd())
}

// cockpitRefresh is how often the live panels are re-read. Instances come and
// go without ai-session being told, so the panel that claims to say what is
// running has to ask again; ten seconds is often enough to feel current and
// rare enough that the scan is never what the machine is busy with.
const cockpitRefresh = 10 * time.Second

type cockpitTickMsg time.Time

func cockpitTickCmd() tea.Cmd {
	return tea.Tick(cockpitRefresh, func(now time.Time) tea.Msg { return cockpitTickMsg(now) })
}

// cockpitLoadedMsg carries one refresh of the read-only panels. It names the
// profile its per-account halves describe: moving the cursor starts a new load
// without cancelling the one in flight, and a slow read landing after a fast one
// would otherwise file one account's sessions under another's name.
type cockpitLoadedMsg struct {
	profile  string
	live     []profileInstance
	recent   []recordedSession
	activity activity
	now      time.Time
}

// loadCockpitCmd reads the panels that describe the world rather than the
// config: what is running everywhere, and what the selected account has been
// doing. All of it touches the filesystem, so none of it happens inline.
func (m tuiModel) loadCockpitCmd() tea.Cmd {
	profiles := append([]Profile(nil), m.profiles...)
	selected, hasSelection := m.selectedProfile()
	return func() tea.Msg {
		now := time.Now()
		msg := cockpitLoadedMsg{profile: selected.Name, now: now}
		msg.live = describeLiveInstances(profiles, allProfileInstances(profiles))
		if hasSelection {
			msg.recent = recentSessions(selected, recentSessionLimit)
			msg.activity = profileActivity(selected, now)
		}
		return msg
	}
}

func checkUpdateCmd(force bool) tea.Cmd {
	return func() tea.Msg {
		return updateCheckedMsg{status: checkForUpdate(force, time.Now()), forced: force}
	}
}

func loadUsageCmd(profiles []Profile) tea.Cmd {
	profiles = append([]Profile(nil), profiles...)
	return func() tea.Msg {
		return usageLoadedMsg(loadProfileUsage(profiles, time.Now()))
	}
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.running {
			return m, nil
		}
		switch m.mode {
		case tuiList:
			return m.updateList(msg)
		case tuiForm:
			return m.updateForm(msg)
		case tuiFolder:
			return m.updateFolder(msg)
		case tuiConfirmDelete:
			return m.updateDelete(msg)
		case tuiConfirmKill:
			return m.updateKill(msg)
		case tuiHijack:
			return m.updateHijack(msg)
		case tuiRecent:
			return m.updateRecent(msg)
		case tuiHandoff:
			return m.updateHandoff(msg)
		case tuiHandoffTo:
			return m.updateHandoffTo(msg)
		case tuiHandoffBrief:
			return m.updateHandoffBrief(msg)
		case tuiParams:
			return m.updateParams(msg)
		case tuiClone:
			return m.updateClone(msg)
		case tuiShare:
			return m.updateShare(msg)
		case tuiConfirmInstall:
			return m.updateInstall(msg)
		case tuiConfirmSelfUpdate:
			return m.updateSelfUpdate(msg)
		case tuiPalette:
			return m.updatePalette(msg)
		}
	case processFinishedMsg:
		m.running = false
		mergeNote := ""
		if m.unlock != nil {
			mergeNote = m.unlock()
			m.unlock = nil
		}
		if msg.err != nil {
			m.setStatus(statusErr, "process exited: "+msg.err.Error()+statusSuffix(mergeNote))
		} else if mergeNote != "" {
			m.setStatus(statusOK, mergeNote)
		} else {
			m.setStatus(statusOK, "process finished")
		}
		return m, tea.Batch(loadUsageCmd(m.profiles), m.loadCockpitCmd())
	case cockpitTickMsg:
		return m, tea.Batch(m.loadCockpitCmd(), cockpitTickCmd())
	case cockpitLoadedMsg:
		// What is running is true of the whole machine, so it lands either way.
		m.live, m.now, m.loaded = msg.live, msg.now, true
		// Both pickers choose from this list by index, so a refresh landing
		// under one would move the row the cursor is on. The next tick takes
		// the panel once the picker is closed.
		if m.mode == tuiRecent || m.mode == tuiHandoff {
			return m, nil
		}
		m.lineage = handedOff(readLineage())
		if profile, ok := m.selectedProfile(); ok && profile.Name == msg.profile {
			m.recent, m.activity = msg.recent, msg.activity
		}
	case usageLoadedMsg:
		selected, had := m.selectedProfile()
		m.usage = msg
		if had {
			m.followSelection(selected.Name)
		}
	case updateCheckedMsg:
		m.update = msg.status
		if msg.forced {
			kind := statusOK
			if !msg.status.Known {
				kind = statusErr
			}
			m.setStatus(kind, msg.status.message())
		}
	case sessionPreviewMsg:
		if m.previews == nil {
			m.previews = make(map[string]sessionPreview)
		}
		m.previews[msg.preview.session] = msg.preview
		if m.mode != tuiRecent && m.mode != tuiHandoff {
			return m, nil
		}
		// The cursor may have moved on while the transcript was being read, and
		// a preview shown beside the wrong row is worse than none: it describes
		// a session that is not the one about to be resumed.
		visible := m.visibleRecent()
		if m.record < 0 || m.record >= len(visible) || visible[m.record].session.id != msg.preview.session {
			return m, nil
		}
		if profile, ok := m.profileForRecord(visible[m.record]); ok && profile.Name == msg.profile {
			m.preview = msg.preview
		}
	case recentAllLoadedMsg:
		m.recentAll, m.recentAllLoaded = msg.records, true
		if m.mode != tuiRecent && m.mode != tuiHandoff {
			return m, nil
		}
		// The cursor was indexing the selected profile's list; the union that
		// just landed replaces it, so start from the top rather than a row
		// number that means something else now.
		m.record = 0
		m.previews = nil
		m.clearStatus()
		return m, m.selectPreview()
	case instancesDescribedMsg:
		m.describing = false
		if m.mode != tuiHijack && m.mode != tuiConfirmKill {
			return m, nil
		}
		if profile, ok := m.selectedProfile(); ok && profile.Name == msg.profile && len(msg.instances) == len(m.instances) {
			m.instances = msg.instances
		}
	}
	return m, nil
}

func (m tuiModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.searching {
		return m.updateSearch(msg)
	}
	profile, hasSelection := m.selectedProfile()
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			return m, m.loadCockpitCmd()
		}
	case "down", "j":
		if m.cursor < len(m.visibleProfiles())-1 {
			m.cursor++
			return m, m.loadCockpitCmd()
		}
	case "/":
		m.searching = true
		m.clearStatus()
	case "?", " ":
		m.openPalette()
	case "a":
		m.mode = tuiForm
		m.form = profileForm{name: "", provider: "codex", command: "codex", isNew: true}
		m.clearStatus()
	case "c":
		m.mode = tuiFolder
		m.folderPath = m.workingDir
		m.clearStatus()
	case "e":
		if hasSelection {
			if profileIsRunning(profile) {
				m.setStatus(statusErr, "cannot edit a running profile")
				return m, nil
			}
			m.mode = tuiForm
			m.form = profileForm{
				name:        profile.Name,
				provider:    profile.Provider,
				command:     profile.Command,
				defaultArgs: formatArguments(profile.DefaultArgs),
				notes:       profile.Notes,
				original:    profile.Name,
			}
			m.clearStatus()
		}
	case "r":
		// The old figures stay up until the new ones land. The board is ranked
		// by them, and blanking them would reshuffle every row twice.
		return m, tea.Batch(loadUsageCmd(m.profiles), checkUpdateCmd(true), m.loadCockpitCmd())
	case "x":
		if hasSelection {
			m.mode = tuiConfirmDelete
			m.clearStatus()
		}
	case "K":
		if hasSelection {
			return m, m.openInstances(tuiConfirmKill)
		}
	case "h":
		if hasSelection {
			return m, m.openInstances(tuiHijack)
		}
	case "R":
		if hasSelection {
			return m, m.openRecent()
		}
	case "H":
		if hasSelection {
			return m, m.openHandoff()
		}
	case "A":
		m.toggleAutoSwap()
	case "C":
		if hasSelection {
			m.mode = tuiClone
			m.clone = ""
			m.clearStatus()
		}
	case "m":
		if hasSelection {
			m.openShare(shareMCP)
		}
	case "s":
		if hasSelection {
			m.openShare(shareSkills)
		}
	case "p":
		if hasSelection {
			m.openParams()
		}
	case "l":
		if hasSelection {
			return m, m.execProfile(profile, loginArgs(profile.Provider), true)
		}
	case "enter":
		if hasSelection {
			return m, m.execProfile(profile, profileRunArgs(profile, nil), false)
		}
	case "u":
		if hasSelection {
			return m, m.execUpdate(profile)
		}
	case "i":
		if hasSelection {
			install, err := providerInstaller(profile.Provider)
			if err != nil {
				m.setStatus(statusErr, err.Error())
				return m, nil
			}
			m.install = install
			m.mode = tuiConfirmInstall
			m.clearStatus()
		}
	case "U":
		source, err := previewUpdateDir()
		if err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.source = source
		m.mode = tuiConfirmSelfUpdate
		m.clearStatus()
	}
	return m, nil
}

// updateInstall confirms running a vendor's install script. It is a confirmation
// rather than a plain keypress because the thing being agreed to is fetching and
// executing code from a URL, and the box is where that URL is shown.
func (m tuiModel) updateInstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.mode = tuiList
		return m, m.execInstall()
	case "n", "N", "esc", "q":
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

func (m tuiModel) updateSelfUpdate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.mode = tuiList
		return m, m.execSelfUpdate()
	case "n", "N", "esc", "q":
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// updateSearch narrows the accounts column as the query is typed. Enter keeps
// the filter and hands the keys back to the list, so a search is a way to reach
// one account among many rather than a mode to be dismissed before acting.
func (m tuiModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.searching, m.filter = false, ""
		m.clampCursor()
		return m, m.loadCockpitCmd()
	case "enter":
		m.searching = false
		return m, nil
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(m.visibleProfiles())-1 {
			m.cursor++
		}
		return m, nil
	case "backspace", "ctrl+h":
		if runes := []rune(m.filter); len(runes) > 0 {
			m.filter = string(runes[:len(runes)-1])
		}
	case "ctrl+u":
		m.filter = ""
	default:
		if msg.Type != tea.KeyRunes {
			return m, nil
		}
		m.filter += string(msg.Runes)
	}
	m.clampCursor()
	return m, m.loadCockpitCmd()
}

// openInstances shows the running instances of the selected profile and starts
// resolving what each one is working on.
func (m *tuiModel) openInstances(mode tuiMode) tea.Cmd {
	profile, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	instances, err := activeProfileInstances(profile)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	if len(instances) == 0 {
		m.setStatus(statusErr, profile.Name+" is not running")
		return nil
	}
	m.instances = instances
	m.instance = 0
	m.mode = mode
	m.describing = true
	m.clearStatus()
	return describeInstancesCmd(profile, instances)
}

func describeInstancesCmd(profile Profile, instances []profileInstance) tea.Cmd {
	instances = append([]profileInstance(nil), instances...)
	return func() tea.Msg {
		return instancesDescribedMsg{profile: profile.Name, instances: describeInstances(profile, instances)}
	}
}

// updateHijack reopens the conversation a running instance has open, in the
// folder that instance was launched from. The original process is left alone.
func (m tuiModel) updateHijack(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.instance > 0 {
			m.instance--
		}
	case "down", "j":
		if m.instance < len(m.instances)-1 {
			m.instance++
		}
	case "enter":
		if len(m.instances) == 0 || m.instance >= len(m.instances) {
			m.mode = tuiList
			return m, nil
		}
		instance := m.instances[m.instance]
		profile, ok := m.selectedProfile()
		if !ok {
			m.mode = tuiList
			return m, nil
		}
		args, err := reopenArgs(profile.Provider, instance.session)
		if err != nil {
			m.setStatus(statusErr, err.Error())
			m.mode = tuiList
			return m, nil
		}
		folder := instance.folder
		if folder == "" {
			folder = m.workingDir
		}
		m.instances = nil
		m.mode = tuiList
		return m, m.execProfileIn(profile, profileRunArgs(profile, args), false, folder)
	case "esc", "q", "n", "N":
		m.instances = nil
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// openRecent offers the conversations the RECENT SESSIONS panel is already
// showing. It is not the same offer as the provider's own resume flow: that one
// only ever sees the folder it was started in, while the panel has read every
// folder this account has worked in. A provider whose transcripts are not read,
// and an account with nothing recorded yet, fall back to the provider's flow
// rather than to an empty picker.
//
// The list is the selected profile's own unless the picker has been switched to
// every account, and each row is reopened under the profile that recorded it —
// which is the only way it can work: a session id lives inside the profile that
// recorded it, so no account can resume another's conversation.
func (m *tuiModel) openRecent() tea.Cmd {
	profile, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	m.record = 0
	m.previews = nil
	m.recentFilter, m.recentSearching = "", false
	m.clearStatus()
	if m.pickerAll {
		m.mode = tuiRecent
		if !m.recentAllLoaded {
			m.setStatus(statusNone, "reading every profile…")
			return loadRecentAllCmd(m.profiles)
		}
		return m.selectPreview()
	}
	if len(m.recent) > 0 {
		m.mode = tuiRecent
		return m.selectPreview()
	}
	args, err := resumeArgs(profile.Provider)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	return m.execProfile(profile, profileRunArgs(profile, args), false)
}

// updateRecent resumes a conversation read back off disk. The id and the folder
// are both needed: the id names the conversation, and the folder is where the
// provider looks for it, so the launch moves to the folder the session ran in
// rather than to whatever folder the launcher is pointed at. The account is the
// one that recorded the row, which is what makes the picker work when it is
// listing several profiles at once.
func (m tuiModel) updateRecent(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.recentSearching {
		return m.updateRecentSearch(msg)
	}
	visible := m.visibleRecent()
	switch msg.String() {
	case "up", "k":
		if m.record > 0 {
			m.record--
			return m, m.selectPreview()
		}
	case "down", "j":
		if m.record < len(visible)-1 {
			m.record++
			return m, m.selectPreview()
		}
	case "/":
		m.recentSearching = true
		m.clearStatus()
	case "a":
		return m.toggleAllProfiles()
	case "H":
		if m.record >= 0 && m.record < len(visible) {
			return m.leaveWith(visible[m.record])
		}
	case "enter":
		if m.record < 0 || m.record >= len(visible) {
			m.mode = tuiList
			return m, nil
		}
		record := visible[m.record]
		profile, ok := m.profileForRecord(record)
		if !ok {
			m.mode = tuiList
			return m, nil
		}
		folder, err := recordedFolder(record, m.workingDir)
		if err != nil {
			// The picker stays open: the other rows are still resumable, and
			// this one is only unreachable because its folder moved.
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		// A fresh launch seeds from the profile store, so a session that only
		// exists in a running instance's private copy would resume into
		// nothing. The picker shows it (the live panel reads the union), but
		// reopening waits for the merge.
		if profile.Provider == "opencode" && !opencodeSessionInProfileDB(profile, record.session.id) {
			m.setStatus(statusErr, "that session is still in a running instance and has not merged yet; stop that instance first")
			return m, nil
		}
		args, err := reopenArgs(profile.Provider, record.session)
		if err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.mode = tuiList
		return m, m.execProfileIn(profile, profileRunArgs(profile, args), false, folder)
	case "esc", "q":
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// updateRecentSearch narrows either picker's list as the query is typed. Enter
// keeps the filter and hands the keys back to the list, matching the profile
// list's own search, so a filter is a way to reach one session among many
// rather than a mode to dismiss before acting.
func (m tuiModel) updateRecentSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.recentSearching, m.recentFilter = false, ""
		m.clampRecord()
		return m, m.selectPreview()
	case "enter":
		m.recentSearching = false
		return m, nil
	case "up", "ctrl+p":
		if m.record > 0 {
			m.record--
			return m, m.selectPreview()
		}
	case "down", "ctrl+n":
		if m.record < len(m.visibleRecent())-1 {
			m.record++
			return m, m.selectPreview()
		}
	case "backspace", "ctrl+h":
		if runes := []rune(m.recentFilter); len(runes) > 0 {
			m.recentFilter = string(runes[:len(runes)-1])
		}
	case "ctrl+u":
		m.recentFilter = ""
	default:
		if msg.Type != tea.KeyRunes {
			return m, nil
		}
		m.recentFilter += string(msg.Runes)
	}
	m.clampRecord()
	return m, m.selectPreview()
}

// toggleAllProfiles switches a picker between the selected account's sessions
// and every account's. The union is read off the keypress, because it opens
// every other profile's transcripts; the picker stays up and says so until it
// lands.
func (m tuiModel) toggleAllProfiles() (tea.Model, tea.Cmd) {
	m.pickerAll = !m.pickerAll
	m.record = 0
	m.previews = nil
	m.clearStatus()
	if m.pickerAll && !m.recentAllLoaded {
		m.setStatus(statusNone, "reading every profile…")
		return m, loadRecentAllCmd(m.profiles)
	}
	return m, m.selectPreview()
}

// loadRecentAllCmd reads every profile's recent sessions as one union for a
// picker that has been switched to all profiles.
func loadRecentAllCmd(profiles []Profile) tea.Cmd {
	profiles = append([]Profile(nil), profiles...)
	return func() tea.Msg {
		return recentAllLoadedMsg{records: allRecentSessions(profiles, allSessionLimit)}
	}
}

// recordedFolder is where a recorded conversation has to be reopened. A
// transcript outlives the directory it was written in, and resuming by id from
// anywhere else does not reach the same conversation — the provider looks for
// the id under the current folder and finds nothing — so a folder that has
// since moved is reported rather than quietly swapped for the launch folder.
func recordedFolder(record recordedSession, fallback string) (string, error) {
	folder := record.folder
	if folder == "" {
		folder = fallback
	}
	if folder == "" {
		return "", errors.New("this session records no folder to resume in")
	}
	info, err := os.Stat(folder)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%s is gone; that session cannot be resumed", shortenHome(folder))
	}
	if err != nil {
		// Anything else — a permission wall, a dead mount — is reported as
		// itself. Calling it gone would send someone looking for a directory
		// that is still there.
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", shortenHome(folder))
	}
	return folder, nil
}

// handoffDraft is one pass being set up. It exists across three modals, so it
// is built in one place and thrown away in one place.
type handoffDraft struct {
	source       recordedSession
	destinations []Profile
	target       int
	path         string
	// closing is how the outgoing conversation ended, and earlier says it ran on
	// before that. They are read once, when the brief is written, because the
	// confirmation screen is drawn on every keypress and the transcript behind
	// them is the large file this whole feature exists to avoid re-reading.
	closing []handoffMessage
	earlier bool
	// provider names who the model half of those turns was, so the screen can
	// label it the way the picker's preview does.
	provider string
	// prompts, notes and git are what the brief was written from, kept so the
	// brief step can show what the file says without reading it back; size is
	// how large the file came out.
	prompts []string
	notes   []string
	git     gitState
	size    int
}

// openHandoff starts a pass by asking which session is leaving. That question
// is never skipped, not even with auto-swap on: which account to spend is a
// preference, but which piece of work is moving is a fact only the user has.
func (m *tuiModel) openHandoff() tea.Cmd {
	profile, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	if !m.pickerAll && len(m.recent) == 0 {
		m.setStatus(statusErr, "nothing recorded for "+profile.Name+" to hand over")
		return nil
	}
	m.handoff = handoffDraft{}
	m.record = 0
	m.mode = tuiHandoff
	m.previews = nil
	m.recentFilter, m.recentSearching = "", false
	m.clearStatus()
	if m.pickerAll && !m.recentAllLoaded {
		m.setStatus(statusNone, "reading every profile…")
		return loadRecentAllCmd(m.profiles)
	}
	return m.selectPreview()
}

// selectPreview points the pane at the row under the cursor: at what has
// already been read when the session is in the cache, and otherwise at a read
// started off the keypress. Nothing about a preview is worth making a cursor
// move wait for the disk.
func (m *tuiModel) selectPreview() tea.Cmd {
	visible := m.visibleRecent()
	if m.record < 0 || m.record >= len(visible) {
		m.preview = sessionPreview{}
		return nil
	}
	record := visible[m.record]
	if cached, read := m.previews[record.session.id]; read {
		m.preview = cached
		return nil
	}
	profile, ok := m.profileForRecord(record)
	if !ok {
		m.preview = sessionPreview{}
		return nil
	}
	m.preview = sessionPreview{}
	return previewSessionCmd(profile, record)
}

func previewSessionCmd(profile Profile, record recordedSession) tea.Cmd {
	return func() tea.Msg {
		return sessionPreviewMsg{profile: profile.Name, preview: readSessionPreview(profile, record)}
	}
}

// updateHandoff picks the session that is leaving, then either asks where it
// should go or, with auto-swap on, sends it to whichever account has the most
// quota left. The session's own account is the one the brief is built from, so
// the picker can be listing several profiles at once.
func (m tuiModel) updateHandoff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.recentSearching {
		return m.updateRecentSearch(msg)
	}
	visible := m.visibleRecent()
	switch msg.String() {
	case "up", "k":
		if m.record > 0 {
			m.record--
			return m, m.selectPreview()
		}
	case "down", "j":
		if m.record < len(visible)-1 {
			m.record++
			return m, m.selectPreview()
		}
	case "/":
		m.recentSearching = true
		m.clearStatus()
	case "a":
		return m.toggleAllProfiles()
	case "A":
		m.toggleAutoSwap()
	case "enter":
		if m.record < 0 || m.record >= len(visible) {
			m.mode = tuiList
			return m, nil
		}
		return m.leaveWith(visible[m.record])
	case "esc", "q":
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// leaveWith settles the first step of a handoff on one conversation and moves
// to the second, or, with auto-swap on, straight to the brief. The resume
// picker reaches it too: the row you would have resumed is the row you are
// handing over.
func (m tuiModel) leaveWith(record recordedSession) (tea.Model, tea.Cmd) {
	source, ok := m.profileForRecord(record)
	if !ok {
		m.mode = tuiList
		return m, nil
	}
	destinations := handoffDestinations(m.profiles, source, m.usage)
	if len(destinations) == 0 {
		m.setStatus(statusErr, "no other profile can be opened with a brief")
		m.mode = tuiList
		return m, nil
	}
	m.handoff = handoffDraft{source: record, destinations: destinations}
	if m.autoSwap {
		return m.startHandoff()
	}
	m.mode = tuiHandoffTo
	m.clearStatus()
	return m, nil
}

func (m tuiModel) updateHandoffTo(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "backspace":
		// Back to the first step, on the same row: the choice being revisited
		// is the destination's, not which conversation is leaving.
		m.handoff = handoffDraft{}
		m.mode = tuiHandoff
		m.clearStatus()
		return m, m.selectPreview()
	case "up", "k":
		if m.handoff.target > 0 {
			m.handoff.target--
		}
	case "down", "j":
		if m.handoff.target < len(m.handoff.destinations)-1 {
			m.handoff.target++
		}
	case "a", "A":
		m.toggleAutoSwap()
	case "enter":
		return m.startHandoff()
	case "esc", "q":
		m.handoff = handoffDraft{}
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// startHandoff writes the brief. It stops before launching even when auto-swap
// skipped the destination question, because writing a file and starting a
// process are different promises: the first can be undone by ignoring it, and
// with auto-swap on this is the frame where the user sees where the work went.
func (m tuiModel) startHandoff() (tea.Model, tea.Cmd) {
	profile, ok := m.profileForRecord(m.handoff.source)
	if !ok {
		m.mode = tuiList
		return m, nil
	}
	brief, err := buildBrief(profile, m.handoff.source)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		m.mode = tuiList
		return m, nil
	}
	folder, err := recordedFolder(m.handoff.source, m.workingDir)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		m.mode = tuiList
		return m, nil
	}
	state := collectGitState(folder)
	body := renderBrief(brief, state, m.clock())
	path, err := writeBriefFile(brief, body)
	if err != nil {
		m.setStatus(statusErr, "could not write the brief: "+err.Error())
		m.mode = tuiList
		return m, nil
	}
	m.handoff.path = path
	m.handoff.closing, m.handoff.earlier = brief.closing, brief.earlier
	m.handoff.provider = profile.Provider
	m.handoff.prompts, m.handoff.notes = brief.prompts, brief.notes
	m.handoff.git, m.handoff.size = state, len(body)
	m.mode = tuiHandoffBrief
	m.clearStatus()
	return m, nil
}

func (m tuiModel) updateHandoffBrief(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "backspace":
		// The brief stays written; going back is for choosing another account,
		// and writing it again for that one costs one more read.
		m.mode = tuiHandoffTo
		m.clearStatus()
	case "e":
		return m, m.execEditor(m.handoff.path)
	case "enter":
		return m.launchHandoff()
	case "esc", "q", "n", "N":
		// The brief stays on disk. It cost a full read to produce, and the most
		// likely reason for backing out here is to open it somewhere else.
		m.setStatus(statusOK, "brief kept at "+shortenHome(m.handoff.path))
		m.handoff = handoffDraft{}
		m.mode = tuiList
	}
	return m, nil
}

func (m tuiModel) launchHandoff() (tea.Model, tea.Cmd) {
	source, ok := m.profileForRecord(m.handoff.source)
	if !ok || m.handoff.target >= len(m.handoff.destinations) {
		m.mode = tuiList
		return m, nil
	}
	target := m.handoff.destinations[m.handoff.target]
	args, err := promptArgs(target.Provider, handoffPrompt(m.handoff.path))
	if err != nil {
		m.setStatus(statusErr, err.Error())
		m.mode = tuiList
		return m, nil
	}
	folder, err := recordedFolder(m.handoff.source, m.workingDir)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		m.mode = tuiList
		return m, nil
	}
	link := lineageLink{
		When:            m.clock(),
		SourceProfile:   source.Name,
		SourceProvider:  source.Provider,
		SourceSessionID: m.handoff.source.session.id,
		SourceTitle:     m.handoff.source.session.title,
		TargetProfile:   target.Name,
		TargetProvider:  target.Provider,
		Folder:          folder,
		Brief:           m.handoff.path,
	}
	if err := appendLineage(link); err != nil {
		// Say so, but hand the work over anyway: the pass is the point and the
		// record is the note about it.
		m.log = append(m.log, logEntry{kind: statusErr, text: "lineage not recorded: " + err.Error()})
	}
	m.lineage = handedOff(readLineage())
	m.handoff = handoffDraft{}
	m.mode = tuiList
	return m, m.execProfileIn(target, profileRunArgs(target, args), false, folder)
}

// execEditor opens the brief in the user's editor. The handoff is the one place
// the tool writes prose on the user's behalf, so it is also the one place that
// has to let them disagree with it before it goes out.
func (m *tuiModel) execEditor(path string) tea.Cmd {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		m.setStatus(statusErr, "set $EDITOR to edit the brief; it is at "+shortenHome(path))
		return nil
	}
	cmd := exec.Command(editor, path)
	cmd.Dir = filepath.Dir(path)
	m.running = true
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return processFinishedMsg{err: err} })
}

// toggleAutoSwap flips the setting and writes it back. It is persisted rather
// than kept for the session because it is a statement about how the user wants
// to be treated, and having to re-assert it every launch would make the safe
// default feel like nagging.
func (m *tuiModel) toggleAutoSwap() {
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return
	}
	cfg.Settings.AutoSwap = !m.autoSwap
	if err := saveConfig(m.configPath, cfg); err != nil {
		m.setStatus(statusErr, err.Error())
		return
	}
	m.autoSwap = cfg.Settings.AutoSwap
	if m.autoSwap {
		m.setStatus(statusOK, "auto-swap on — H sends to the account with the most quota left, unasked")
		return
	}
	m.setStatus(statusOK, "auto-swap off — H asks where the work should go")
}

// openParams opens the argument prompt with the history as it is on disk now,
// so a set pinned from another TUI since this one started is already there.
func (m *tuiModel) openParams() {
	m.mode = tuiParams
	m.params, m.argumentDraft, m.argumentRow = "", "", -1
	m.clearStatus()
	history, err := loadArgumentHistory()
	if err != nil {
		m.setStatus(statusErr, err.Error())
	}
	m.arguments = history
}

func (m tuiModel) updateParams(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = tuiList
		m.clearStatus()
		return m, nil
	case "enter":
		args, err := parseArguments(m.params)
		if err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		profile, ok := m.selectedProfile()
		if !ok {
			m.mode = tuiList
			return m, nil
		}
		m.mode = tuiList
		cmd := m.execProfile(profile, profileRunArgs(profile, args), false)
		if cmd != nil && len(args) > 0 {
			set := argumentSet{Args: args, Profile: profile.Name, Provider: profile.Provider, Used: m.clock()}
			if _, err := updateArgumentHistory(func(history argumentHistory) argumentHistory {
				return history.record(set)
			}); err != nil {
				// The launch goes ahead: remembering the arguments is a
				// convenience, and the session is what was asked for.
				m.log = append(m.log, logEntry{kind: statusErr, text: "arguments not remembered: " + err.Error()})
			}
		}
		return m, cmd
	case "down":
		if m.argumentRow < len(m.arguments.entries())-1 {
			if m.argumentRow < 0 {
				m.argumentDraft = m.params
			}
			m.pickArgumentRow(m.argumentRow + 1)
		}
		return m, nil
	case "up":
		if m.argumentRow > 0 {
			m.pickArgumentRow(m.argumentRow - 1)
		} else if m.argumentRow == 0 {
			m.argumentRow, m.params = -1, m.argumentDraft
		}
		return m, nil
	case "ctrl+p":
		m.toggleArgumentPin()
		return m, nil
	case "backspace", "ctrl+h":
		if runes := []rune(m.params); len(runes) > 0 {
			m.params = string(runes[:len(runes)-1])
		}
		m.argumentRow = -1
		return m, nil
	case "ctrl+u":
		m.params = ""
		m.argumentRow = -1
		return m, nil
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		// Editing a recalled set makes it a new one, so the row it came from is
		// no longer the thing a pin or the highlight would be about.
		m.params += string(msg.Runes)
		m.argumentRow = -1
	}
	return m, nil
}

// pickArgumentRow moves the highlight and recalls that row into the field,
// where it can be run as it is or edited first.
func (m *tuiModel) pickArgumentRow(row int) {
	entries := m.arguments.entries()
	if row < 0 || row >= len(entries) {
		return
	}
	m.argumentRow = row
	m.params = formatArguments(entries[row].Args)
}

// toggleArgumentPin pins or unpins the highlighted row, or, with no row
// highlighted, what has been typed. The highlight follows the set to where it
// lands, so pressing the key twice is a no-op rather than a pin on whatever
// slid into the row it left.
func (m *tuiModel) toggleArgumentPin() {
	set := argumentSet{}
	if entries := m.arguments.entries(); m.argumentRow >= 0 && m.argumentRow < len(entries) {
		set = entries[m.argumentRow]
	} else {
		args, err := parseArguments(m.params)
		if err != nil {
			m.setStatus(statusErr, err.Error())
			return
		}
		if len(args) == 0 {
			m.setStatus(statusErr, "type some arguments, or pick a row, to pin")
			return
		}
		set.Args = args
	}
	history, err := updateArgumentHistory(func(history argumentHistory) argumentHistory {
		return history.togglePin(set)
	})
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return
	}
	m.arguments = history
	pinned := history.pinnedIndex(set.Args)
	label := formatArguments(set.Args)
	if pinned >= 0 {
		m.setStatus(statusOK, "pinned "+label)
	} else {
		m.setStatus(statusOK, "unpinned "+label)
	}
	if m.argumentRow < 0 {
		return
	}
	switch recent := history.recentIndex(set.Args); {
	case pinned >= 0:
		m.argumentRow = pinned
	case recent >= 0:
		m.argumentRow = len(history.Pinned) + recent
	default:
		m.argumentRow = -1
	}
}

func (m tuiModel) updateKill(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.instance > 0 {
			m.instance--
		}
	case "down", "j":
		if m.instance < len(m.instances)-1 {
			m.instance++
		}
	case "enter":
		if len(m.instances) == 0 || m.instance >= len(m.instances) {
			m.mode = tuiList
			return m, nil
		}
		instance := m.instances[m.instance]
		note, err := terminateProfileLock(instance.lockDir)
		if err != nil {
			var staleErr staleProfileLockError
			if errors.As(err, &staleErr) {
				m.setStatus(statusOK, staleErr.Error()+statusSuffix(note))
			} else {
				m.setStatus(statusErr, "stop failed: "+err.Error())
			}
		} else {
			name, _ := m.selectedProfile()
			m.setStatus(statusOK, fmt.Sprintf("stopped %s instance %d (PID %d)", name.Name, m.instance+1, instance.pid)+statusSuffix(note))
		}
		m.instances = nil
		m.mode = tuiList
	case "a", "y", "Y":
		profile, ok := m.selectedProfile()
		if !ok {
			m.mode = tuiList
			return m, nil
		}
		if err := terminateProfile(profile); err != nil {
			var staleErr staleProfileLockError
			if errors.As(err, &staleErr) {
				m.setStatus(statusOK, staleErr.Error())
			} else {
				m.setStatus(statusErr, "stop failed: "+err.Error())
			}
		} else {
			m.setStatus(statusOK, "stopped "+profile.Name)
		}
		m.instances = nil
		m.mode = tuiList
	case "n", "N", "esc", "q":
		m.instances = nil
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

func (m tuiModel) updateFolder(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = tuiList
		m.clearStatus()
		return m, nil
	case "enter":
		workingDir, err := resolveWorkingDir(m.folderPath, m.workingDir)
		if err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.workingDir = workingDir
		m.mode = tuiList
		m.setStatus(statusOK, "launch folder set to "+workingDir)
		return m, nil
	case "backspace", "ctrl+h":
		if runes := []rune(m.folderPath); len(runes) > 0 {
			m.folderPath = string(runes[:len(runes)-1])
		}
		return m, nil
	case "ctrl+u":
		m.folderPath = ""
		return m, nil
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		m.folderPath += string(msg.Runes)
	}
	return m, nil
}

func (m tuiModel) updateDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		profile, ok := m.selectedProfile()
		if !ok {
			m.mode = tuiList
			return m, nil
		}
		if profileIsRunning(profile) {
			m.setStatus(statusErr, "cannot delete a running profile")
			m.mode = tuiList
			return m, nil
		}
		root, err := profileRoot()
		if err == nil {
			err = os.RemoveAll(filepath.Join(root, profile.Name))
		}
		if err != nil {
			m.setStatus(statusErr, "delete failed: "+err.Error())
		} else {
			cfg, loadErr := loadConfig(m.configPath)
			if loadErr == nil {
				filtered := cfg.Profiles[:0]
				for _, candidate := range cfg.Profiles {
					if candidate.Name != profile.Name {
						filtered = append(filtered, candidate)
					}
				}
				cfg.Profiles = filtered
				err = saveConfig(m.configPath, cfg)
			}
			if err != nil {
				m.setStatus(statusErr, "delete failed: "+err.Error())
			} else {
				m.profiles = sortedProfiles(cfg.Profiles)
				m.clampCursor()
				m.setStatus(statusOK, "deleted "+profile.Name)
			}
		}
		m.mode = tuiList
	case "n", "N", "esc", "q":
		m.mode = tuiList
		m.clearStatus()
	}
	return m, nil
}

// formFields is how many fields the editor has: name, provider, command,
// default arguments, and note.
const formFields = 5

// formProviderField is the one field that is chosen rather than typed.
const formProviderField = 1

// knownProviders are the providers the editor offers as chips, in the order it
// draws them.
var knownProviders = []string{"codex", "claude", "antigravity", "opencode", "deepseek"}

// updateForm edits a profile. Enter saves from any field — the editor shows
// what the launch will be as it is typed, so there is nothing left to confirm
// by walking to the last field — and tab moves between them.
func (m tuiModel) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = tuiList
		m.clearStatus()
		return m, nil
	case "enter":
		if err := m.saveForm(); err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.mode = tuiList
		return m, tea.Batch(loadUsageCmd(m.profiles), m.loadCockpitCmd())
	case "tab", "down":
		m.form.field = (m.form.field + 1) % formFields
		return m, nil
	case "shift+tab", "up":
		m.form.field = (m.form.field + formFields - 1) % formFields
		return m, nil
	}
	if m.form.field == formProviderField {
		switch msg.String() {
		case "left", "h":
			m.cycleProvider(-1)
		case "right", "l", " ":
			m.cycleProvider(1)
		}
		return m, nil
	}
	switch msg.String() {
	case "backspace", "ctrl+h":
		value := m.formValue()
		if runes := []rune(value); len(runes) > 0 {
			m.setFormValue(string(runes[:len(runes)-1]))
		}
		return m, nil
	case "ctrl+u":
		m.setFormValue("")
		return m, nil
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		m.setFormValue(m.formValue() + string(msg.Runes))
	}
	return m, nil
}

// cycleProvider moves the provider chip. A command still set to the old
// provider's default follows it, since a claude profile launching codex is
// never what switching the chip meant; a command typed by hand is left alone.
func (m *tuiModel) cycleProvider(step int) {
	index := -1
	for position, provider := range knownProviders {
		if provider == m.form.provider {
			index = position
		}
	}
	next := knownProviders[(index+step+len(knownProviders))%len(knownProviders)]
	if index < 0 && step < 0 {
		next = knownProviders[len(knownProviders)-1]
	}
	if m.form.command == "" || m.form.command == defaultCommand(m.form.provider) {
		m.form.command = defaultCommand(next)
	}
	m.form.provider = next
}

func (m *tuiModel) formValue() string {
	switch m.form.field {
	case 0:
		return m.form.name
	case 1:
		return m.form.provider
	case 2:
		return m.form.command
	case 3:
		return m.form.defaultArgs
	default:
		return m.form.notes
	}
}

func (m *tuiModel) setFormValue(value string) {
	switch m.form.field {
	case 0:
		m.form.name = value
	case 1:
		m.form.provider = value
	case 2:
		m.form.command = value
	case 3:
		m.form.defaultArgs = value
	default:
		m.form.notes = value
	}
}

func (m *tuiModel) saveForm() error {
	if !validName(m.form.name) {
		return errors.New("name must use letters, numbers, dots, dashes, or underscores")
	}
	if m.form.provider == "" || m.form.command == "" {
		return errors.New("provider and command are required")
	}
	defaultArgs, err := parseArguments(m.form.defaultArgs)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		return err
	}
	if m.form.isNew {
		if err := checkNameAvailable(cfg, m.form.name); err != nil {
			return err
		}
		root, err := profileRoot()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(root, m.form.name), 0700); err != nil {
			return err
		}
		cfg.Profiles = append(cfg.Profiles, Profile{
			Name:        m.form.name,
			Provider:    m.form.provider,
			Command:     m.form.command,
			DefaultArgs: defaultArgs,
			Notes:       strings.TrimSpace(m.form.notes),
		})
	} else {
		original, err := findProfile(cfg, m.form.original)
		if err != nil {
			return err
		}
		if profileIsRunning(original) {
			return errors.New("cannot edit a running profile")
		}
		if m.form.name != m.form.original {
			if err := checkNameAvailable(cfg, m.form.name); err != nil {
				return err
			}
			if apps := appsReferencing(cfg, m.form.original); len(apps) > 0 {
				return fmt.Errorf("cannot rename profile %q: used by app(s) %v", m.form.original, apps)
			}
			root, err := profileRoot()
			if err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(root, m.form.original), filepath.Join(root, m.form.name)); err != nil {
				return err
			}
		}
		for index := range cfg.Profiles {
			if cfg.Profiles[index].Name == m.form.original {
				cfg.Profiles[index] = Profile{
					Name:        m.form.name,
					Provider:    m.form.provider,
					Command:     m.form.command,
					DefaultArgs: defaultArgs,
					Notes:       strings.TrimSpace(m.form.notes),
				}
				break
			}
		}
	}
	if err := saveConfig(m.configPath, cfg); err != nil {
		return err
	}
	m.profiles = sortedProfiles(cfg.Profiles)
	// A saved profile has to be visible to be selected, and its name need not
	// match whatever the list was filtered to when the form was opened.
	m.filter, m.searching = "", false
	m.cursor = 0
	for index, profile := range m.visibleProfiles() {
		if profile.Name == m.form.name {
			m.cursor = index
			break
		}
	}
	m.setStatus(statusOK, "saved "+m.form.name)
	return nil
}

func resolveWorkingDir(value, current string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("folder is required")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
		}
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(current, value)
	}
	workingDir, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", workingDir)
	}
	return workingDir, nil
}

func (m *tuiModel) execProfile(profile Profile, args []string, exclusive bool) tea.Cmd {
	return m.execProfileIn(profile, args, exclusive, m.workingDir)
}

// execProfileIn runs a profile in an explicit folder. Hijacking needs this:
// the session being reopened belongs to the folder its instance started in,
// not to whatever folder the TUI is currently pointed at.
func (m *tuiModel) execProfileIn(profile Profile, args []string, exclusive bool, folder string) tea.Cmd {
	workdir, err := ensureProfileState(profile)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	var lockDir string
	var unlock func() string
	if exclusive {
		lockDir, unlock, err = acquireExclusiveRunLock(workdir)
	} else {
		lockDir, unlock, err = acquireProfileRunLock(profile, workdir)
	}
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	cmd := exec.Command(profile.Command, args...)
	cmd.Dir = folder
	// The environment depends on the lock directory: an isolated OpenCode
	// instance points its data/state homes at the instance, not the profile.
	cmd.Env = launchEnvironment(profile, workdir, lockDir, os.Environ())
	if cmd, err = applyIndicator(cmd, profile, lockDir); err != nil {
		unlock()
		m.setStatus(statusErr, err.Error())
		return nil
	}
	m.running = true
	m.unlock = unlock
	return tea.Exec(trackedExecCommand{cmd: cmd, workdir: lockDir, title: sessionTitle(profile)}, func(err error) tea.Msg {
		return processFinishedMsg{err: err}
	})
}

func (m *tuiModel) execUpdate(profile Profile) tea.Cmd {
	args, err := updateArgs(profile.Provider)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return nil
	}
	cmd := exec.Command(profile.Command, args...)
	cmd.Dir = m.workingDir
	m.running = true
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return processFinishedMsg{err: err}
	})
}

func (m *tuiModel) execInstall() tea.Cmd {
	install := m.install
	m.running = true
	return tea.Exec(&installExecCommand{install: install}, func(err error) tea.Msg {
		return processFinishedMsg{err: err}
	})
}

func (m *tuiModel) execSelfUpdate() tea.Cmd {
	m.running = true
	return tea.Exec(&selfUpdateExecCommand{}, func(err error) tea.Msg {
		return processFinishedMsg{err: err}
	})
}

// installExecCommand and selfUpdateExecCommand both run several steps rather
// than one process, which tea.ExecProcess cannot express. They take the terminal
// the same way a launch does, so an installer that asks a question can.
type installExecCommand struct {
	install providerInstall
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

func (c *installExecCommand) SetStdin(reader io.Reader)  { c.stdin = reader }
func (c *installExecCommand) SetStdout(writer io.Writer) { c.stdout = writer }
func (c *installExecCommand) SetStderr(writer io.Writer) { c.stderr = writer }
func (c *installExecCommand) Run() error {
	return runInstall(c.install, c.stdin, c.stdout, c.stderr)
}

type selfUpdateExecCommand struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c *selfUpdateExecCommand) SetStdin(reader io.Reader)  { c.stdin = reader }
func (c *selfUpdateExecCommand) SetStdout(writer io.Writer) { c.stdout = writer }
func (c *selfUpdateExecCommand) SetStderr(writer io.Writer) { c.stderr = writer }

// Run resolves the checkout for real (cloning/switching branches as needed,
// unlike the preview shown before confirmation) and, on success, reopens ai —
// which is why this never returns on the success path.
func (c *selfUpdateExecCommand) Run() error {
	var dir string
	if override := strings.TrimSpace(os.Getenv(sourceDirEnv)); override != "" {
		resolved, err := validateSourceDir(override)
		if err != nil {
			return err
		}
		dir = resolved
	} else {
		resolved, err := ensureRepo(c.stdout, c.stderr)
		if err != nil {
			return err
		}
		dir = resolved
	}
	return performSelfUpdate(dir, c.stdin, c.stdout, c.stderr)
}

type trackedExecCommand struct {
	cmd     *exec.Cmd
	workdir string
	title   string
}

func (c trackedExecCommand) SetStdin(reader io.Reader)  { c.cmd.Stdin = reader }
func (c trackedExecCommand) SetStdout(writer io.Writer) { c.cmd.Stdout = writer }
func (c trackedExecCommand) SetStderr(writer io.Writer) { c.cmd.Stderr = writer }
func (c trackedExecCommand) Run() error {
	// Bubble Tea has already released the terminal, so the title escape reaches
	// it directly rather than being swallowed by the alternate screen.
	restoreTitle := markTerminalTitle(c.cmd.Stdout, c.title)
	defer restoreTitle()
	defer stopTmuxServer(c.workdir)
	if err := c.cmd.Start(); err != nil {
		return err
	}
	if err := setProfileChildPID(c.workdir, c.cmd.Process.Pid); err != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
		return err
	}
	_ = setProfileInstanceMeta(c.workdir, c.cmd.Dir)
	return c.cmd.Wait()
}

const usageWidth = 4

type usageWindowKind int

const (
	fiveHourWindow usageWindowKind = iota
	weeklyWindow
)

// usageCellWith renders one quota figure for the accounts table. The pen carries
// the row's background so a selected row stays highlighted through a cell that
// sets its own colour.
func (m tuiModel) usageCellWith(ink pen, profile Profile, kind usageWindowKind) string {
	if m.usage == nil {
		return ink.render(unknownStyle, pad("…", usageWidth))
	}
	usage, exists := m.usage[profile.Name]
	if !exists {
		return ink.render(unknownStyle, pad("—", usageWidth))
	}
	window := usage.FiveHour
	if kind == weeklyWindow {
		window = usage.Weekly
	}
	if !window.Known {
		return ink.render(unknownStyle, pad("—", usageWidth))
	}
	return ink.render(usageStyle(window), pad(fmt.Sprintf("%d%%", window.Percent), usageWidth))
}

// authWidth fits the widest cell rendered by authCell, plus the AUTH heading.
const authWidth = 5

func authCell(profile Profile) string {
	switch profileAuthState(profile) {
	case authPresent:
		return authPresentStyle.Render(pad("● yes", authWidth))
	case authAPIKey:
		return authKeyStyle.Render(pad("● key", authWidth))
	case authMissing:
		return authMissingStyle.Render(pad("○ no", authWidth))
	default:
		return authUnknownStyle.Render(pad("· ?", authWidth))
	}
}

// windowRows scrolls a list longer than the space it has, keeping the row under
// the cursor in view. A picker that cannot show the row about to be acted on is
// worse than a short list: the keys still work, and there is nothing on screen
// saying what they would do.
func windowRows(rows []string, cursor, height int) []string {
	if height < 1 || len(rows) <= height {
		return rows
	}
	start := min(max(cursor-height/2, 0), len(rows)-height)
	return rows[start : start+height]
}

func quotaStyle(percent int) lipgloss.Style {
	switch {
	case percent < 0:
		return unknownStyle
	case percent <= 10:
		return usageCriticalStyle
	case percent <= 25:
		return usageWarningStyle
	default:
		return usageGoodStyle
	}
}

// statusIcon marks how a message landed. The log panel and the modal footer
// share it so one glance means the same thing in both places.
func statusIcon(kind statusKind) (string, lipgloss.Style) {
	switch kind {
	case statusOK:
		return "✓", statusOKStyle
	case statusErr:
		return "✗", statusErrStyle
	default:
		return "•", statusInfoStyle
	}
}

// instanceTitle names the conversation an instance has open, or says why it
// cannot. A lookup still in flight reads as a placeholder rather than as an
// answer, so a slow provider is never mistaken for a nameless session.
func (m tuiModel) instanceTitle(instance profileInstance) string {
	if instance.session.title != "" {
		return instance.session.title
	}
	if m.describing {
		return "…"
	}
	if instance.session.id != "" {
		return "untitled session"
	}
	return "—"
}

func shortenHome(path string) string {
	if path == "" {
		return "unknown folder"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

func terminateProfile(profile Profile) error {
	root, err := profileRoot()
	if err != nil {
		return err
	}
	workdir := filepath.Join(root, profile.Name)
	lockDirs, err := activeProfileLocks(workdir)
	if err != nil {
		return err
	}
	if len(lockDirs) == 0 {
		return os.ErrNotExist
	}
	var errs []error
	for _, lockDir := range lockDirs {
		if _, err := terminateProfileLock(lockDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func terminateProfileLock(lockDir string) (string, error) {
	// A wrapped launch runs the CLI under a tmux server of its own; killing the
	// client alone would leave it running headless on its socket.
	stopTmuxServer(lockDir)
	pid, err := profileLockPID(lockDir)
	if err != nil {
		return "", err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return "", err
	}
	if err := process.Signal(syscall.Signal(0)); errors.Is(err, syscall.ESRCH) {
		if note, ok := retireStaleLockDir(lockDir); ok {
			return note, nil
		}
		removeProfileLock(lockDir)
		return "", staleProfileLockError{pid: pid}
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			if note, ok := retireStaleLockDir(lockDir); ok {
				return note, nil
			}
			removeProfileLock(lockDir)
			return "", nil
		}
		return "", err
	}
	// A kill from here usually self-heals: the launcher waiting on the child
	// exits through the normal path and merges on the way out. This retire is
	// the backstop for the launches that cannot — and for the store left
	// behind while the victim finishes dying, the wait below comes first.
	if waitForPIDDeath(pid, 5*time.Second) {
		if note, ok := retireStaleLockDir(lockDir); ok {
			return note, nil
		}
	}
	return "", nil
}

// retireStaleLockDir merges and removes an isolated OpenCode instance
// directory, for the kill paths and the stale-lock branches. It reports
// whether the directory was an isolated instance at all; anything else keeps
// the old remove-the-lock-files behavior in the caller.
func retireStaleLockDir(lockDir string) (string, bool) {
	if !isIsolatedInstanceDir(lockDir) {
		return "", false
	}
	workdir, profile, ok := profileOfInstanceDir(lockDir)
	if !ok {
		return "", false
	}
	if note := retireIsolatedInstance(workdir, profile, lockDir); note != "" {
		return note, true
	}
	return "cleared stale instance store", true
}

// waitForPIDDeath polls for a process to actually exit after a kill, so a
// merge that follows does not open a database its writer still holds.
func waitForPIDDeath(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func removeProfileLock(lockDir string) {
	_ = os.Remove(filepath.Join(lockDir, ".active.lock"))
	_ = os.Remove(filepath.Join(lockDir, instanceMetaFile))
	if filepath.Base(filepath.Dir(lockDir)) == instancesDirectory {
		_ = os.RemoveAll(lockDir)
	}
}

type staleProfileLockError struct {
	pid int
}

func (e staleProfileLockError) Error() string {
	return fmt.Sprintf("cleared stale profile lock (process %d is not running)", e.pid)
}

func sortedProfiles(profiles []Profile) []Profile {
	result := append([]Profile(nil), profiles...)
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// --- Cloning a profile, and lending what is in one -------------------------

// shareKind is what a copy between two profiles is carrying. The two kinds go
// through the same three frames — pick a source, tick what to take, apply —
// because from the keyboard they are the same question asked about different
// things, and the differences between them are all below this file.
type shareKind int

const (
	shareMCP shareKind = iota
	shareSkills
)

func (kind shareKind) noun() string {
	if kind == shareSkills {
		return "skill"
	}
	return "MCP server"
}

func (kind shareKind) supported(profile Profile) bool {
	if kind == shareSkills {
		return supportsSkills(profile)
	}
	return supportsMCP(profile)
}

// read lists what one profile has of this kind, already in picker shape. The
// detail line is what makes a row worth reading: an MCP server is identified by
// where it points, a skill by what it says it is for.
func (kind shareKind) read(profile Profile) ([]shareItem, error) {
	if kind == shareSkills {
		skills, err := readSkills(profile)
		if err != nil {
			return nil, err
		}
		items := make([]shareItem, 0, len(skills))
		for _, entry := range skills {
			items = append(items, shareItem{name: entry.name, detail: entry.description})
		}
		return items, nil
	}
	servers, err := readMCPServers(profile)
	if err != nil {
		return nil, err
	}
	items := make([]shareItem, 0, len(servers))
	for index := range servers {
		server := servers[index]
		items = append(items, shareItem{name: server.Name, detail: server.endpoint(), server: &server})
	}
	return items, nil
}

func (kind shareKind) copy(source, destination Profile, names []string) ([]string, error) {
	if kind == shareSkills {
		return copySkills(source, destination, names, true)
	}
	return copyMCPServers(source, destination, names, true)
}

// shareDraft is a copy being set up: what kind of thing, which profiles could
// lend one, and which of the chosen profile's items are ticked. The destination
// is not in here — it is whichever profile the cursor is on, the same profile
// every other key on the list acts on.
type shareDraft struct {
	kind    shareKind
	sources []shareSource
	source  int
	items   []shareItem
	item    int
	// focus is which pane the arrows move in: the lenders on the left, or what
	// the chosen one has on the right.
	focus int
	// dry lists the other profiles with nothing of this kind to lend. They are
	// named rather than left out, so an account that is missing from the list
	// is visibly empty rather than overlooked.
	dry []Profile
}

const (
	shareFocusSources = iota
	shareFocusItems
)

// shareSource is one profile that has something to lend, with how much of it.
// The count is taken when the picker opens rather than when a row is drawn:
// rendering a frame is not the place to reread four config files, and the
// cockpit redraws on every keypress.
type shareSource struct {
	profile Profile
	count   int
}

// shareItem is one row of the multi-select.
type shareItem struct {
	name   string
	detail string
	// present is whether the destination already has one by this name. It is
	// shown rather than filtered out, because "you already have this" is an
	// answer the picker was opened to get.
	present bool
	chosen  bool
	// server is the MCP definition behind the row, kept so the box can say how
	// it will be rewritten for the destination. Skills have none.
	server *mcpServer
}

// shareSource is the profile lending, when one has been chosen. Both the
// renderer and the apply step index the same slice, and neither should be the
// place a stale index becomes a panic.
func (m tuiModel) shareSource() (Profile, bool) {
	if m.share.source < 0 || m.share.source >= len(m.share.sources) {
		return Profile{}, false
	}
	return m.share.sources[m.share.source].profile, true
}

func (draft shareDraft) chosenNames() []string {
	var names []string
	for _, item := range draft.items {
		if item.chosen {
			names = append(names, item.name)
		}
	}
	return names
}

// openShare starts a copy into the selected profile. Everything that would make
// the copy impossible is settled here, before a picker offers a choice that
// cannot be carried out.
func (m *tuiModel) openShare(kind shareKind) {
	destination, ok := m.selectedProfile()
	if !ok {
		return
	}
	if !kind.supported(destination) {
		m.setStatus(statusErr, fmt.Sprintf("%s (%s) keeps no %ss this launcher can write",
			destination.Name, destination.Provider, kind.noun()))
		return
	}
	if profileIsRunning(destination) {
		m.setStatus(statusErr, "cannot write to a running profile's configuration")
		return
	}
	sources := shareSources(m.profiles, destination, kind)
	if len(sources) == 0 {
		m.setStatus(statusErr, "no other profile has a "+kind.noun()+" to lend")
		return
	}
	m.share = shareDraft{kind: kind, sources: sources, dry: dryProfiles(m.profiles, destination, sources)}
	if err := m.loadShareItems(); err != nil {
		m.setStatus(statusErr, err.Error())
		return
	}
	m.mode = tuiShare
	m.clearStatus()
}

// dryProfiles is every other profile that is not lending anything.
func dryProfiles(profiles []Profile, destination Profile, sources []shareSource) []Profile {
	lending := make(map[string]bool, len(sources))
	for _, source := range sources {
		lending[source.profile.Name] = true
	}
	var dry []Profile
	for _, profile := range profiles {
		if profile.Name != destination.Name && !lending[profile.Name] {
			dry = append(dry, profile)
		}
	}
	return dry
}

// shareSources lists the profiles with something of this kind to give. A
// profile with none is left out rather than offered and then found empty: the
// picker's job is to narrow the choice, and a row that leads nowhere widens it.
func shareSources(profiles []Profile, destination Profile, kind shareKind) []shareSource {
	var sources []shareSource
	for _, profile := range profiles {
		if profile.Name == destination.Name || !kind.supported(profile) {
			continue
		}
		items, err := kind.read(profile)
		if err != nil || len(items) == 0 {
			continue
		}
		sources = append(sources, shareSource{profile: profile, count: len(items)})
	}
	return sources
}

// loadShareItems fills the multi-select from the chosen source, marking the
// rows the destination already has.
func (m *tuiModel) loadShareItems() error {
	destination, ok := m.selectedProfile()
	if !ok {
		return errors.New("nothing selected")
	}
	source, chosen := m.shareSource()
	if !chosen {
		return errors.New("no source chosen")
	}
	items, err := m.share.kind.read(source)
	if err != nil {
		return err
	}
	existing, err := m.share.kind.read(destination)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(existing))
	for _, item := range existing {
		present[item.name] = true
	}
	for index := range items {
		items[index].present = present[items[index].name]
	}
	m.share.items, m.share.item = items, 0
	return nil
}

// updateShare drives the one box a copy happens in: lenders on the left, what
// the chosen one has on the right. Moving through the lenders reads each one
// as the cursor lands on it, so the right pane always answers for the row on
// the left.
func (m tuiModel) updateShare(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.mode = tuiList
		m.clearStatus()
	case "tab":
		// The other kind of thing to copy, into the same profile. It is a new
		// question, so whatever was ticked for the old one is dropped.
		kind := shareSkills
		if m.share.kind == shareSkills {
			kind = shareMCP
		}
		// With nothing of that kind to lend, openShare says so and this box
		// stays on the kind it was showing.
		m.openShare(kind)
	case "left", "h":
		m.share.focus = shareFocusSources
	case "right", "l":
		if len(m.share.items) > 0 {
			m.share.focus = shareFocusItems
		}
	case "up", "k":
		if m.share.focus == shareFocusSources {
			if m.share.source > 0 {
				m.share.source--
				m.reloadShareItems()
			}
		} else if m.share.item > 0 {
			m.share.item--
		}
	case "down", "j":
		if m.share.focus == shareFocusSources {
			if m.share.source < len(m.share.sources)-1 {
				m.share.source++
				m.reloadShareItems()
			}
		} else if m.share.item < len(m.share.items)-1 {
			m.share.item++
		}
	case " ":
		if m.share.focus == shareFocusSources {
			m.share.focus = shareFocusItems
			return m, nil
		}
		if m.share.item >= 0 && m.share.item < len(m.share.items) {
			m.share.items[m.share.item].chosen = !m.share.items[m.share.item].chosen
		}
	case "a":
		// Ticking everything deliberately passes over what the destination
		// already has. Those rows overwrite something, so they are ticked one
		// at a time or not at all — a bulk key must not be the thing that
		// replaced a definition nobody looked at.
		m.toggleAllShareItems()
	case "enter":
		if len(m.share.chosenNames()) == 0 && m.share.focus == shareFocusSources {
			m.share.focus = shareFocusItems
			return m, nil
		}
		return m.applyShare()
	}
	return m, nil
}

// reloadShareItems reads the lender the cursor just moved to.
func (m *tuiModel) reloadShareItems() {
	if err := m.loadShareItems(); err != nil {
		m.share.items = nil
		m.setStatus(statusErr, err.Error())
		return
	}
	m.clearStatus()
}

func (m *tuiModel) toggleAllShareItems() {
	all := true
	for _, item := range m.share.items {
		if !item.present && !item.chosen {
			all = false
			break
		}
	}
	for index := range m.share.items {
		if m.share.items[index].present {
			continue
		}
		m.share.items[index].chosen = !all
	}
}

func (m tuiModel) applyShare() (tea.Model, tea.Cmd) {
	destination, ok := m.selectedProfile()
	if !ok {
		m.mode = tuiList
		return m, nil
	}
	source, ok := m.shareSource()
	if !ok {
		m.mode = tuiList
		return m, nil
	}
	names := m.share.chosenNames()
	if len(names) == 0 {
		m.setStatus(statusErr, "nothing ticked; space selects a row")
		return m, nil
	}
	copied, err := m.share.kind.copy(source, destination, names)
	if err != nil {
		m.setStatus(statusErr, err.Error())
		return m, nil
	}
	m.mode = tuiList
	m.setStatus(statusOK, fmt.Sprintf("copied %s from %s: %s",
		plural(len(copied), m.share.kind.noun()), source.Name, strings.Join(copied, ", ")))
	return m, nil
}

// updateClone takes the new profile's name. A clone is a name and nothing else
// to decide: everything about how it launches comes from the profile it copies,
// which is the point of having the key at all.
func (m tuiModel) updateClone(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = tuiList
		m.clearStatus()
		return m, nil
	case "enter":
		if err := m.saveClone(); err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.mode = tuiList
		return m, tea.Batch(loadUsageCmd(m.profiles), m.loadCockpitCmd())
	case "backspace", "ctrl+h":
		if runes := []rune(m.clone); len(runes) > 0 {
			m.clone = string(runes[:len(runes)-1])
		}
		return m, nil
	case "ctrl+u":
		m.clone = ""
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		m.clone += string(msg.Runes)
	}
	return m, nil
}

func (m *tuiModel) saveClone() error {
	source, ok := m.selectedProfile()
	if !ok {
		return errors.New("nothing selected")
	}
	cfg, err := loadConfig(m.configPath)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(m.clone)
	if err := cloneProfile(source.Name, name, false, &cfg, m.configPath, io.Discard); err != nil {
		return err
	}
	m.profiles = sortedProfiles(cfg.Profiles)
	// The clone has to be visible to be selected, and a filter that matched the
	// profile it came from need not match the name just typed.
	m.filter, m.searching = "", false
	m.cursor = 0
	for index, profile := range m.visibleProfiles() {
		if profile.Name == name {
			m.cursor = index
			break
		}
	}
	m.setStatus(statusOK, "cloned "+source.Name+" into "+name+" — log in with l")
	return nil
}
