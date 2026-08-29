package main

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func searchProfiles() []Profile {
	return []Profile{
		{Name: "claude-personal", Provider: "claude", Command: "claude"},
		{Name: "codex-personal", Provider: "codex", Command: "codex"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
		{Name: "opencode-go", Provider: "opencode", Command: "opencode"},
	}
}

func typeInto(m tuiModel, text string) tuiModel {
	for _, key := range text {
		updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		m = updated.(tuiModel)
	}
	return m
}

func TestSearchNarrowsTheProfileList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(searchProfiles())
	opened, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = typeInto(opened.(tuiModel), "work")
	if !m.searching || m.filter != "work" {
		t.Fatalf("filter = %q, searching = %v", m.filter, m.searching)
	}
	if visible := m.visibleProfiles(); len(visible) != 1 || visible[0].Name != "codex-work" {
		t.Fatalf("visible = %+v, want only codex-work", visible)
	}
	view := m.View()
	if !strings.Contains(view, "codex-work") || strings.Contains(view, "opencode-go") {
		t.Fatalf("the list was not filtered:\n%s", view)
	}
}

func TestSearchMatchesTheProviderToo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(searchProfiles())
	m.filter = "opencode"
	if visible := m.visibleProfiles(); len(visible) != 1 || visible[0].Name != "opencode-go" {
		t.Fatalf("visible = %+v, want the profile whose provider matches", visible)
	}
}

// The cursor indexes the filtered list, so an action taken after a search must
// reach the profile that is on screen — not the one at the same index in the
// full list.
func TestSearchKeepsActionsOnTheVisibleProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(searchProfiles())
	m.cursor = 3
	opened, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = typeInto(opened.(tuiModel), "codex")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want it pulled back onto the filtered list", m.cursor)
	}
	profile, ok := m.selectedProfile()
	if !ok || profile.Name != "codex-work" {
		t.Fatalf("selected = %+v, want a profile from the filtered list", profile)
	}
}

func TestSearchEscapeRestoresTheFullList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(searchProfiles())
	opened, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = typeInto(opened.(tuiModel), "zzz")
	if view := m.View(); !strings.Contains(view, "Nothing matches zzz") {
		t.Fatalf("a search with no matches says nothing:\n%s", view)
	}
	cleared, _ := m.updateList(tea.KeyMsg{Type: tea.KeyEsc})
	got := cleared.(tuiModel)
	if got.searching || got.filter != "" || len(got.visibleProfiles()) != 4 {
		t.Fatalf("esc did not clear the search: %+v", got.filter)
	}
}

// Enter keeps the filter and hands the keys back, so a search is a way to reach
// one account among many rather than a mode to dismiss before acting.
func TestSearchEnterKeepsTheFilterAndReturnsTheKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(searchProfiles())
	opened, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = typeInto(opened.(tuiModel), "work")
	settled, _ := m.updateList(tea.KeyMsg{Type: tea.KeyEnter})
	got := settled.(tuiModel)
	if got.searching || got.filter != "work" {
		t.Fatalf("enter should keep the filter and leave the prompt: %+v", got.filter)
	}
	// The list keys work again rather than typing into the query.
	form, _ := got.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if form.(tuiModel).mode != tuiForm {
		t.Fatalf("keys did not return to the list after enter")
	}
}

// A single status line loses the reason a launch failed the moment the next key
// is pressed, which is exactly when the user goes looking for it.
func TestLogKeepsRecentMessagesNewestFirst(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.setStatus(statusOK, "stopped codex-work")
	m.setStatus(statusErr, "opencode-go is already running")
	if len(m.log) != 2 || m.log[0].text != "opencode-go is already running" {
		t.Fatalf("log = %+v, want the newest message first", m.log)
	}
	view := m.View()
	for _, want := range []string{"LOG", "✓ stopped codex-work", "✗ opencode-go is already running"} {
		if !strings.Contains(view, want) {
			t.Fatalf("log panel is missing %q:\n%s", want, view)
		}
	}
}

func TestLogStaysBounded(t *testing.T) {
	m := tuiModel{}
	for index := 0; index < logLimit+5; index++ {
		m.setStatus(statusOK, "message")
	}
	if len(m.log) != logLimit {
		t.Fatalf("log holds %d entries, want at most %d", len(m.log), logLimit)
	}
}

// The quota bars are the reason the detail column exists; a percentage with no
// reset time cannot tell a window about to refill from one that has to last.
func TestQuotaMetersCarryPercentAndReset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.Local)
	m := wideModel(testProfiles())
	m.cursor, m.now = 1, now
	m.usage = map[string]usageRemaining{"codex-work": {
		FiveHour: usageWindow{Percent: 18, Known: true, Resets: now.Add(2 * time.Hour)},
		Weekly:   usageWindow{Percent: 55, Known: true, Resets: now.Add(72 * time.Hour)},
	}}
	view := m.View()
	for _, want := range []string{"QUOTA", "18%", "55%", "resets 14:00", "resets", "█", "░"} {
		if !strings.Contains(view, want) {
			t.Fatalf("quota block is missing %q:\n%s", want, view)
		}
	}
}

func TestQuotaSaysWhenItHasNothingToShow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	if view := m.View(); !strings.Contains(view, "quota cache") {
		t.Fatalf("a quota that has not loaded should say so:\n%s", view)
	}
	m.usage = map[string]usageRemaining{}
	if view := m.View(); !strings.Contains(view, "no quota recorded") {
		t.Fatalf("a profile with no quota should say so:\n%s", view)
	}
}

func TestRecentSessionsPanelDatesAndPlacesEachSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Date(2026, 8, 29, 18, 0, 0, 0, time.Local)
	m := wideModel(testProfiles())
	m.cursor, m.now = 1, now
	m.recent = []recordedSession{
		{session: instanceSession{title: "refactor the tui view layer"}, folder: "/work/lattice", when: now.Add(-4 * time.Hour)},
		{folder: "/work/hub", when: now.Add(-26 * time.Hour)},
	}
	view := m.View()
	for _, want := range []string{"RECENT SESSIONS", "14:00", "refactor the tui view layer", "/work/lattice", "yest.", "untitled session"} {
		if !strings.Contains(view, want) {
			t.Fatalf("recent panel is missing %q:\n%s", want, view)
		}
	}
}

func TestLivePanelNamesEveryRunningInstanceAcrossProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	now := time.Date(2026, 8, 29, 18, 0, 0, 0, time.Local)
	m := wideModel(testProfiles())
	m.cursor, m.now = 1, now
	m.live = []profileInstance{
		{profile: "codex-work", pid: 48213, folder: "/work/lattice", started: now.Add(-26 * time.Minute), session: instanceSession{id: "a", title: "refactor the tui view layer"}},
		{profile: "claude-personal", pid: 47001, folder: "/work/hub", started: now.Add(-72 * time.Minute), session: instanceSession{id: "b", title: "draft the release notes"}},
	}
	view := m.View()
	for _, want := range []string{"RUNNING · 2 TOTAL", "PID 48213", "refactor the tui view layer", "26m", "claude-personal", "1h12m"} {
		if !strings.Contains(view, want) {
			t.Fatalf("live panel is missing %q:\n%s", want, view)
		}
	}
}

// An empty panel before the first read means "still looking". Saying "nothing
// running" then would be a claim rather than a report.
func TestLivePanelDistinguishesPendingFromIdle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	if view := m.View(); strings.Contains(view, "nothing running") {
		t.Fatalf("the opening frame answered before it had read anything:\n%s", view)
	}
	loaded, _ := m.Update(cockpitLoadedMsg{profile: "claude-personal", now: time.Now()})
	view := loaded.(tuiModel).View()
	if !strings.Contains(view, "nothing running") {
		t.Fatalf("an idle machine should say so once it has looked:\n%s", view)
	}
	if !strings.Contains(view, "no recorded sessions") {
		t.Fatalf("an empty history should say so once it has looked:\n%s", view)
	}
}

func TestFormatUptimeShortensToTheUsefulUnits(t *testing.T) {
	cases := []struct {
		uptime time.Duration
		want   string
	}{
		{0, "—"},
		{30 * time.Second, "just now"},
		{26 * time.Minute, "26m"},
		{72 * time.Minute, "1h12m"},
		{50 * time.Hour, "2d2h"},
	}
	for _, test := range cases {
		if got := formatUptime(test.uptime); got != test.want {
			t.Fatalf("formatUptime(%s) = %q, want %q", test.uptime, got, test.want)
		}
	}
}

func TestInstanceUptimeIgnoresAnUnrecordedStart(t *testing.T) {
	now := time.Now()
	if got := (profileInstance{}).uptime(now); got != 0 {
		t.Fatalf("uptime = %s, want nothing without a recorded start", got)
	}
	if got := (profileInstance{started: now.Add(time.Hour)}).uptime(now); got != 0 {
		t.Fatalf("uptime = %s, want nothing for a start in the future", got)
	}
}

// The modal's error is the answer to what was just typed, so it belongs in the
// box rather than on a panel the box might be covering.
func TestModalCarriesItsOwnStatusLine(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.mode = tuiForm
	m.form = profileForm{name: "codex-work", provider: "codex", command: "codex", isNew: true}
	m.setStatus(statusErr, "a profile named codex-work already exists")
	view := m.View()
	if !strings.Contains(view, "✗ a profile named codex-work already exists") {
		t.Fatalf("the modal does not carry its error:\n%s", view)
	}
}

// Rendering a frame is not the place to touch the disk, so the running counts
// come from the panel that was loaded, not from a fresh scan of the locks.
func TestRunningCountComesFromTheLoadedPanel(t *testing.T) {
	m := tuiModel{profiles: testProfiles(), live: []profileInstance{
		{profile: "codex-work"}, {profile: "codex-work"}, {profile: "claude-personal"},
	}}
	if got := m.runningCount("codex-work"); got != 2 {
		t.Fatalf("runningCount = %d, want 2", got)
	}
	if got := m.runningCount("opencode-go"); got != 0 {
		t.Fatalf("runningCount = %d, want none", got)
	}
}

// Moving the cursor starts a load without cancelling the one in flight, so a
// slow read must not file one account's sessions under another's name.
func TestStaleCockpitLoadIsIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	stale := cockpitLoadedMsg{
		profile: "claude-personal",
		live:    []profileInstance{{profile: "codex-work", pid: 1}},
		recent:  []recordedSession{{session: instanceSession{title: "someone else's session"}}},
		now:     time.Now(),
	}
	got, _ := m.Update(stale)
	updated := got.(tuiModel)
	if len(updated.recent) != 0 {
		t.Fatalf("recent = %+v, want the stale answer discarded", updated.recent)
	}
	if len(updated.live) != 1 {
		t.Fatalf("live = %+v, want what is running kept regardless of the cursor", updated.live)
	}

	fresh := stale
	fresh.profile = "codex-work"
	got, _ = m.Update(fresh)
	if len(got.(tuiModel).recent) != 1 {
		t.Fatalf("a load for the selected profile was discarded")
	}
}

// The cursor indexes the filtered list, so a save has to clear the filter: the
// saved profile need not match whatever the list was narrowed to, and a cursor
// pointing into the old list would select somebody else.
func TestSavingAProfileClearsTheFilterAndSelectsIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: searchProfiles()}); err != nil {
		t.Fatal(err)
	}
	m := wideModel(searchProfiles())
	m.configPath = path
	m.filter, m.searching, m.cursor = "opencode", true, 0
	m.mode = tuiForm
	m.form = profileForm{name: "zed-work", provider: "codex", command: "codex", isNew: true, field: 4}

	saved, _ := m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	got := saved.(tuiModel)
	if got.filter != "" || got.searching {
		t.Fatalf("save left the list filtered to %q", got.filter)
	}
	profile, ok := got.selectedProfile()
	if !ok || profile.Name != "zed-work" {
		t.Fatalf("selected = %+v, want the profile that was just saved", profile)
	}
}
