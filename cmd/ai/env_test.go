package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Leading assignments are env the way a shell reads them: only while they
// lead, only with a bare name and =, and with the value free to be quoted.
func TestParseLaunchLineSplitsLeadingAssignmentsFromArguments(t *testing.T) {
	for _, test := range []struct {
		line      string
		env, args []string
	}{
		{`FOO=1 BAR=2 --model opus`, []string{"FOO=1", "BAR=2"}, []string{"--model", "opus"}},
		{`FOO="a b" --x`, []string{"FOO=a b"}, []string{"--x"}},
		{`FOO= --x`, []string{"FOO="}, []string{"--x"}},
		{`FOO=1`, []string{"FOO=1"}, nil},
		{`--model opus FOO=1`, nil, []string{"--model", "opus", "FOO=1"}},
		{`'FOO=1' --x`, nil, []string{"FOO=1", "--x"}},
		{`"FOO"=1`, nil, []string{"FOO=1"}},
		{`FOO\=1`, nil, []string{"FOO=1"}},
		{`"x=1 is a prompt"`, nil, []string{"x=1 is a prompt"}},
		{`1FOO=1 =x`, nil, []string{"1FOO=1", "=x"}},
		{`_A1=x a=b`, []string{"_A1=x", "a=b"}, nil},
	} {
		env, args, err := parseLaunchLine(test.line)
		if err != nil {
			t.Fatalf("%s: %v", test.line, err)
		}
		if !slices.Equal(env, test.env) || !slices.Equal(args, test.args) {
			t.Fatalf("%s = env %q args %q, want env %q args %q", test.line, env, args, test.env, test.args)
		}
	}
}

// What is formatted reads back as the same launch, including an argument that
// looks like an assignment and has to stay an argument.
func TestFormatLaunchLineRoundTrips(t *testing.T) {
	for _, test := range []struct {
		env, args []string
		want      string
	}{
		{[]string{"FOO=1", "BAR=a b", "EMPTY="}, []string{"--x"}, `FOO=1 BAR='a b' EMPTY= --x`},
		{nil, []string{"FOO=1", "--x"}, `'FOO=1' --x`},
		{[]string{"A=1"}, []string{"B=2", "C=3"}, `A=1 'B=2' C=3`},
		{nil, []string{"--model", "gpt 5"}, `--model 'gpt 5'`},
	} {
		line := formatLaunchLine(test.env, test.args)
		if line != test.want {
			t.Fatalf("format(%q, %q) = %s, want %s", test.env, test.args, line, test.want)
		}
		env, args, err := parseLaunchLine(line)
		if err != nil || !slices.Equal(env, test.env) || !slices.Equal(args, test.args) {
			t.Fatalf("%s read back as env %q args %q (%v)", line, env, args, err)
		}
	}
}

// A default fills only what nothing set; the shell and ai-session's own
// variables beat it, the prompt beats everything, and plain drops defaults.
func TestLaunchEnvLayersDefaultsUnderTheShellAndTypedOverAll(t *testing.T) {
	profile := Profile{Name: "max", Provider: "claude", DefaultEnv: []string{"FOO=default", "BAR=default"}}
	built := []string{"PATH=/bin", "FOO=shell", "CLAUDE_CONFIG_DIR=/profiles/max/claude"}

	got := withLaunchEnv(built, profile, false, nil)
	if envValue(got, "FOO") != "shell" || envValue(got, "BAR") != "default" {
		t.Fatalf("defaults = %v, want the shell's FOO and the default BAR", got)
	}
	got = withLaunchEnv(built, profile, false, []string{"FOO=typed", "CLAUDE_CONFIG_DIR=/elsewhere"})
	if envValue(got, "FOO") != "typed" || envValue(got, "CLAUDE_CONFIG_DIR") != "/elsewhere" {
		t.Fatalf("typed = %v, want typed values to win", got)
	}
	if strings.Count(strings.Join(got, "\n"), "FOO=") != 1 {
		t.Fatalf("typed FOO left a duplicate behind: %v", got)
	}
	got = withLaunchEnv(built, profile, true, nil)
	if hasEnv(got, "BAR") {
		t.Fatalf("plain launch kept a default: %v", got)
	}
}

func TestOwnedEnvNamesFollowTheProvider(t *testing.T) {
	claude := Profile{Name: "max", Provider: "claude"}
	if owned := ownedNamesIn(claude, []string{"CLAUDE_CONFIG_DIR=/x", "AI_PROFILE=y", "HOME=/h", "FOO=1"}); !slices.Equal(owned, []string{"CLAUDE_CONFIG_DIR", "AI_PROFILE"}) {
		t.Fatalf("claude owns %v", owned)
	}
	// Antigravity is isolated through HOME, so there HOME is ai-session's.
	agy := Profile{Name: "gemini", Provider: "antigravity"}
	if owned := ownedNamesIn(agy, []string{"HOME=/h"}); !slices.Equal(owned, []string{"HOME"}) {
		t.Fatalf("antigravity owns %v", owned)
	}
}

func TestPreviewEnvShowsTheDefaultsThatApplyThenTheTyped(t *testing.T) {
	profile := Profile{Name: "max", Provider: "claude", DefaultEnv: []string{"A=1", "B=2", "C=3"}}
	got := previewEnv(profile, []string{"B=shell"}, []string{"C=typed"})
	if !slices.Equal(got, []string{"A=1", "C=typed"}) {
		t.Fatalf("preview = %v", got)
	}
	if shadowed := shadowedDefaults(profile, []string{"B=shell"}); !slices.Equal(shadowed, []string{"B"}) {
		t.Fatalf("shadowed = %v", shadowed)
	}
}

// The whole path from the command line: `FOO=shell ai ka` beats the default
// FOO, the default BAR fills in, and --plain drops it.
func TestLaunchAppliesDefaultEnvUnderTheShell(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FOO", "shell")
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{{
		Name:        "ka",
		Provider:    "codex",
		Command:     "/bin/sh",
		DefaultArgs: []string{"-c", `echo "$FOO|$BAR"`},
		DefaultEnv:  []string{"FOO=default", "BAR=default"},
	}}}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if err := run([]string{"ka"}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	if stdout.String() != "shell|default\n" {
		t.Fatalf("launch saw %q, want the shell's FOO and the default BAR", stdout.String())
	}
	stdout.Reset()
	if err := run([]string{"-p", "ka", "-c", `echo "$FOO|$BAR"`}, &stdout, &stderr); err != nil {
		t.Fatal(err, stderr.String())
	}
	if stdout.String() != "shell|\n" {
		t.Fatalf("plain launch saw %q, want no default BAR", stdout.String())
	}
}

// Env is part of what makes two sets the same: FOO=1 --x and --x are two rows.
func TestArgumentHistoryTellsSetsApartByTheirEnv(t *testing.T) {
	history := argumentHistory{}
	history = history.record(argumentSet{Args: []string{"--x"}})
	history = history.record(argumentSet{Env: []string{"FOO=1"}, Args: []string{"--x"}})
	history = history.record(argumentSet{Env: []string{"FOO=1"}})
	if len(history.Recent) != 3 {
		t.Fatalf("recent = %+v, want three distinct sets", history.Recent)
	}
	history = history.togglePin(argumentSet{Env: []string{"FOO=1"}, Args: []string{"--x"}})
	if len(history.Pinned) != 1 || history.Pinned[0].line() != "FOO=1 --x" || len(history.Recent) != 2 {
		t.Fatalf("pinned %+v recent %+v", history.Pinned, history.Recent)
	}
}

func TestParamsPromptRemembersTheEnvItLaunchedWith(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	m.workingDir = t.TempDir()
	m.now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	m = press(t, m, "p")
	m = paramsKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("FOO=1 --search")})
	if view := m.View(); !strings.Contains(view, "FOO=1 codex") {
		t.Fatalf("the preview does not lead with the typed env:\n%s", view)
	}
	updated, cmd := m.updateParams(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(tuiModel)
	if got.unlock != nil {
		defer got.unlock()
	}
	if cmd == nil {
		t.Fatalf("the prompt did not launch: %q", got.status)
	}
	history, err := loadArgumentHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Recent) != 1 || !slices.Equal(history.Recent[0].Env, []string{"FOO=1"}) || !slices.Equal(history.Recent[0].Args, []string{"--search"}) {
		t.Fatalf("recorded %+v, want env and args apart", history.Recent)
	}
}

func TestParamsPromptWarnsWhenTypedEnvOverridesAiSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.cursor = 1
	m = press(t, m, "p")
	m = paramsKey(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("CODEX_HOME=/x")})
	if view := m.View(); !strings.Contains(view, "CODEX_HOME overrides ai-session's own") {
		t.Fatalf("no warning for an owned variable:\n%s", view)
	}
}

// The default-args field takes env too, refuses ai-session's own names, and
// saving keeps what the form does not show.
func TestSaveFormStoresDefaultEnvAndKeepsHiddenFields(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(path, Config{Profiles: []Profile{{
		Name: "max", Provider: "claude", Command: "claude", Indicator: tmuxIndicator, TmuxShim: true,
	}}}); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{configPath: path}
	m.form = profileForm{name: "max", provider: "claude", command: "claude", original: "max",
		defaultArgs: `CLAUDE_CONFIG_DIR=/x --permission-mode auto`}
	if err := m.saveForm(); err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR") {
		t.Fatalf("saving an owned default = %v, want it refused", err)
	}
	m.form.defaultArgs = `FOO='a b' --permission-mode auto`
	if err := m.saveForm(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := findProfile(cfg, "max")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(profile.DefaultEnv, []string{"FOO=a b"}) || !slices.Equal(profile.DefaultArgs, []string{"--permission-mode", "auto"}) {
		t.Fatalf("saved %+v", profile)
	}
	if profile.Indicator != tmuxIndicator || !profile.TmuxShim {
		t.Fatalf("saving the form dropped the indicator or shim: %+v", profile)
	}
}
