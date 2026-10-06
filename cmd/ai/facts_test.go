package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLaunchModelPrefersTheProfilesOwnArguments(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"model":"sonnet"}`, "max", "claude", "settings.json")
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	if model, source := launchModel(profile); model != "sonnet" || source != "from settings" {
		t.Fatalf("launchModel = %q %q, want the settings' model", model, source)
	}
	// The arguments override the settings on the CLI's own command line, so
	// naming the settings' model beside them would contradict the RUNS line.
	for _, args := range [][]string{{"--model", "opus"}, {"--model=opus"}, {"-m", "opus"}, {"-m=opus"}, {"--model", "haiku", "-m", "opus"}} {
		profile.DefaultArgs = args
		if model, source := launchModel(profile); model != "opus" || source != "from args" {
			t.Errorf("launchModel(%v) = %q %q, want opus from args", args, model, source)
		}
	}
	profile.DefaultArgs = []string{"--model"}
	if model, _ := launchModel(profile); model != "sonnet" {
		t.Fatalf("a dangling --model named %q", model)
	}
}

func TestResetPhraseSpellsTheDayAndTheWait(t *testing.T) {
	now := time.Date(2026, 9, 23, 14, 5, 0, 0, time.Local)
	cases := []struct {
		resets   time.Time
		when, in string
	}{
		{time.Date(2026, 9, 23, 21, 40, 0, 0, time.Local), "21:40 today", "in 7h35m"},
		{time.Date(2026, 9, 24, 9, 0, 0, 0, time.Local), "Thu 24 Sep 09:00", "in 18h55m"},
		{time.Date(2026, 9, 23, 14, 30, 0, 0, time.Local), "14:30 today", "in 25m"},
		{time.Date(2026, 9, 26, 9, 0, 0, 0, time.Local), "Sat 26 Sep 09:00", "in 2d18h"},
	}
	for _, c := range cases {
		if when, in := resetPhrase(now, c.resets); when != c.when || in != c.in {
			t.Errorf("resetPhrase(%v) = %q %q, want %q %q", c.resets, when, in, c.when, c.in)
		}
	}
	if when, _ := resetPhrase(now, time.Time{}); when != "" {
		t.Fatalf("an unknown reset said %q", when)
	}
	if when, _ := resetPhrase(now, now.Add(-time.Minute)); when != "" {
		t.Fatalf("a reset in the past said %q", when)
	}
}

func TestFolderShortKeepsTheRootAndTheLeaf(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		folder string
		width  int
		want   string
	}{
		{filepath.Join(home, "projects/ai-session"), 16, "~/…/ai-session"},
		{filepath.Join(home, "projects/ranma"), 16, "~/projects/ranma"},
		{filepath.Join(home, "work/billing"), 14, "~/work/billing"},
		{"/srv/lattice/releases/current", 16, "/…/current"},
	}
	for _, c := range cases {
		if got := folderShort(c.folder, c.width); got != c.want {
			t.Errorf("folderShort(%q, %d) = %q, want %q", c.folder, c.width, got, c.want)
		}
	}
	if got := folderLeaf(filepath.Join(home, "work/billing")); got != "billing" {
		t.Fatalf("folderLeaf = %q", got)
	}
}

// The histogram ends at the current hour, so the labels follow the clock rather
// than sitting at fixed places in the row.
func TestActivityAxisLabelsTheHoursTheBarsAre(t *testing.T) {
	cases := map[int]string{
		14: "   18    00    06    12",
		23: "00    06    12    18",
		0:  "     06    12    18    00",
	}
	for hour, want := range cases {
		now := time.Date(2026, 9, 23, hour, 5, 0, 0, time.Local)
		if got := activityAxis(now); got != want {
			t.Errorf("activityAxis at %02d:05 = %q, want %q", hour, got, want)
		}
	}
}

func TestDuplicateTitlesIgnoresCaseAndUntitledRows(t *testing.T) {
	records := []recordedSession{
		{session: instanceSession{id: "a", title: "What's next"}},
		{session: instanceSession{id: "b", title: "what's next "}},
		{session: instanceSession{id: "c", title: "Refactor"}},
		{session: instanceSession{id: "d"}},
		{session: instanceSession{id: "e"}},
	}
	got := duplicateTitles(records)
	if !got["a"] || !got["b"] || got["c"] || got["d"] || got["e"] {
		t.Fatalf("duplicateTitles = %v, want a and b only", got)
	}
}

func TestSecondPromptSkipsWhatTheCLISentAhead(t *testing.T) {
	messages := []handoffMessage{
		{fromUser: true, text: "<environment_context>cwd</environment_context>"},
		{fromUser: true, text: "What's next"},
		{fromUser: false, text: "Here is the list."},
		{fromUser: true, text: "after the   folder tree\nlands, then the rest"},
	}
	if got := secondPrompt(messages); got != "after the folder tree" {
		t.Fatalf("secondPrompt = %q", got)
	}
	if got := secondPrompt(messages[:3]); got != "" {
		t.Fatalf("a conversation with one prompt gave %q", got)
	}
}

func TestHeadlessLaunchKnowsEachProvidersSpelling(t *testing.T) {
	cases := []struct {
		provider string
		args     []string
		want     bool
	}{
		{"claude", []string{"--model", "opus", "-p", "lint"}, true},
		{"claude", []string{"--print", "lint"}, true},
		{"claude", []string{"--dangerously-skip-permissions"}, false},
		{"codex", []string{"exec", "lint"}, true},
		{"codex", []string{"resume"}, false},
		{"opencode", []string{"run", "--auto", "lint"}, true},
		{"opencode", []string{"--continue"}, false},
	}
	for _, c := range cases {
		if got := headlessLaunch(c.provider, c.args); got != c.want {
			t.Errorf("headlessLaunch(%s, %v) = %v, want %v", c.provider, c.args, got, c.want)
		}
	}
}

func TestInstanceMetaCarriesTheHeadlessFlag(t *testing.T) {
	dir := t.TempDir()
	if err := setProfileInstanceMeta(dir, "/work/hub", true); err != nil {
		t.Fatal(err)
	}
	if meta := readInstanceMeta(dir); !meta.Headless || meta.Folder != "/work/hub" {
		t.Fatalf("meta = %+v, want a headless launch in /work/hub", meta)
	}
}

// A handoff learns which conversation it became from what that conversation
// was opened with, and writes it down so the arrow is drawn from then on.
func TestResolveIncomingMatchesTheConversationTheHandoffOpened(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	brief := filepath.Join(root, "brief-0e981f8e.md")
	if err := os.WriteFile(brief, []byte("# Handoff: Fix billing retries\n\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pasted := filepath.Join(root, "brief-pasted.md")
	if err := os.WriteFile(pasted, []byte("# Handoff: Tidy the board\n\nbody\n"), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 23, 9, 40, 0, 0, time.UTC)
	writeTranscript(t, root, "max", "-work-hub", "aaaa1111", "/work/hub", "2026-09-23T09:41:00Z",
		"Read the handoff brief at "+brief+" and continue that work.")
	writeTranscript(t, root, "max", "-work-hub", "bbbb2222", "/work/hub", "2026-09-23T09:42:00Z", "something else")
	writeTranscript(t, root, "max", "-work-hub", "cccc3333", "/work/hub", "2026-09-23T15:00:00Z", "# Handoff: Tidy the board")
	links := []lineageLink{
		{When: when, SourceProfile: "pro", SourceSessionID: "src1", TargetProfile: "max", Folder: "/work/hub", Brief: brief},
		{When: when.Add(time.Hour), SourceProfile: "pro2", SourceSessionID: "src2", TargetProfile: "max", Folder: "/work/hub", Brief: pasted},
		{When: when, SourceProfile: "max", SourceSessionID: "src3", TargetProfile: "pro", Brief: brief},
	}
	if err := writeLineage(links); err != nil {
		t.Fatal(err)
	}
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	recent := scannedRecentSessions(profile, 12)
	if len(recent) != 3 {
		t.Fatalf("read %d sessions, want 3", len(recent))
	}

	resolved, ruled := resolveIncoming(profile, readLineage(), recent, nil)
	if resolved[0].TargetSessionID != "aaaa1111" || resolved[1].TargetSessionID != "cccc3333" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[2].TargetSessionID != "" {
		t.Fatal("a handoff to another profile was resolved against this one")
	}
	if !ruled[brief+"\x00bbbb2222"] {
		t.Fatalf("the conversation that did not match was not ruled out: %v", ruled)
	}
	stored := readLineage()
	if stored[0].TargetSessionID != "aaaa1111" || stored[1].TargetSessionID != "cccc3333" {
		t.Fatalf("the resolution was not written to the lineage: %+v", stored)
	}

	facts, _ := loadProfileFacts(profile, Config{Apps: []App{{Name: "shiori", Members: []string{"max", "pro"}, Active: "max"}}}, nil, recent, nil, nil)
	if facts.incoming["aaaa1111"] != "pro" || facts.incoming["cccc3333"] != "pro2" {
		t.Fatalf("incoming = %v", facts.incoming)
	}
	if len(facts.apps) != 1 || facts.apps[0].name != "shiori" || !facts.apps[0].active {
		t.Fatalf("apps = %+v", facts.apps)
	}
}

func TestLoadProfileFactsReadsSecondPromptsOnlyForSharedTitles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	user := func(at, text string) string {
		return `{"type":"user","cwd":"/work/hub","timestamp":"` + at + `","message":{"content":[{"type":"text","text":"` + text + `"}]}}`
	}
	writeTranscriptLines(t, root, "max", "-work-hub", "one", user("2026-09-23T09:00:00Z", "What's next"), user("2026-09-23T09:05:00Z", "after the folder tree lands"))
	writeTranscriptLines(t, root, "max", "-work-hub", "two", user("2026-09-23T10:00:00Z", "What's next"), user("2026-09-23T10:05:00Z", "billing retry backoff"))
	writeTranscriptLines(t, root, "max", "-work-hub", "three", user("2026-09-23T11:00:00Z", "Refactor"), user("2026-09-23T11:05:00Z", "not asked for"))
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	recent := scannedRecentSessions(profile, 12)
	facts, _ := loadProfileFacts(profile, Config{}, nil, recent, nil, nil)
	got := map[string]string{}
	for _, record := range recent {
		if second, ok := facts.seconds[secondPromptKey(record)]; ok {
			got[record.session.id] = second
		}
	}
	if got["one"] != "after the folder tree lands" || got["two"] != "billing retry backoff" || len(got) != 2 {
		t.Fatalf("second prompts = %v, want the two What's next rows only", got)
	}
	// What has been read is passed back in and not read again.
	known := map[string]string{}
	for _, record := range recent {
		known[secondPromptKey(record)] = "remembered"
	}
	facts, _ = loadProfileFacts(profile, Config{}, nil, recent, known, nil)
	for key, second := range facts.seconds {
		if second != "remembered" {
			t.Fatalf("%s was read again: %q", key, second)
		}
	}
}
