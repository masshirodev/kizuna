package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testIntegrationEnv is a machine with the named tools on PATH, the profiles
// in running running, and each profile's status line owned as given.
func testIntegrationEnv(tools string, inRanma bool, running map[string]bool, lines map[string]statusLineOwner) integrationEnv {
	return integrationEnv{
		onPath:     func(tool string) bool { return strings.Contains(" "+tools+" ", " "+tool+" ") },
		inRanma:    inRanma,
		running:    func(profile Profile) bool { return running[profile.Name] },
		statusLine: func(profile Profile) statusLineOwner { return lines[profile.Name] },
	}
}

func stateOf(t *testing.T, profile Profile, kind integrationKind, env integrationEnv) integrationStatus {
	t.Helper()
	for _, status := range integrationsFor(profile, env) {
		if status.kind == kind {
			return status
		}
	}
	t.Fatalf("no %s row for %s", kind.name(), profile.Name)
	return integrationStatus{}
}

func TestStatusLineHasThreeStatesForANativeProvider(t *testing.T) {
	profile := Profile{Name: "claude-max", Provider: "claude", Command: "claude"}
	for _, test := range []struct {
		owner  statusLineOwner
		state  integrationState
		word   string
		action string
	}{
		{statusLineNone, stateOff, "○ off", "turn on"},
		{statusLineOurs, stateOn, "● ai's line", ""},
		{statusLineTheirs, stateAnother, "● another", ""},
	} {
		env := testIntegrationEnv("", false, nil, map[string]statusLineOwner{"claude-max": test.owner})
		status := stateOf(t, profile, integrationStatusLine, env)
		if status.state != test.state || status.word() != test.word || status.action != test.action {
			t.Errorf("owner %d: state %d %q action %q, want %d %q %q", test.owner, status.state, status.word(), status.action, test.state, test.word, test.action)
		}
	}
}

func TestStatusLineForATmuxProviderNeedsTmuxAndRefusesTheShim(t *testing.T) {
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	if status := stateOf(t, profile, integrationStatusLine, testIntegrationEnv("", false, nil, nil)); status.state != stateNeeds || status.word() != "needs tmux" {
		t.Fatalf("without tmux: %q, want needs tmux", status.word())
	}
	status := stateOf(t, profile, integrationStatusLine, testIntegrationEnv("tmux", false, nil, nil))
	if status.state != stateOff || status.note != "tmux bar" || status.action != "turn on" {
		t.Fatalf("with tmux: %q %q %q, want an offer of the tmux bar", status.word(), status.note, status.action)
	}
	profile.TmuxShim = true
	if status := stateOf(t, profile, integrationStatusLine, testIntegrationEnv("tmux", false, nil, nil)); status.state != stateRefused || status.action != "" {
		t.Fatalf("beside the shim: %q action %q, want refused with nothing to do", status.word(), status.action)
	}
	profile.TmuxShim, profile.Indicator = false, tmuxIndicator
	if status := stateOf(t, profile, integrationStatusLine, testIntegrationEnv("tmux", false, nil, nil)); status.state != stateOn || status.action != "" {
		t.Fatalf("set: %q action %q, want on with no undo", status.word(), status.action)
	}
}

func TestOpenUsageIsNeverReadBack(t *testing.T) {
	profile := Profile{Name: "claude-max", Provider: "claude", Command: "claude"}
	status := stateOf(t, profile, integrationOpenUsage, testIntegrationEnv("openusage", false, nil, nil))
	if status.state != stateNotRead || status.action != "install" {
		t.Fatalf("%q action %q, want not read with install offered", status.word(), status.action)
	}
	if status.runs != "openusage integrations install claude_code" {
		t.Fatalf("runs = %q, want the installer", status.runs)
	}
}

func TestOpenUsageIsLockedWhileTheProfileRuns(t *testing.T) {
	profile := Profile{Name: "codex-work", Provider: "codex", Command: "codex"}
	status := stateOf(t, profile, integrationOpenUsage, testIntegrationEnv("openusage", false, map[string]bool{"codex-work": true}, nil))
	if status.state != stateLocked || status.action != "" || status.note != "running" {
		t.Fatalf("%q %q action %q, want locked while running", status.word(), status.note, status.action)
	}
}

func TestOpenUsageIsNotForAntigravity(t *testing.T) {
	profile := Profile{Name: "gemini", Provider: "antigravity", Command: "agy"}
	status := stateOf(t, profile, integrationOpenUsage, testIntegrationEnv("openusage", false, nil, nil))
	if status.state != stateNA || status.note != "antigravity" || status.action != "" {
		t.Fatalf("%q %q action %q, want n/a for antigravity", status.word(), status.note, status.action)
	}
}

func TestRanmaShimStates(t *testing.T) {
	profile := Profile{Name: "max2", Provider: "claude", Command: "claude"}
	if status := stateOf(t, profile, integrationRanma, testIntegrationEnv("", true, nil, nil)); status.state != stateNeeds || status.needs != "ranma" {
		t.Fatalf("without ranma: %q, want needs ranma", status.word())
	}
	status := stateOf(t, profile, integrationRanma, testIntegrationEnv("ranma", false, nil, nil))
	if status.state != stateOff || status.action != "turn on" || status.hereApplies {
		t.Fatalf("off outside ranma: %q action %q applies %v", status.word(), status.action, status.hereApplies)
	}
	if !strings.Contains(status.launch, agentTeamsEnv) {
		t.Fatalf("a claude profile's launch should name the teams flag: %q", status.launch)
	}
	profile.TmuxShim = true
	status = stateOf(t, profile, integrationRanma, testIntegrationEnv("ranma", false, nil, nil))
	if status.state != stateOn || !status.inert || status.note != "inert here" || status.action != "turn off" || !strings.HasSuffix(status.sameAs, "--off") {
		t.Fatalf("on outside ranma: %q %q action %q same as %q", status.word(), status.note, status.action, status.sameAs)
	}
	status = stateOf(t, profile, integrationRanma, testIntegrationEnv("ranma", true, nil, nil))
	if status.inert || !status.hereApplies || status.note != "" {
		t.Fatalf("on inside ranma: inert %v applies %v note %q", status.inert, status.hereApplies, status.note)
	}
}

// The box refuses what the launch refuses: the shim beside the tmux bar. A
// pair set by hand is still offered the way out.
func TestRanmaShimIsRefusedBesideTheTmuxBar(t *testing.T) {
	profile := Profile{Name: "codex-personal", Provider: "codex", Command: "codex", Indicator: tmuxIndicator}
	status := stateOf(t, profile, integrationRanma, testIntegrationEnv("ranma tmux", true, nil, nil))
	if status.state != stateRefused || status.action != "" || status.undo != "" {
		t.Fatalf("%q action %q undo %q, want refused with nothing to do", status.word(), status.action, status.undo)
	}
	profile.TmuxShim = true
	if status := stateOf(t, profile, integrationRanma, testIntegrationEnv("ranma tmux", true, nil, nil)); status.state != stateRefused || status.action != "turn off" {
		t.Fatalf("both set: %q action %q, want refused with turn off offered", status.word(), status.action)
	}
}

// The status line is read back from the file installStatusLine writes, so the
// two cannot disagree about what "ai's line" is.
func TestReadStatusLineOwnerRecognisesTheLineAiWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profile := Profile{Name: "claude-max", Provider: "claude", Command: "claude"}
	if owner := readStatusLineOwner(profile); owner != statusLineNone {
		t.Fatalf("no settings: owner %d, want none", owner)
	}
	path, err := installStatusLine(profile)
	if err != nil {
		t.Fatal(err)
	}
	if owner := readStatusLineOwner(profile); owner != statusLineOurs {
		t.Fatalf("after install: owner %d, want ai's", owner)
	}
	if err := os.WriteFile(path, []byte(`{"statusLine": {"type": "command", "command": "my-line"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if owner := readStatusLineOwner(profile); owner != statusLineTheirs {
		t.Fatalf("someone else's: owner %d, want theirs", owner)
	}
	if filepath.Base(path) != "settings.json" {
		t.Fatalf("path = %s", path)
	}
}

func TestIntegrateListPrintsEveryProfileAndIntegration(t *testing.T) {
	shimProfileConfig(t, Profile{Name: "gemini", Provider: "antigravity", Command: "agy"})
	var stdout, stderr strings.Builder
	if err := run([]string{"integrate", "list"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != len(integrationKinds) {
		t.Fatalf("listing = %q, want one line per integration", stdout.String())
	}
	if lines[1] != "gemini\topenusage\t— n/a\tantigravity" {
		t.Fatalf("openusage line = %q", lines[1])
	}
}

func TestIntegrateOpenUsageRefusesAProviderItHasNothingFor(t *testing.T) {
	shimProfileConfig(t, Profile{Name: "gemini", Provider: "antigravity", Command: "agy"})
	var stdout, stderr strings.Builder
	err := run([]string{"integrate", "openusage", "gemini"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "no antigravity integration") {
		t.Fatalf("err = %v, want the provider named", err)
	}
}

func TestMessagingStatusReadsWhatIntegrateLeft(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	claude := Profile{Name: "claude-max", Provider: "claude", Command: "claude"}
	opencode := Profile{Name: "opencode3", Provider: "opencode", Command: "opencode"}
	deepseek := Profile{Name: "deepseek", Provider: "deepseek", Command: "opencode"}
	envWith := func(delivery map[string]string) integrationEnv {
		env := testIntegrationEnv("ai", false, nil, nil)
		env.messaging = func(profile Profile) string { return delivery[profile.Name] }
		return env
	}

	if status := stateOf(t, claude, integrationMessaging, envWith(nil)); status.state != stateOff || status.action != "turn on" {
		t.Fatalf("nothing written = %+v, want off with turn on", status)
	}
	status := stateOf(t, claude, integrationMessaging, envWith(map[string]string{"claude-max": "hooks"}))
	if status.state != stateOn || status.action != "turn off" || !strings.HasSuffix(status.sameAs, "--off") {
		t.Fatalf("hooks written = %+v, want on with turn off", status)
	}
	// A Claude profile with the server but no hooks still has something to
	// turn on: the hooks are what make it read by itself.
	if status := stateOf(t, claude, integrationMessaging, envWith(map[string]string{"claude-max": "mcp"})); status.state != stateOff || status.note != "tools only" || status.action != "turn on" {
		t.Fatalf("server without hooks = %+v", status)
	}
	if status := stateOf(t, opencode, integrationMessaging, envWith(map[string]string{"opencode3": "mcp"})); status.state != stateOn || status.note != "tools only" {
		t.Fatalf("opencode with the server = %+v, want on, tools only", status)
	}
	if status := stateOf(t, deepseek, integrationMessaging, envWith(nil)); status.state != stateNA {
		t.Fatalf("deepseek = %+v, want n/a: it has no MCP config of its own", status)
	}
}
