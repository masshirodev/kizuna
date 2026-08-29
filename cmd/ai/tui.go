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
	width      int
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
}

func (m *tuiModel) setStatus(kind statusKind, message string) {
	m.statusKind = kind
	m.status = message
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
	return tea.Batch(loadUsageCmd(m.profiles), checkUpdateCmd(false))
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
		m.width = msg.Width
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
		return m, loadUsageCmd(m.profiles)
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
		if m.cursor < len(m.profiles) && m.profiles[m.cursor].Name == msg.profile && len(msg.instances) == len(m.instances) {
			m.instances = msg.instances
		}
	}
	return m, nil
}

func (m tuiModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.profiles)-1 {
			m.cursor++
		}
	case "a":
		m.mode = tuiForm
		m.form = profileForm{name: "", provider: "codex", command: "codex", isNew: true}
		m.clearStatus()
	case "c":
		m.mode = tuiFolder
		m.folderPath = m.workingDir
		m.clearStatus()
	case "e":
		if len(m.profiles) > 0 {
			profile := m.profiles[m.cursor]
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
		return m, tea.Batch(loadUsageCmd(m.profiles), checkUpdateCmd(true))
	case "x":
		if len(m.profiles) > 0 {
			m.mode = tuiConfirmDelete
			m.clearStatus()
		}
	case "K":
		if len(m.profiles) > 0 {
			return m, m.openInstances(tuiConfirmKill)
		}
	case "h":
		if len(m.profiles) > 0 {
			return m, m.openInstances(tuiHijack)
		}
	case "R":
		if len(m.profiles) > 0 {
			profile := m.profiles[m.cursor]
			args, err := resumeArgs(profile.Provider)
			if err != nil {
				m.setStatus(statusErr, err.Error())
				return m, nil
			}
			return m, m.execProfile(profile, profileRunArgs(profile, args), false)
		}
	case "p":
		if len(m.profiles) > 0 {
			m.mode = tuiParams
			m.params = ""
			m.clearStatus()
		}
	case "l":
		if len(m.profiles) > 0 {
			return m, m.execProfile(m.profiles[m.cursor], loginArgs(m.profiles[m.cursor].Provider), true)
		}
	case "enter":
		if len(m.profiles) > 0 {
			profile := m.profiles[m.cursor]
			return m, m.execProfile(profile, profileRunArgs(profile, nil), false)
		}
	case "u":
		if len(m.profiles) > 0 {
			return m, m.execUpdate(m.profiles[m.cursor])
		}
	}
	return m, nil
}

// openInstances shows the running instances of the selected profile and starts
// resolving what each one is working on.
func (m *tuiModel) openInstances(mode tuiMode) tea.Cmd {
	profile := m.profiles[m.cursor]
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
		profile := m.profiles[m.cursor]
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
		profile := m.profiles[m.cursor]
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
			m.setStatus(statusOK, fmt.Sprintf("stopped %s instance %d (PID %d)", m.profiles[m.cursor].Name, m.instance+1, instance.pid))
		}
		m.instances = nil
		m.mode = tuiList
	case "a", "y", "Y":
		profile := m.profiles[m.cursor]
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
		profile := m.profiles[m.cursor]
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
				if m.cursor >= len(m.profiles) && m.cursor > 0 {
					m.cursor--
				}
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
		return m, loadUsageCmd(m.profiles)
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
	m.cursor = 0
	for index, profile := range m.profiles {
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
	cmd.Env = append(cleanEnvironment(os.Environ()), profileEnv(profile)...)
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

func (m tuiModel) View() string {
	width := m.contentWidth()
	sections := []string{m.headerView(width)}
	switch m.mode {
	case tuiForm:
		sections = append(sections, m.formView(width))
	case tuiFolder:
		sections = append(sections, m.folderView(width))
	case tuiParams:
		sections = append(sections, m.paramsView(width))
	case tuiConfirmDelete, tuiConfirmKill, tuiHijack:
		sections = append(sections, m.listView(width), m.selectedProfileView(width), m.confirmView(width))
	default:
		sections = append(sections, m.listView(width), m.selectedProfileView(width))
	}
	sections = append(sections, rule(width), m.helpView(width))
	if update := m.updateView(width); update != "" {
		sections = append(sections, update)
	}
	if status := m.statusView(width); status != "" {
		sections = append(sections, status)
	}
	return "\n" + lipgloss.NewStyle().PaddingLeft(2).Render(
		lipgloss.JoinVertical(lipgloss.Left, sections...),
	) + "\n"
}

// contentWidth keeps the layout comfortable on wide terminals and still usable
// on narrow ones; a zero width means the terminal size is not known yet.
func (m tuiModel) contentWidth() int {
	width := m.width - 4
	if m.width == 0 {
		width = 72
	}
	return min(max(width, 34), 86)
}

// headerView names the profile under the cursor beside the count. The row
// highlight alone says which row is selected but not which account is in play,
// which is the thing worth reading back before pressing enter.
func (m tuiModel) headerView(width int) string {
	title := lipgloss.JoinHorizontal(lipgloss.Center, appBadgeStyle.Render("ai"), " ", headerTitleStyle.Render("session profiles"))
	count := fmt.Sprintf("%d profiles", len(m.profiles))
	switch len(m.profiles) {
	case 0:
		count = "no profiles"
	case 1:
		count = "1 profile"
	}
	right := headerCountStyle.Render(count)
	if name := m.selectedName(); name != "" {
		// Drop the name rather than the count when the two cannot both fit:
		// a truncated name is worse than none at identifying an account.
		if room := width - lipgloss.Width(title) - lipgloss.Width(count) - 4; room >= lipgloss.Width(name) {
			right = headerProfileStyle.Render(name) + headerCountStyle.Render(" · "+count)
		}
	}
	return spread(title, right, width) + "\n" + rule(width) + "\n"
}

func (m tuiModel) selectedName() string {
	if m.cursor < 0 || m.cursor >= len(m.profiles) {
		return ""
	}
	return m.profiles[m.cursor].Name
}

func (m tuiModel) listView(width int) string {
	if len(m.profiles) == 0 {
		return emptyStateStyle.Render("No profiles yet — press a to create one.") + "\n"
	}
	nameWidth, providerWidth, modelWidth := 12, 8, len("MODEL")
	models := make([]string, len(m.profiles))
	for index, profile := range m.profiles {
		models[index] = profileModel(profile)
		nameWidth = max(nameWidth, lipgloss.Width(profile.Name))
		providerWidth = max(providerWidth, lipgloss.Width(profile.Provider))
		modelWidth = max(modelWidth, lipgloss.Width(models[index]))
	}
	nameWidth, providerWidth, modelWidth = min(nameWidth, 26), min(providerWidth, 12), min(modelWidth, 22)

	// Notes and the full launch command live in the selected-profile panel. The
	// table keeps the scan-friendly fields, shrinking columns in usefulness order
	// when the terminal is narrow.
	total := 2 + nameWidth + 2 + providerWidth + 2 + authWidth + 2 + modelWidth + 2 + usageWidth + 2 + usageWidth
	showProvider := total <= width
	if !showProvider {
		total -= providerWidth + 2
	}
	showAuth := true
	if total > width {
		showAuth = false
		total -= authWidth + 2
	}
	for total > width && nameWidth > 8 {
		nameWidth--
		total--
	}
	for total > width && modelWidth > len("MODEL") {
		modelWidth--
		total--
	}
	for showProvider && total > width && providerWidth > 4 {
		providerWidth--
		total--
	}
	for total > width && nameWidth > 1 {
		nameWidth--
		total--
	}

	header := pad(truncate("NAME", nameWidth), nameWidth)
	if showProvider {
		header += "  " + pad(truncate("PROVIDER", providerWidth), providerWidth)
	}
	if showAuth {
		header += "  " + pad("AUTH", authWidth)
	}
	header += "  " + pad(truncate("MODEL", modelWidth), modelWidth) +
		"  " + pad("5H", usageWidth) +
		"  " + pad("7D", usageWidth)
	rows := []string{"  " + columnHeaderStyle.Render(header)}
	for index, profile := range m.profiles {
		bar, name := "  ", nameStyle.Render(pad(truncate(profile.Name, nameWidth), nameWidth))
		if index == m.cursor {
			bar = cursorBarStyle.Render("▌ ")
			name = nameActiveStyle.Render(pad(truncate(profile.Name, nameWidth), nameWidth))
		}
		left := bar + name
		if showProvider {
			provider := providerStyle(profile.Provider).Render(pad(truncate(profile.Provider, providerWidth), providerWidth))
			left += "  " + provider
		}
		if showAuth {
			left += "  " + authCell(profile)
		}
		left += "  " + modelCell(models[index], modelWidth) + "  " + m.usageCell(profile, fiveHourWindow) + "  " + m.usageCell(profile, weeklyWindow)
		badge := ""
		if running := profileRunningCount(profile); running > 0 {
			badgeText := "▶ running"
			if running > 1 {
				badgeText = fmt.Sprintf("▶ %d running", running)
			}
			badge = liveStyle.Render(badgeText)
			if lipgloss.Width(left)+1+lipgloss.Width(badge) > width {
				badge = liveStyle.Render("▶")
			}
		}
		rows = append(rows, spread(left, badge, width))
	}
	return strings.Join(rows, "\n") + "\n"
}

const usageWidth = 4

type usageWindowKind int

const (
	fiveHourWindow usageWindowKind = iota
	weeklyWindow
)

func (m tuiModel) usageCell(profile Profile, kind usageWindowKind) string {
	if m.usage == nil {
		return unknownStyle.Render(pad("…", usageWidth))
	}
	usage, exists := m.usage[profile.Name]
	if !exists {
		return unknownStyle.Render(pad("—", usageWidth))
	}
	window := usage.FiveHour
	if kind == weeklyWindow {
		window = usage.Weekly
	}
	if !window.Known {
		return unknownStyle.Render(pad("—", usageWidth))
	}
	value := pad(fmt.Sprintf("%d%%", window.Percent), usageWidth)
	switch {
	case window.Percent <= 10:
		return usageCriticalStyle.Render(value)
	case window.Percent <= 25:
		return usageWarningStyle.Render(value)
	default:
		return usageGoodStyle.Render(value)
	}
}

func (m tuiModel) selectedProfileView(width int) string {
	if len(m.profiles) == 0 || m.cursor >= len(m.profiles) {
		return ""
	}
	profile := m.profiles[m.cursor]
	launch := formatArguments(append([]string{profile.Command}, profile.DefaultArgs...))
	defaultArgs := formatArguments(profile.DefaultArgs)
	if defaultArgs == "" {
		defaultArgs = "—"
	}
	notes := profile.Notes
	if notes == "" {
		notes = "—"
	}
	workingDir := m.workingDir
	if workingDir == "" {
		workingDir = "—"
	}
	valueWidth := max(width-18, 1)
	lines := []string{
		fieldLabelStyle.Render(pad("Launch", 12)) + " " + fieldValueStyle.Render(truncate(launch, valueWidth)),
		fieldLabelStyle.Render(pad("Folder", 12)) + " " + fieldValueStyle.Render(truncate(workingDir, valueWidth)),
		fieldLabelStyle.Render(pad("Default args", 12)) + " " + fieldValueStyle.Render(truncate(defaultArgs, valueWidth)),
		fieldLabelStyle.Render(pad("Notes", 12)) + " " + fieldValueStyle.Render(truncate(notes, valueWidth)),
	}
	return "\n" + sectionTitle.Render("Selected") + "\n" + panelStyle.Width(width-2).Render(strings.Join(lines, "\n")) + "\n"
}

func modelCell(model string, width int) string {
	if model == "" {
		return unknownStyle.Render(pad("—", width))
	}
	return modelStyle.Render(pad(truncate(model, width), width))
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

func (m tuiModel) formView(width int) string {
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
	lines := make([]string, 0, len(fields)+1)
	for index, field := range fields {
		label, value := fieldLabelStyle.Render(pad(field.label, 12)), fieldValueStyle.Render(field.value)
		if index == m.form.field {
			label = fieldLabelActive.Render(pad(field.label, 12))
			value += cursorStyle.Render(" ")
		}
		lines = append(lines, label+" "+value)
	}
	if m.form.field == 1 {
		lines = append(lines, "", hintStyle.Render("known providers: codex · claude · opencode · deepseek"))
	} else if m.form.field == 3 {
		lines = append(lines, "", hintStyle.Render("shell-style quotes are supported; arguments apply only when running"))
	}
	body := panelStyle.Width(width - 2).Render(strings.Join(lines, "\n"))
	return sectionTitle.Render(heading) + "\n" + body + "\n"
}

func (m tuiModel) folderView(width int) string {
	value := fieldValueStyle.Render(m.folderPath) + cursorStyle.Render(" ")
	content := lipgloss.JoinVertical(lipgloss.Left,
		fieldLabelActive.Render(pad("Folder", 12))+" "+value,
		"",
		hintStyle.Render("Relative paths use the current launch folder. ~ is supported."),
	)
	return sectionTitle.Render("Change launch folder") + "\n" + panelStyle.Width(width-2).Render(content) + "\n"
}

func (m tuiModel) confirmView(width int) string {
	if m.cursor >= len(m.profiles) {
		return ""
	}
	profile := m.profiles[m.cursor]
	var body string
	if m.mode == tuiHijack {
		lines := append([]string{sectionTitle.Render("Open a running " + profile.Name + " session here")},
			m.instanceRows(width, fieldValueStyle)...)
		lines = append(lines, "", hintStyle.Render("The original instance keeps running; this opens its conversation."))
		return "\n" + panelStyle.Width(width-2).Render(lipgloss.JoinVertical(lipgloss.Left, lines...)) + "\n"
	}
	if m.mode == tuiConfirmKill {
		lines := append([]string{dangerTextStyle.Render("Stop a " + profile.Name + " instance?")},
			m.instanceRows(width, dangerTextStyle)...)
		lines = append(lines, "", confirmBodyStyle.Render("Enter stops the selected instance. a or y stops them all."))
		body = dangerPanelStyle.Width(width - 2).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
		return "\n" + body + "\n"
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		dangerTextStyle.Render("Delete "+profile.Name+"?"),
		confirmBodyStyle.Render("Its isolated state directory and stored credentials"),
		confirmBodyStyle.Render("are removed. This cannot be undone."),
	)
	body = dangerPanelStyle.Width(width - 2).Render(content)
	return "\n" + body + "\n"
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

func (m tuiModel) paramsView(width int) string {
	profile := "profile"
	if m.cursor < len(m.profiles) {
		profile = m.profiles[m.cursor].Name
	}
	value := fieldValueStyle.Render(m.params) + cursorStyle.Render(" ")
	content := lipgloss.JoinVertical(lipgloss.Left,
		fieldLabelActive.Render(pad("Arguments", 12))+" "+value,
		"",
		hintStyle.Render("Added after the profile default arguments. Shell-style quotes are supported."),
	)
	return sectionTitle.Render("Run "+profile+" with arguments") + "\n" + panelStyle.Width(width-2).Render(content) + "\n"
}

// helpView wraps at the content width so the key list reflows instead of
// running past the edge of a narrow terminal.
func (m tuiModel) helpView(width int) string {
	return lipgloss.NewStyle().Width(width).Render(m.helpEntries())
}

func (m tuiModel) helpEntries() string {
	switch m.mode {
	case tuiForm:
		return renderHelp([]helpEntry{
			{"tab/↵", "next field"},
			{"↵", "save on last field"},
			{"ctrl-u", "clear"},
			{"esc", "cancel"},
		})
	case tuiFolder:
		return renderHelp([]helpEntry{{"↵", "set folder"}, {"ctrl-u", "clear"}, {"esc", "cancel"}})
	case tuiConfirmDelete:
		return renderHelp([]helpEntry{{"y", "delete"}, {"n/esc", "keep"}})
	case tuiConfirmKill:
		return renderHelp([]helpEntry{{"↑↓/jk", "choose instance"}, {"↵", "stop selected"}, {"a/y", "stop all"}, {"n/esc", "keep running"}})
	case tuiHijack:
		return renderHelp([]helpEntry{{"↑↓/jk", "choose instance"}, {"↵", "open here"}, {"esc", "cancel"}})
	case tuiParams:
		return renderHelp([]helpEntry{{"↵", "run"}, {"ctrl-u", "clear"}, {"esc", "cancel"}})
	default:
		if len(m.profiles) == 0 {
			return renderHelp([]helpEntry{{"a", "add profile"}, {"q", "quit"}})
		}
		return renderHelp([]helpEntry{
			{"↵", "run"},
			{"p", "run with args"},
			{"R", "resume"},
			{"h", "hijack"},
			{"l", "login"},
			{"u", "update CLI"},
			{"c", "change folder"},
			{"r", "refresh usage"},
			{"a", "add"},
			{"e", "edit"},
			{"x", "delete"},
			{"K", "stop"},
			{"q", "quit"},
		})
	}
}

// updateView is shown only when there is something to act on. A check that
// could not run says so through the status line when the user asks for it with
// r, rather than nagging on every launch.
func (m tuiModel) updateView(width int) string {
	if !m.update.available() {
		return ""
	}
	return "\n" + updateStyle.Width(width).Render("↑ "+m.update.message())
}

func (m tuiModel) statusView(width int) string {
	if m.status == "" {
		return ""
	}
	icon, style := "•", statusInfoStyle
	switch m.statusKind {
	case statusOK:
		icon, style = "✓", statusOKStyle
	case statusErr:
		icon, style = "✗", statusErrStyle
	}
	return "\n" + style.Width(width).Render(icon+" "+m.status)
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
