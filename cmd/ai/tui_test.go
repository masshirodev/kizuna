package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testProfiles() []Profile {
	return []Profile{
		{Name: "claude-personal", Provider: "claude", Command: "claude"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
	}
}

func TestViewListsProfilesWithHelp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles := testProfiles()
	profiles[1].DefaultArgs = []string{"--search"}
	profiles[1].Notes = "work subscription"
	m := tuiModel{
		profiles: profiles,
		width:    80,
		cursor:   1,
		usage: map[string]usageRemaining{"codex-work": {
			FiveHour: usageWindow{Percent: 56, Known: true},
			Weekly:   usageWindow{Percent: 73, Known: true},
		}},
	}
	view := m.View()
	for _, want := range []string{"session profiles", "2 profiles", "claude-personal", "codex-work", "5H", "7D", "56%", "73%", "Launch", "codex --search", "work subscription", "run", "login", "quit"} {
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
	view := tuiModel{profiles: profiles, width: 80}.View()
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
	view := tuiModel{profiles: testProfiles(), width: 100}.View()
	if !strings.Contains(view, "MODEL") || !strings.Contains(view, "opus") {
		t.Fatalf("model column missing:\n%s", view)
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
	narrow := tuiModel{profiles: testProfiles(), width: 46, usage: usage}.View()
	if !strings.Contains(narrow, "opus") || !strings.Contains(narrow, "5H") || !strings.Contains(narrow, "7D") || !strings.Contains(narrow, "56%") || !strings.Contains(narrow, "97%") {
		t.Fatalf("narrow view dropped model or usage:\n%s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if lipgloss.Width(line) > 46 {
			t.Fatalf("line overflows a 46-column terminal (%d):\n%q", lipgloss.Width(line), line)
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
	m := tuiModel{profiles: profiles, width: 80}
	view := m.View()
	if strings.Count(view, "running") != 1 {
		t.Fatalf("expected exactly one running marker:\n%s", view)
	}
	if !strings.Contains(view, "codex-work") {
		t.Fatalf("locked profile missing from view:\n%s", view)
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
	view := tuiModel{profiles: []Profile{profile}, width: 80}.View()
	if !strings.Contains(view, "2 running") {
		t.Fatalf("concurrent instance count missing:\n%s", view)
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
	view := tuiModel{width: 80}.View()
	if !strings.Contains(view, "No profiles yet") || !strings.Contains(view, "add profile") {
		t.Fatalf("unhelpful empty state:\n%s", view)
	}
	if strings.Contains(view, "delete") {
		t.Fatalf("empty state offers keys that do nothing:\n%s", view)
	}
}

func TestViewFormShowsFieldsAndError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 80, mode: tuiForm}
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
	m := tuiModel{profiles: testProfiles(), width: 80, cursor: 1, mode: tuiConfirmDelete}
	view := m.View()
	if !strings.Contains(view, "Delete codex-work?") || !strings.Contains(view, "cannot be undone") {
		t.Fatalf("confirmation is unclear:\n%s", view)
	}
	if !strings.Contains(view, "keep") {
		t.Fatalf("confirmation lacks a cancel hint:\n%s", view)
	}
}

func TestHeaderNamesSelectedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 80, cursor: 1}
	header := m.headerView(m.contentWidth())
	if !strings.Contains(header, "codex-work · 2 profiles") {
		t.Fatalf("header does not name the selected profile:\n%s", header)
	}
	m.cursor = 0
	if header := m.headerView(m.contentWidth()); !strings.Contains(header, "claude-personal · 2 profiles") {
		t.Fatalf("header did not follow the cursor:\n%s", header)
	}
}

// The count is the field that must survive a narrow terminal; a name clipped
// to a few characters identifies nothing.
func TestHeaderDropsProfileNameBeforeTruncatingIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 34, cursor: 1}
	header := m.headerView(m.contentWidth())
	if !strings.Contains(header, "2 profiles") {
		t.Fatalf("header lost the count:\n%s", header)
	}
	if strings.Contains(header, "…") {
		t.Fatalf("header truncated the profile name instead of dropping it:\n%s", header)
	}
	for _, line := range strings.Split(strings.TrimRight(header, "\n"), "\n") {
		if lipgloss.Width(line) > m.contentWidth() {
			t.Fatalf("header line overflows the content width: %q", line)
		}
	}
}

func TestHeaderOmitsNameWithoutProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{width: 80}
	if header := m.headerView(m.contentWidth()); !strings.Contains(header, "no profiles") || strings.Contains(header, "·") {
		t.Fatalf("empty header should carry only the count:\n%s", header)
	}
}

func TestViewOffersUpdateOnlyWhenOneIsAvailable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := tuiModel{profiles: testProfiles(), width: 80}

	m.update = updateStatus{Known: true, Behind: 3}
	if view := m.View(); !strings.Contains(view, "3 commits behind main") || !strings.Contains(view, "go install ./...") {
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

func TestViewSurvivesNarrowAndUnknownWidths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, width := range []int{0, 1, 20, 200} {
		m := tuiModel{profiles: testProfiles(), width: width}
		if view := m.View(); !strings.Contains(view, "codex-work") {
			t.Fatalf("width %d dropped content:\n%s", width, view)
		}
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
		width:    90,
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
		width:     90,
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
	m := tuiModel{profiles: testProfiles(), width: 80, cursor: 1}
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
