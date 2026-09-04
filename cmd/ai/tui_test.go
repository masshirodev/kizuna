package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testProfiles() []Profile {
	return []Profile{
		{Name: "claude-personal", Provider: "claude", Command: "claude"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
	}
}

// wideModel is a cockpit with room for all three columns, which is what most
// view assertions are about. Tests that care about a specific terminal size set
// width and height themselves.
func wideModel(profiles []Profile) tuiModel {
	return tuiModel{profiles: profiles, width: 140, height: 32}
}

func TestViewListsProfilesWithHelp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles := testProfiles()
	profiles[1].DefaultArgs = []string{"--search"}
	profiles[1].Notes = "work subscription"
	m := wideModel(profiles)
	m.cursor = 1
	m.usage = map[string]usageRemaining{"codex-work": {
		FiveHour: usageWindow{Percent: 56, Known: true},
		Weekly:   usageWindow{Percent: 73, Known: true},
	}}
	view := m.View()
	for _, want := range []string{"PROFILES", "2 profiles", "claude-personal", "codex-work", "5H", "7D", "56%", "73%", "launch", "codex --search", "work subscription", "run", "login", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view is missing %q:\n%s", want, view)
		}
	}
	if !strings.Contains(view, "▌ codex-work") {
		t.Fatalf("selected profile is not marked:\n%s", view)
	}
}

func TestViewShowsAuthenticationColumn(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	credentials := filepath.Join(root, appName, "profiles", "claude-personal", "claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(credentials), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(deepSeekKeyEnv, "sk-test")
	profiles := append(testProfiles(), Profile{Name: "deepseek", Provider: "deepseek", Command: "opencode"})
	view := wideModel(profiles).View()
	if !strings.Contains(view, "AUTH") {
		t.Fatalf("missing AUTH column header:\n%s", view)
	}
	if strings.Count(view, "● yes") != 1 || strings.Count(view, "○ no") != 1 || strings.Count(view, "● key") != 1 {
		t.Fatalf("auth column does not distinguish the three auth sources:\n%s", view)
	}
	if strings.Contains(view, "sk-test") {
		t.Fatalf("the API key value leaked into the view:\n%s", view)
	}
}

func TestViewShowsSelectedModelWhenKnown(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"model":"opus"}`, "claude-personal", "claude", "settings.json")
	view := wideModel(testProfiles()).View()
	if !strings.Contains(view, "model") || !strings.Contains(view, "opus") {
		t.Fatalf("selected profile does not name its model:\n%s", view)
	}
	if !strings.Contains(view, "—") {
		t.Fatalf("a profile with no discoverable model should show a dash:\n%s", view)
	}
}

// The selected-profile panel owns launch details, so a narrow table can keep
// both model and remaining usage visible without wrapping rows.
func TestViewKeepsModelAndUsageWhenNarrow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"model":"opus"}`, "claude-personal", "claude", "settings.json")
	usage := map[string]usageRemaining{"claude-personal": {
		FiveHour: usageWindow{Percent: 56, Known: true},
		Weekly:   usageWindow{Percent: 97, Known: true},
	}}
	narrow := tuiModel{profiles: testProfiles(), width: 46, height: 24, usage: usage}.View()
	if !strings.Contains(narrow, "5H") || !strings.Contains(narrow, "7D") || !strings.Contains(narrow, "56%") || !strings.Contains(narrow, "97%") {
		t.Fatalf("narrow view dropped the quota figures:\n%s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if lipgloss.Width(line) != 46 {
			t.Fatalf("line is %d columns wide, want 46:\n%q", lipgloss.Width(line), line)
		}
	}
}

func TestViewMarksRunningProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profiles := testProfiles()
	dir := filepath.Join(root, appName, "profiles", profiles[1].Name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := wideModel(profiles)
	m.cursor = 1
	loaded, _ := m.Update(m.loadCockpitCmd()().(cockpitLoadedMsg))
	view := loaded.(tuiModel).View()
	if !strings.Contains(view, "▶ running") {
		t.Fatalf("the locked profile is not marked as running:\n%s", view)
	}
	if !strings.Contains(view, "RUNNING · 1 TOTAL") {
		t.Fatalf("the live panel does not account for the running instance:\n%s", view)
	}
	if !strings.Contains(view, "1 instance") {
		t.Fatalf("the title bar does not carry the instance count:\n%s", view)
	}
}

func TestViewCountsConcurrentProfileInstances(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	workdir := filepath.Join(root, appName, "profiles", profile.Name)
	for _, name := range []string{"run-one", "run-two"} {
		dir := filepath.Join(workdir, instancesDirectory, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := wideModel([]Profile{profile})
	loaded, _ := m.Update(m.loadCockpitCmd()().(cockpitLoadedMsg))
	view := loaded.(tuiModel).View()
	if !strings.Contains(view, "▶ 2 running") {
		t.Fatalf("concurrent instance count missing:\n%s", view)
	}
	if !strings.Contains(view, "RUNNING · 2 TOTAL") {
		t.Fatalf("the live panel lists neither instance:\n%s", view)
	}
}

func TestEditRefusesRunningProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "claude-personal", Provider: "claude", Command: "claude"}
	dir := filepath.Join(root, appName, "profiles", profile.Name, instancesDirectory, "run-one")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte("1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{profiles: []Profile{profile}, mode: tuiList}
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	got := updated.(tuiModel)
	if got.mode != tuiList || !strings.Contains(got.status, "cannot edit") {
		t.Fatalf("running profile entered edit mode: mode=%v status=%q", got.mode, got.status)
	}
}

func TestKillPickerListsEachRunningInstance(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	workdir := filepath.Join(root, appName, "profiles", profile.Name)
	for _, name := range []string{"run-one", "run-two"} {
		dir := filepath.Join(workdir, instancesDirectory, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".active.lock"), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	m := tuiModel{profiles: []Profile{profile}, width: 80}
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("K")})
	got := updated.(tuiModel)
	if got.mode != tuiConfirmKill || len(got.instances) != 2 {
		t.Fatalf("kill picker = mode %v with %d instances", got.mode, len(got.instances))
	}
	view := got.View()
	for _, want := range []string{"Instance 1 (PID 1)", "Instance 2 (PID 1)", "stops them all"} {
		if !strings.Contains(view, want) {
			t.Fatalf("kill picker is missing %q:\n%s", want, view)
		}
	}
}

func TestTerminateProfileLockStopsOnlySelectedInstance(t *testing.T) {
	first := exec.Command("sleep", "30")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	second := exec.Command("sleep", "30")
	if err := second.Start(); err != nil {
		_ = first.Process.Kill()
		_ = first.Wait()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Process.Kill()
		_ = first.Wait()
		_ = second.Process.Kill()
		_ = second.Wait()
	})

	dir := t.TempDir()
	firstDir := filepath.Join(dir, instancesDirectory, "run-one")
	secondDir := filepath.Join(dir, instancesDirectory, "run-two")
	for lockDir, command := range map[string]*exec.Cmd{firstDir: first, secondDir: second} {
		if err := os.MkdirAll(lockDir, 0700); err != nil {
			t.Fatal(err)
		}
		lock := fmt.Sprintf("%d\n%d\n", os.Getpid(), command.Process.Pid)
		if err := os.WriteFile(filepath.Join(lockDir, ".active.lock"), []byte(lock), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := terminateProfileLock(firstDir); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err == nil {
		t.Fatal("selected instance did not stop")
	}
	if err := second.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unselected instance was stopped: %v", err)
	}
}

func TestChangeFolderUsesRelativeDirectory(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "project")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{profiles: testProfiles(), workingDir: root}
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	got := updated.(tuiModel)
	if got.mode != tuiFolder || got.folderPath != root {
		t.Fatalf("change folder form = mode %v, value %q", got.mode, got.folderPath)
	}
	got.folderPath = "project"
	updated, _ = got.updateFolder(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if got.mode != tuiList || got.workingDir != folder {
		t.Fatalf("launch folder = mode %v, folder %q; want %q", got.mode, got.workingDir, folder)
	}
}

func TestViewEmptyStateInvitesFirstProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	view := wideModel(nil).View()
	if !strings.Contains(view, "No profiles yet") || !strings.Contains(view, "add profile") {
		t.Fatalf("unhelpful empty state:\n%s", view)
	}
	if strings.Contains(view, "delete") {
		t.Fatalf("empty state offers keys that do nothing:\n%s", view)
	}
}

func TestViewFormShowsFieldsAndError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.mode = tuiForm
	m.form = profileForm{name: "codex-work", provider: "codex", command: "codex", defaultArgs: "--search", notes: "work", field: 1, original: "codex-work"}
	m.setStatus(statusErr, "already exists")
	view := m.View()
	for _, want := range []string{"Edit codex-work", "Name", "Provider", "Command", "Default args", "Notes", "--search", "work", "known providers", "✗ already exists"} {
		if !strings.Contains(view, want) {
			t.Fatalf("form view is missing %q:\n%s", want, view)
		}
	}
}

func TestSaveFormPersistsDefaultArgsAndNotes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{}); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{configPath: path}
	m.form = profileForm{
		name:        "codex-work",
		provider:    "codex",
		command:     "codex",
		defaultArgs: `--model "gpt 5" --search`,
		notes:       "  weekly allowance  ",
		isNew:       true,
	}
	if err := m.saveForm(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := findProfile(cfg, "codex-work")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(profile.DefaultArgs, "|") != "--model|gpt 5|--search" || profile.Notes != "weekly allowance" {
		t.Fatalf("saved profile = %+v", profile)
	}
}

func TestSaveFormRejectsNameCollidingWithApp(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{
		Profiles: []Profile{{Name: "gemini", Provider: "antigravity", Command: "gemini"}},
		Apps:     []App{{Name: "shiori", Members: []string{"gemini"}, Active: "gemini"}},
	}); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{configPath: path}
	m.form = profileForm{name: "shiori", provider: "codex", command: "codex", isNew: true}
	if err := m.saveForm(); err == nil {
		t.Fatal("expected saveForm to reject a profile name that collides with an existing app")
	}
	m.form = profileForm{name: "apps", provider: "codex", command: "codex", isNew: true}
	if err := m.saveForm(); err == nil {
		t.Fatal("expected saveForm to reject the reserved name \"apps\"")
	}
}

func TestSaveFormRefusesToRenameAnAppMember(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ai", "profiles", "gemini"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{
		Profiles: []Profile{{Name: "gemini", Provider: "antigravity", Command: "gemini"}},
		Apps:     []App{{Name: "shiori", Members: []string{"gemini"}, Active: "gemini"}},
	}); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{configPath: path}
	m.form = profileForm{name: "gemini-renamed", original: "gemini", provider: "antigravity", command: "gemini"}
	if err := m.saveForm(); err == nil {
		t.Fatal("expected saveForm to refuse renaming a profile used by an app")
	}
}

func TestFormAcceptsSpacesInDefaultArgsAndNotes(t *testing.T) {
	for _, field := range []int{3, 4} {
		m := tuiModel{mode: tuiForm, form: profileForm{field: field}}
		updated, _ := m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
		updated, _ = updated.(tuiModel).updateForm(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
		updated, _ = updated.(tuiModel).updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("second")})
		result := updated.(tuiModel)
		got := result.formValue()
		if got != "first second" {
			t.Fatalf("field %d value = %q, want a preserved space", field, got)
		}
	}
}

func TestViewConfirmDeleteNamesProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor, m.mode = 1, tuiConfirmDelete
	view := m.View()
	if !strings.Contains(view, "Delete codex-work?") || !strings.Contains(view, "cannot be undone") {
		t.Fatalf("confirmation is unclear:\n%s", view)
	}
	if !strings.Contains(view, "keep") {
		t.Fatalf("confirmation lacks a cancel hint:\n%s", view)
	}
}

func TestTitleBarNamesSelectedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	frame := frameLayout(m.width, m.height)
	if bar := m.topBarView(frame); !strings.Contains(bar, "codex-work") || !strings.Contains(bar, "2 profiles") {
		t.Fatalf("title bar does not name the selected profile:\n%s", bar)
	}
	m.cursor = 0
	if bar := m.topBarView(frame); !strings.Contains(bar, "claude-personal") {
		t.Fatalf("title bar did not follow the cursor:\n%s", bar)
	}
}

// The count is the field that must survive a narrow terminal. The launch folder
// and the update notice are dropped first, in that order, rather than being
// clipped into something unreadable.
func TestTitleBarDropsDetailBeforeTheCount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 46, height: 24, cursor: 1}
	m.workingDir = "/home/someone/a/very/long/working/directory/name"
	m.update = updateStatus{Known: true, Behind: 3}
	bar := m.topBarView(frameLayout(m.width, m.height))
	if !strings.Contains(bar, "2 profiles") {
		t.Fatalf("title bar lost the count:\n%s", bar)
	}
	if strings.Contains(bar, "working/directory") {
		t.Fatalf("title bar kept a folder it had no room for:\n%s", bar)
	}
	if lipgloss.Width(bar) > 46 {
		t.Fatalf("title bar overflows the terminal (%d):\n%s", lipgloss.Width(bar), bar)
	}
}

func TestTitleBarOmitsNameWithoutProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(nil)
	if bar := m.topBarView(frameLayout(m.width, m.height)); !strings.Contains(bar, "no profiles") {
		t.Fatalf("empty title bar should still carry the count:\n%s", bar)
	}
}

func TestViewOffersUpdateOnlyWhenOneIsAvailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())

	m.update = updateStatus{Known: true, Behind: 3}
	if view := m.View(); !strings.Contains(view, "↑ 3 commits behind main") {
		t.Fatalf("available update is not offered:\n%s", view)
	}
	m.update = updateStatus{Known: true}
	if view := m.View(); strings.Contains(view, "behind main") {
		t.Fatalf("an up-to-date build was nagged:\n%s", view)
	}
	// A check that could not run answers through the status line when asked,
	// never by claiming an update on the main screen.
	m.update = updateStatus{Reason: "github could not be reached"}
	if view := m.View(); strings.Contains(view, "↑") {
		t.Fatalf("a failed check was rendered as an update:\n%s", view)
	}
}

// The check runs as the main screen appears, so an available update is visible
// without the user asking for it.
func TestInitChecksForUpdates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	m := tuiModel{profiles: testProfiles()}
	if m.Init() == nil {
		t.Fatal("Init issued no commands")
	}
	updated, _ := m.Update(updateCheckedMsg{status: updateStatus{Known: true, Behind: 2}})
	if got := updated.(tuiModel).update.Behind; got != 2 {
		t.Fatalf("update status was not kept: %d", got)
	}
	// Only a check the user asked for speaks through the status line.
	if status := updated.(tuiModel).status; status != "" {
		t.Fatalf("the startup check wrote to the status line: %q", status)
	}
	forced, _ := m.Update(updateCheckedMsg{status: updateStatus{Known: true, Behind: 2}, forced: true})
	if !strings.Contains(forced.(tuiModel).status, "2 commits behind") {
		t.Fatalf("a forced check said nothing: %q", forced.(tuiModel).status)
	}
}

// The cockpit owns the alt screen, so a frame that is one line short or one
// column wide leaves the previous frame showing through underneath it.
func TestViewFillsTheTerminalExactly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, size := range [][2]int{{149, 34}, {118, 30}, {100, 26}, {80, 24}, {70, 22}, {40, 12}, {20, 8}, {1, 5}} {
		width, height := size[0], size[1]
		m := tuiModel{profiles: testProfiles(), width: width, height: height, cursor: 1}
		lines := strings.Split(m.View(), "\n")
		if len(lines) != height {
			t.Fatalf("%dx%d rendered %d lines", width, height, len(lines))
		}
		for index, line := range lines {
			if lipgloss.Width(line) != width {
				t.Fatalf("%dx%d line %d is %d columns wide:\n%q", width, height, index, lipgloss.Width(line), line)
			}
		}
	}
}

// A terminal that has not reported its size yet still has to draw something.
func TestViewAssumesASizeBeforeTheFirstResize(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	view := tuiModel{profiles: testProfiles(), cursor: 1}.View()
	if !strings.Contains(view, "codex-work") {
		t.Fatalf("unsized view dropped content:\n%s", view)
	}
	if lines := strings.Split(view, "\n"); len(lines) != assumedHeight {
		t.Fatalf("unsized view rendered %d lines, want %d", len(lines), assumedHeight)
	}
}

func TestTruncateKeepsWidthBudget(t *testing.T) {
	if got := truncate("codex-personal", 6); got != "codex…" {
		t.Fatalf("truncate = %q, want %q", got, "codex…")
	}
	if got := truncate("codex", 8); got != "codex" {
		t.Fatalf("truncate shortened a fitting value: %q", got)
	}
	if got := pad("codex", 8); got != "codex   " {
		t.Fatalf("pad = %q", got)
	}
}

func TestHijackPickerNamesSessionAndFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{
		profiles: testProfiles(),
		width:    140,
		height:   32,
		cursor:   1,
		mode:     tuiHijack,
		instances: []profileInstance{
			{pid: 11, folder: "/work/lattice", session: instanceSession{id: "aaa", title: "Edit contacts"}},
			{pid: 12, folder: "/work/hub", session: instanceSession{id: "bbb", title: "Server status card"}},
		},
	}
	view := m.View()
	for _, want := range []string{"Open a running codex-work session here", "Instance 1 (PID 11)", "Edit contacts", "/work/lattice", "Instance 2 (PID 12)", "Server status card", "open here"} {
		if !strings.Contains(view, want) {
			t.Fatalf("hijack picker is missing %q:\n%s", want, view)
		}
	}
}

func TestKillPickerNamesSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{
		profiles:  testProfiles(),
		width:     140,
		height:    32,
		cursor:    1,
		mode:      tuiConfirmKill,
		instances: []profileInstance{{pid: 11, folder: "/work/lattice", session: instanceSession{id: "aaa", title: "Edit contacts"}}},
	}
	view := m.View()
	for _, want := range []string{"Stop a codex-work instance?", "Instance 1 (PID 11)", "Edit contacts", "/work/lattice"} {
		if !strings.Contains(view, want) {
			t.Fatalf("kill picker is missing %q:\n%s", want, view)
		}
	}
}

func TestInstanceTitleDistinguishesPendingFromUnknown(t *testing.T) {
	pending := tuiModel{describing: true}
	if got := pending.instanceTitle(profileInstance{}); got != "…" {
		t.Fatalf("pending title = %q, want a placeholder while the lookup runs", got)
	}
	settled := tuiModel{}
	if got := settled.instanceTitle(profileInstance{}); got != "—" {
		t.Fatalf("unknown title = %q", got)
	}
	if got := settled.instanceTitle(profileInstance{session: instanceSession{id: "aaa"}}); got != "untitled session" {
		t.Fatalf("nameless session title = %q", got)
	}
}

func TestHijackPickerRefusesWhenNothingIsRunning(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 80, cursor: 1}
	updated, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	got := updated.(tuiModel)
	if got.mode != tuiList || cmd != nil {
		t.Fatalf("hijack opened a picker with no instances: mode %v", got.mode)
	}
	if !strings.Contains(got.status, "not running") {
		t.Fatalf("status = %q, want an explanation", got.status)
	}
}

func TestDescribedInstancesReachTheOpenPicker(t *testing.T) {
	m := tuiModel{
		profiles:   testProfiles(),
		cursor:     1,
		mode:       tuiHijack,
		describing: true,
		instances:  []profileInstance{{pid: 11}},
	}
	described := []profileInstance{{pid: 11, session: instanceSession{id: "aaa", title: "Edit contacts"}}}
	updated, _ := m.Update(instancesDescribedMsg{profile: "codex-work", instances: described})
	got := updated.(tuiModel)
	if got.describing || got.instances[0].session.title != "Edit contacts" {
		t.Fatalf("picker did not take the described sessions: %+v", got.instances)
	}

	stale, _ := m.Update(instancesDescribedMsg{profile: "claude-personal", instances: described})
	if stale.(tuiModel).instances[0].session.title != "" {
		t.Fatalf("a lookup for another profile overwrote the picker")
	}
}

func TestParamsPromptCollectsArgumentsAndReportsQuoteErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	got := updated.(tuiModel)
	if got.mode != tuiParams {
		t.Fatalf("mode = %v, want the argument prompt", got.mode)
	}
	updated, _ = got.updateParams(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("--model")})
	updated, _ = updated.(tuiModel).updateParams(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	updated, _ = updated.(tuiModel).updateParams(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`"gpt 5`)})
	got = updated.(tuiModel)
	if got.params != `--model "gpt 5` {
		t.Fatalf("typed arguments = %q", got.params)
	}
	if view := got.View(); !strings.Contains(view, "Run codex-work with arguments") || !strings.Contains(view, "--model") {
		t.Fatalf("argument prompt does not show what was typed:\n%s", view)
	}

	updated, cmd := got.updateParams(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if cmd != nil || got.mode != tuiParams || !strings.Contains(got.status, "unclosed quote") {
		t.Fatalf("unclosed quote was accepted: mode %v, status %q", got.mode, got.status)
	}
}

// press sends one key to the list and returns the model it produced.
func press(t *testing.T, m tuiModel, key string) tuiModel {
	t.Helper()
	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return updated.(tuiModel)
}

// The bottom bar drops entries from the end to fit, and the keys it drops are
// exactly the ones a new user has not learned yet. The pane is where they are.
func TestHelpPaneListsTheKeysTheBottomBarCannotFit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := press(t, wideModel(testProfiles()), "?")
	if m.mode != tuiHelp {
		t.Fatalf("mode = %v, want the help pane", m.mode)
	}
	view := m.View()
	for _, want := range []string{"Keys", "LAUNCH", "PROFILES", "PROVIDER CLI", "AI-SESSION",
		"install the CLI", "update ai-session itself", "filter by name or provider", "any key closes"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help pane is missing %q:\n%s", want, view)
		}
	}
}

// Every key the pane advertises has to be one updateList actually answers,
// otherwise the pane is documentation that drifts from the program.
func TestHelpPaneOnlyAdvertisesKeysTheListHandles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	handled := map[string]bool{"↵": true, "↑↓ jk": true}
	for _, column := range helpSections() {
		for _, section := range column {
			for _, entry := range section.entries {
				if handled[entry.key] {
					continue
				}
				if len([]rune(entry.key)) != 1 {
					t.Fatalf("help pane lists an unexplained key %q", entry.key)
				}
				base := wideModel(testProfiles())
				updated, cmd := base.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(entry.key)})
				got := updated.(tuiModel)
				// Answering means one of: opening something, saying why not, or
				// handing back work to run.
				if cmd == nil && got.mode == base.mode && got.status == "" && got.searching == base.searching {
					t.Errorf("key %q (%s) does nothing in the list", entry.key, entry.desc)
				}
			}
		}
	}
}

func TestHelpPaneClosesOnAnyKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := press(t, wideModel(testProfiles()), "?")
	updated, _ := m.updateHelp(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	if updated.(tuiModel).mode != tuiList {
		t.Fatal("the help pane survived a keypress")
	}
}

// Agreeing to install a CLI is not agreement to run whatever a URL serves, so
// the box has to show the command before it asks.
func TestInstallPromptNamesTheCommandItWillRun(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	m = press(t, m, "i")
	if m.mode != tuiConfirmInstall {
		t.Fatalf("mode = %v, want the install confirmation", m.mode)
	}
	view := m.View()
	for _, want := range []string{"Install the codex CLI", "curl -fsSL https://chatgpt.com/codex/install.sh | sh", "codex on PATH"} {
		if !strings.Contains(view, want) {
			t.Fatalf("install prompt is missing %q:\n%s", want, view)
		}
	}
	updated, cmd := m.updateInstall(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if got := updated.(tuiModel); got.mode != tuiList || cmd != nil {
		t.Fatalf("declining the install did not return to the list: mode %v", got.mode)
	}
}

func TestInstallPromptRefusesAProviderWithNoInstaller(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel([]Profile{{Name: "local", Provider: "homegrown", Command: "llm"}})
	m = press(t, m, "i")
	if m.mode != tuiList || !strings.Contains(m.status, "no known installer") {
		t.Fatalf("mode = %v, status = %q; want a refusal that stays on the list", m.mode, m.status)
	}
}

func TestSelfUpdatePromptNamesTheCheckoutAndItsSteps(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	checkout := fakeCheckout(t)
	t.Setenv(sourceDirEnv, checkout)

	m := wideModel(testProfiles())
	m.update = updateStatus{Known: true, Behind: 2}
	m = press(t, m, "U")
	if m.mode != tuiConfirmSelfUpdate || m.source != checkout {
		t.Fatalf("mode = %v, source = %q", m.mode, m.source)
	}
	view := m.View()
	for _, want := range []string{"Update ai-session", "git pull --ff-only", "2 commits behind main", "reopens itself"} {
		if !strings.Contains(view, want) {
			t.Fatalf("self-update prompt is missing %q:\n%s", want, view)
		}
	}
}

// With nothing overridden, U previews the managed clone's path without
// requiring it to exist yet — ensureRepo's clone-if-missing runs only once
// the update is actually confirmed, not while just showing the checkout.
func TestSelfUpdateKeyPreviewsTheManagedCloneWithoutRequiringItToExist(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	m := press(t, wideModel(testProfiles()), "U")
	if m.mode != tuiConfirmSelfUpdate {
		t.Fatalf("mode = %v, status = %q; want the confirmation to open", m.mode, m.status)
	}
	if want := filepath.Join(root, "ai", "repo"); m.source != want {
		t.Fatalf("m.source = %q, want the managed clone path %q", m.source, want)
	}
}

// AI_SOURCE_DIR is the one case previewing the checkout can still fail: an
// override pointed at something that is not actually an ai-session checkout.
func TestSelfUpdateKeyExplainsAnInvalidSourceOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(sourceDirEnv, t.TempDir())
	m := press(t, wideModel(testProfiles()), "U")
	if m.mode != tuiList || !strings.Contains(m.status, "not an ai-session checkout") {
		t.Fatalf("mode = %v, status = %q; want the bad override named", m.mode, m.status)
	}
}

// A profile can be configured and logged in and still fail at launch because
// the CLI was never installed, and exec's own message names neither the
// profile nor the way out.
func TestDetailPanelSaysWhetherTheProviderCLIIsInstalled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// A short directory, because the detail column truncates the path it shows
	// and t.TempDir names itself after the test.
	binDir, err := os.MkdirTemp("", "aibin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(binDir) })
	installed := filepath.Join(binDir, "claude")
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	scopePATH(t, binDir)

	m := wideModel(testProfiles())
	if view := m.View(); !strings.Contains(view, installed) {
		t.Fatalf("detail panel does not say where the CLI is:\n%s", view)
	}
	m.cursor = 1
	if view := m.View(); !strings.Contains(view, "not installed — press i") {
		t.Fatalf("detail panel does not offer to install a missing CLI:\n%s", view)
	}
}

// A key list clipped to fit is missing exactly the keys nobody has learned yet,
// so a terminal too narrow for two columns gets one.
func TestHelpPaneFoldsToOneColumnRatherThanClipping(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 70, height: 40}
	view := press(t, m, "?").View()
	for _, want := range []string{"filter by name or provider", "refresh quotas and updates", "stop a running instance"} {
		if !strings.Contains(view, want) {
			t.Fatalf("narrow help pane lost %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "select    l") {
		t.Fatalf("narrow help pane kept two columns:\n%s", view)
	}
}

// recentTestSessions is a profile's history as the panel would have read it.
func recentTestSessions(folder string) []recordedSession {
	when := time.Date(2026, 9, 4, 11, 30, 0, 0, time.Local)
	return []recordedSession{
		{session: instanceSession{id: "aaa", title: "Edit contacts"}, folder: folder, when: when},
		{session: instanceSession{id: "bbb", title: "Server status card"}, folder: "/gone/hub", when: when.Add(-time.Hour)},
	}
}

func TestResumePickerOffersTheRecordedSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	m.recent = recentTestSessions(t.TempDir())
	m = press(t, m, "R")
	if m.mode != tuiRecent {
		t.Fatalf("mode = %v, want the resume picker", m.mode)
	}
	view := m.View()
	for _, want := range []string{"Resume a codex-work session", "Edit contacts", "Server status card", "/gone/hub", "resume it there"} {
		if !strings.Contains(view, want) {
			t.Fatalf("resume picker is missing %q:\n%s", want, view)
		}
	}
}

// With nothing recorded — an account that has not run yet, or a provider whose
// transcripts are not read — R has to stay the key that resumes rather than
// opening an empty box.
func TestResumeFallsBackToTheProviderFlowWithNothingRecorded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	updated, cmd := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	got := updated.(tuiModel)
	if cmd == nil || got.mode != tuiList {
		t.Fatalf("R with no recorded sessions did not fall back: mode %v, cmd %v", got.mode, cmd != nil)
	}
}

func TestResumePickerLaunchesTheSelectedSessionInItsFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	folder := t.TempDir()
	m := wideModel(testProfiles())
	m.cursor = 1
	m.workingDir = t.TempDir()
	m.recent = recentTestSessions(folder)
	m.mode = tuiRecent

	// The second row's folder is gone, so choosing it has to say so and leave
	// the picker up rather than resume somewhere else.
	updated, _ := m.updateRecent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	updated, cmd := updated.(tuiModel).updateRecent(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if cmd != nil || got.mode != tuiRecent || !strings.Contains(got.status, "gone") {
		t.Fatalf("a missing folder was resumed anyway: mode %v, status %q", got.mode, got.status)
	}

	updated, _ = got.updateRecent(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	updated, cmd = updated.(tuiModel).updateRecent(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if cmd == nil || got.mode != tuiList {
		t.Fatalf("the recorded session did not launch: mode %v, cmd %v", got.mode, cmd != nil)
	}
}

// A refresh landing while the picker is open would move the row under the
// cursor, which is the one way a picker of transcripts can resume the wrong one.
func TestOpenResumePickerKeepsItsListThroughARefresh(t *testing.T) {
	m := wideModel(testProfiles())
	m.cursor = 1
	m.recent = recentTestSessions("/work/lattice")
	m.mode = tuiRecent
	updated, _ := m.Update(cockpitLoadedMsg{profile: "codex-work", now: time.Now()})
	got := updated.(tuiModel)
	if len(got.recent) != 2 {
		t.Fatalf("a refresh emptied the open picker: %d rows", len(got.recent))
	}
	if !got.loaded {
		t.Fatal("the refresh was dropped entirely; the live panel still needs it")
	}
}

func TestRecordedFolderPrefersTheRecordedDirectory(t *testing.T) {
	folder, fallback := t.TempDir(), t.TempDir()
	got, err := recordedFolder(recordedSession{folder: folder}, fallback)
	if err != nil || got != folder {
		t.Fatalf("recordedFolder = %q, %v, want the recorded folder", got, err)
	}
	got, err = recordedFolder(recordedSession{}, fallback)
	if err != nil || got != fallback {
		t.Fatalf("a session with no folder = %q, %v, want the launch folder", got, err)
	}
	if _, err := recordedFolder(recordedSession{folder: filepath.Join(folder, "moved")}, fallback); err == nil {
		t.Fatal("a folder that no longer exists was accepted")
	}
}
