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
	tuiParams
	tuiConfirmInstall
	tuiConfirmSelfUpdate
	tuiHelp
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
	unlock     func()
	usage      map[string]usageRemaining
	workingDir string
	folderPath string
	params     string
	instances  []profileInstance
	instance   int
	describing bool
	update     updateStatus
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

// visibleProfiles is the list the cursor moves through. Everything that acts on
// "the selected profile" goes through it, so a filtered list can never run the
// account that merely happens to sit at the same index in the full one.
func (m tuiModel) visibleProfiles() []Profile {
	if m.filter == "" {
		return m.profiles
	}
	query := strings.ToLower(m.filter)
	matches := make([]Profile, 0, len(m.profiles))
	for _, profile := range m.profiles {
		if strings.Contains(strings.ToLower(profile.Name), query) ||
			strings.Contains(strings.ToLower(profile.Provider), query) {
			matches = append(matches, profile)
		}
	}
	return matches
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

func (m *tuiModel) setStatus(kind statusKind, message string) {
	m.statusKind = kind
	m.status = message
	if message == "" {
		return
	}
	m.log = append([]logEntry{{kind: kind, text: message}}, m.log...)
	if len(m.log) > logLimit {
		m.log = m.log[:logLimit]
	}
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
	m := tuiModel{configPath: configPath, profiles: sortedProfiles(cfg.Profiles), workingDir: workingDir}
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
		case tuiParams:
			return m.updateParams(msg)
		case tuiConfirmInstall:
			return m.updateInstall(msg)
		case tuiConfirmSelfUpdate:
			return m.updateSelfUpdate(msg)
		case tuiHelp:
			return m.updateHelp(msg)
		}
	case processFinishedMsg:
		m.running = false
		if m.unlock != nil {
			m.unlock()
			m.unlock = nil
		}
		if msg.err != nil {
			m.setStatus(statusErr, "process exited: "+msg.err.Error())
		} else {
			m.setStatus(statusOK, "process finished")
		}
		return m, tea.Batch(loadUsageCmd(m.profiles), m.loadCockpitCmd())
	case cockpitTickMsg:
		return m, tea.Batch(m.loadCockpitCmd(), cockpitTickCmd())
	case cockpitLoadedMsg:
		// What is running is true of the whole machine, so it lands either way.
		m.live, m.now, m.loaded = msg.live, msg.now, true
		if profile, ok := m.selectedProfile(); ok && profile.Name == msg.profile {
			m.recent, m.activity = msg.recent, msg.activity
		}
	case usageLoadedMsg:
		m.usage = msg
	case updateCheckedMsg:
		m.update = msg.status
		if msg.forced {
			kind := statusOK
			if !msg.status.Known {
				kind = statusErr
			}
			m.setStatus(kind, msg.status.message())
		}
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
	case "?":
		m.mode = tuiHelp
		m.clearStatus()
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
		m.usage = nil
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
			args, err := resumeArgs(profile.Provider)
			if err != nil {
				m.setStatus(statusErr, err.Error())
				return m, nil
			}
			return m, m.execProfile(profile, profileRunArgs(profile, args), false)
		}
	case "p":
		if hasSelection {
			m.mode = tuiParams
			m.params = ""
			m.clearStatus()
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
		source, err := sourceDir()
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

// updateHelp closes on anything. A pane that only lists keys has nothing to do
// with one, and needing to remember which key dismisses the key list would be
// its own small joke.
func (m tuiModel) updateHelp(tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.mode = tuiList
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
		args, err := hijackArgs(profile.Provider, instance.session)
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
		return m, m.execProfile(profile, profileRunArgs(profile, args), false)
	case "backspace", "ctrl+h":
		if runes := []rune(m.params); len(runes) > 0 {
			m.params = string(runes[:len(runes)-1])
		}
		return m, nil
	case "ctrl+u":
		m.params = ""
		return m, nil
	}
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
		m.params += string(msg.Runes)
	}
	return m, nil
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
		if err := terminateProfileLock(instance.lockDir); err != nil {
			var staleErr staleProfileLockError
			if errors.As(err, &staleErr) {
				m.setStatus(statusOK, staleErr.Error())
			} else {
				m.setStatus(statusErr, "stop failed: "+err.Error())
			}
		} else {
			name, _ := m.selectedProfile()
			m.setStatus(statusOK, fmt.Sprintf("stopped %s instance %d (PID %d)", name.Name, m.instance+1, instance.pid))
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

func (m tuiModel) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = tuiList
		m.clearStatus()
		return m, nil
	case "tab", "down", "enter":
		if m.form.field < 4 {
			m.form.field++
			return m, nil
		}
		if err := m.saveForm(); err != nil {
			m.setStatus(statusErr, err.Error())
			return m, nil
		}
		m.mode = tuiList
		return m, tea.Batch(loadUsageCmd(m.profiles), m.loadCockpitCmd())
	case "shift+tab", "up":
		if m.form.field > 0 {
			m.form.field--
		}
		return m, nil
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
		if _, err := findProfile(cfg, m.form.name); err == nil {
			return fmt.Errorf("profile %q already exists", m.form.name)
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
			if _, err := findProfile(cfg, m.form.name); err == nil {
				return fmt.Errorf("profile %q already exists", m.form.name)
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
	var unlock func()
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
	cmd.Env = launchEnvironment(profile, os.Environ())
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
	source := m.source
	m.running = true
	return tea.Exec(&selfUpdateExecCommand{source: source}, func(err error) tea.Msg {
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
	source string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c *selfUpdateExecCommand) SetStdin(reader io.Reader)  { c.stdin = reader }
func (c *selfUpdateExecCommand) SetStdout(writer io.Writer) { c.stdout = writer }
func (c *selfUpdateExecCommand) SetStderr(writer io.Writer) { c.stderr = writer }
func (c *selfUpdateExecCommand) Run() error {
	return runSelfUpdate(c.source, c.stdin, c.stdout, c.stderr)
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

func (m tuiModel) formContent() []string {
	heading := "New profile"
	if !m.form.isNew {
		heading = "Edit " + m.form.original
	}
	fields := []struct{ label, value string }{
		{"Name", m.form.name},
		{"Provider", m.form.provider},
		{"Command", m.form.command},
		{"Default args", m.form.defaultArgs},
		{"Notes", m.form.notes},
	}
	lines := []string{headerTitleStyle.Render(heading), ""}
	for index, field := range fields {
		label, value := fieldLabelStyle.Render(pad(field.label, 12)), fieldValueStyle.Render(field.value)
		if index == m.form.field {
			label = fieldLabelActive.Render(pad(field.label, 12))
			value += cursorStyle.Render(" ")
		}
		lines = append(lines, label+" "+value)
	}
	if m.form.field == 1 {
		lines = append(lines, "", hintStyle.Render("known providers: codex · claude · antigravity · opencode · deepseek"))
	} else if m.form.field == 3 {
		lines = append(lines, "", hintStyle.Render("shell-style quotes are supported; arguments apply only when running"))
	}
	return lines
}

func (m tuiModel) folderContent() []string {
	return []string{
		headerTitleStyle.Render("Change launch folder"),
		"",
		fieldLabelActive.Render(pad("Folder", 12)) + " " + fieldValueStyle.Render(m.folderPath) + cursorStyle.Render(" "),
		"",
		hintStyle.Render("Relative paths use the current launch folder. ~ is supported."),
	}
}

func (m tuiModel) paramsContent() []string {
	name := "profile"
	if profile, ok := m.selectedProfile(); ok {
		name = profile.Name
	}
	return []string{
		headerTitleStyle.Render("Run " + name + " with arguments"),
		"",
		fieldLabelActive.Render(pad("Arguments", 12)) + " " + fieldValueStyle.Render(m.params) + cursorStyle.Render(" "),
		"",
		hintStyle.Render("Added after the profile default arguments. Shell-style quotes are supported."),
	}
}

func (m tuiModel) installContent(width int) []string {
	value := max(width-detailLabelWidth, 8)
	return []string{
		headerTitleStyle.Render("Install the " + m.install.provider + " CLI"),
		"",
		fieldLabelStyle.Render(pad("runs", detailLabelWidth)) + fieldValueStyle.Render(truncate(m.install.command(), value)),
		fieldLabelStyle.Render(pad("provides", detailLabelWidth)) + fieldValueStyle.Render(truncate(defaultCommand(m.install.provider)+" on PATH", value)),
		"",
		confirmBodyStyle.Render("The script is downloaded before it is run, and runs in your"),
		confirmBodyStyle.Render("own environment rather than a profile's isolated one."),
		"",
		hintStyle.Render("y installs · n cancels"),
	}
}

func (m tuiModel) selfUpdateContent(width int) []string {
	value := max(width-detailLabelWidth, 8)
	lines := []string{
		headerTitleStyle.Render("Update ai-session"),
		"",
		fieldLabelStyle.Render(pad("checkout", detailLabelWidth)) + fieldValueStyle.Render(truncate(shortenHome(m.source), value)),
		fieldLabelStyle.Render(pad("status", detailLabelWidth)) + fieldValueStyle.Render(truncate(m.update.message(), value)),
		"",
	}
	for _, step := range selfUpdateSteps(m.source) {
		lines = append(lines, hintStyle.Render("› "+strings.Join(step, " ")))
	}
	return append(lines,
		"",
		confirmBodyStyle.Render("The rebuilt binary applies to the next ai you start."),
		"",
		hintStyle.Render("y updates · n cancels"))
}

// helpPane lists every key at once. The bottom bar drops entries from the end
// to fit, so on a narrow terminal it is not the full answer, and the keys it
// drops are exactly the ones a new user has not learned yet.
func (m tuiModel) helpPane(width int) []string {
	// The modal pads two cells on either side of what it is handed, and a line
	// past that budget is wrapped rather than clipped — which for a key list
	// puts a description on a row of its own under the wrong key.
	content := max(width-4, 24)
	columns := helpSections()
	rendered := make([][]string, len(columns))
	// Each column is only as wide as its own content, because an even split
	// would cut the longer side to match a shorter one that did not need it.
	widths := make([]int, len(columns))
	rows := 0
	for index, sections := range columns {
		var lines []string
		for position, section := range sections {
			if position > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, sectionLabelStyle.Render(section.title))
			for _, entry := range section.entries {
				lines = append(lines, helpKeyStyle.Render(pad(entry.key, helpKeyColumn))+helpDescStyle.Render(entry.desc))
			}
		}
		for _, line := range lines {
			widths[index] = max(widths[index], lipgloss.Width(line))
		}
		rendered[index] = lines
		rows = max(rows, len(lines))
	}

	pane := []string{headerTitleStyle.Render("Keys"), ""}
	if widths[0]+helpColumnGap+widths[1] > content {
		// Too narrow for two columns, so they stack. The cockpit behind this box
		// folds rather than squeezes for the same reason: a key list cut to fit
		// is missing the keys nobody has learned yet.
		for index, lines := range rendered {
			if index > 0 {
				pane = append(pane, "")
			}
			for _, line := range lines {
				pane = append(pane, padLine(line, content))
			}
		}
		return append(pane, "", hintStyle.Render("any key closes"))
	}
	for row := range rows {
		left, right := "", ""
		if row < len(rendered[0]) {
			left = rendered[0][row]
		}
		if row < len(rendered[1]) {
			right = rendered[1][row]
		}
		pane = append(pane, padLine(pad(left, widths[0])+strings.Repeat(" ", helpColumnGap)+right, content))
	}
	return append(pane, "", hintStyle.Render("any key closes"))
}

func (m tuiModel) confirmContent(width int) []string {
	profile, ok := m.selectedProfile()
	if !ok {
		return nil
	}
	switch m.mode {
	case tuiHijack:
		lines := append([]string{headerTitleStyle.Render("Open a running " + profile.Name + " session here"), ""},
			m.instanceRows(width, fieldValueStyle)...)
		return append(lines, "", hintStyle.Render("The original instance keeps running; this opens its conversation."))
	case tuiConfirmKill:
		lines := append([]string{dangerTextStyle.Render("Stop a " + profile.Name + " instance?"), ""},
			m.instanceRows(width, dangerTextStyle)...)
		return append(lines, "", confirmBodyStyle.Render("Enter stops the selected instance. a or y stops them all."))
	default:
		return []string{
			dangerTextStyle.Render("Delete " + profile.Name + "?"),
			"",
			confirmBodyStyle.Render("Its isolated state directory and stored credentials"),
			confirmBodyStyle.Render("are removed. This cannot be undone."),
		}
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

// instanceRows renders one running instance per two lines: what it is working
// on, and where it was launched. selected styles the highlighted row, which
// differs between the two pickers that share this list.
func (m tuiModel) instanceRows(width int, selected lipgloss.Style) []string {
	valueWidth := max(width-8, 8)
	rows := make([]string, 0, len(m.instances)*2)
	for index, instance := range m.instances {
		label := fmt.Sprintf("Instance %d (PID %d)  %s", index+1, instance.pid, m.instanceTitle(instance))
		label = truncate(label, valueWidth)
		if index == m.instance {
			rows = append(rows, cursorBarStyle.Render("▌ ")+selected.Render(label))
		} else {
			rows = append(rows, "  "+confirmBodyStyle.Render(label))
		}
		rows = append(rows, "    "+hintStyle.Render(truncate(shortenHome(instance.folder), valueWidth)))
	}
	return rows
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

const (
	// helpKeyColumn fits the widest key label in the help pane, plus a space.
	helpKeyColumn = 8
	// helpColumnGap separates the two columns of keys.
	helpColumnGap = 2
	// helpModalWidth is wider than the prompts share, because the key list is
	// two columns of text rather than one question. A terminal too narrow for
	// that gets one column instead of a clipped two.
	helpModalWidth = 84
)

// helpSection is one titled group of keys in the help pane. The grouping is by
// what the key acts on — a conversation, a profile, a provider's CLI, ai itself
// — because that is how someone looking for a key already thinks about it.
type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSections is two columns of sections, laid out side by side.
func helpSections() [][]helpSection {
	return [][]helpSection{
		{
			{"LAUNCH", []helpEntry{
				{"↵", "run the selected profile"},
				{"p", "run with extra arguments"},
				{"R", "resume a conversation"},
				{"h", "open a running session"},
				{"c", "change the launch folder"},
			}},
			{"PROFILES", []helpEntry{
				{"↑↓ jk", "select"},
				{"/", "filter by name or provider"},
				{"a", "add"},
				{"e", "edit"},
				{"x", "delete"},
			}},
		},
		{
			{"PROVIDER CLI", []helpEntry{
				{"l", "log in"},
				{"i", "install the CLI"},
				{"u", "update the CLI"},
				{"K", "stop a running instance"},
			}},
			{"AI-SESSION", []helpEntry{
				{"U", "update ai-session itself"},
				{"r", "refresh quotas and updates"},
				{"?", "these keys"},
				{"q", "quit"},
			}},
		},
	}
}

func (m tuiModel) helpEntries() []helpEntry {
	switch {
	case m.searching:
		return []helpEntry{{"type", "filter"}, {"↑↓", "choose"}, {"↵", "keep filter"}, {"esc", "clear"}}
	case m.mode == tuiForm:
		return []helpEntry{
			{"tab/↵", "next field"},
			{"↵", "save on last field"},
			{"ctrl-u", "clear"},
			{"esc", "cancel"},
		}
	case m.mode == tuiFolder:
		return []helpEntry{{"↵", "set folder"}, {"ctrl-u", "clear"}, {"esc", "cancel"}}
	case m.mode == tuiConfirmDelete:
		return []helpEntry{{"y", "delete"}, {"n/esc", "keep"}}
	case m.mode == tuiConfirmKill:
		return []helpEntry{{"↑↓/jk", "choose instance"}, {"↵", "stop selected"}, {"a/y", "stop all"}, {"n/esc", "keep running"}}
	case m.mode == tuiHijack:
		return []helpEntry{{"↑↓/jk", "choose instance"}, {"↵", "open here"}, {"esc", "cancel"}}
	case m.mode == tuiParams:
		return []helpEntry{{"↵", "run"}, {"ctrl-u", "clear"}, {"esc", "cancel"}}
	case m.mode == tuiConfirmInstall:
		return []helpEntry{{"y", "install"}, {"n/esc", "cancel"}}
	case m.mode == tuiConfirmSelfUpdate:
		return []helpEntry{{"y", "update"}, {"n/esc", "cancel"}}
	case m.mode == tuiHelp:
		return []helpEntry{{"any key", "close"}}
	case len(m.profiles) == 0:
		return []helpEntry{{"a", "add profile"}, {"q", "quit"}}
	default:
		return []helpEntry{
			{"↵", "run"},
			{"p", "args"},
			{"R", "resume"},
			{"h", "hijack"},
			{"l", "login"},
			{"i", "install"},
			{"u", "update"},
			{"U", "update ai"},
			{"c", "folder"},
			{"r", "refresh"},
			{"a e x", "add edit delete"},
			{"K", "stop"},
		}
	}
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
		if err := terminateProfileLock(lockDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func terminateProfileLock(lockDir string) error {
	// A wrapped launch runs the CLI under a tmux server of its own; killing the
	// client alone would leave it running headless on its socket.
	stopTmuxServer(lockDir)
	pid, err := profileLockPID(lockDir)
	if err != nil {
		return err
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Signal(syscall.Signal(0)); errors.Is(err, syscall.ESRCH) {
		removeProfileLock(lockDir)
		return staleProfileLockError{pid: pid}
	}
	if err := process.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			removeProfileLock(lockDir)
			return nil
		}
		return err
	}
	return nil
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
