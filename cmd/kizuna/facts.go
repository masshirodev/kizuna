package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// profileFacts is what the expanded row says about the selected account beyond
// its config: the integration states, what is installed in it, which apps it
// belongs to, and what its recent list leaves out. Every part of it costs a
// file read, so it is gathered with the rest of the cockpit's panels, off the
// UI thread, and the row shows `…` until it lands.
type profileFacts struct {
	profile string
	loaded  bool
	// integrations are the same statuses the integrations box (`I`) reads, so
	// the row and the box cannot disagree about what is on.
	integrations []integrationStatus
	mcp          []string
	mcpKnown     bool
	skills       int
	skillsKnown  bool
	apps         []appMembership
	counts       sessionCounts
	// seconds is the second prompt of each listed conversation whose title is
	// shared by another listed one, keyed by secondPromptKey. The first prompt
	// is what the title is made of, so two "What's next" rows are told apart by
	// what was asked after it.
	seconds map[string]string
	// incoming is where each conversation that arrived by handoff came from,
	// keyed by the conversation's id.
	incoming map[string]string
}

// appMembership is one app the profile is a member of, and whether it is the
// one the app currently points at.
type appMembership struct {
	name   string
	active bool
}

// sessionCounts is how many conversations the profile has on record, split the
// way the recent list splits them. known is false when the session index could
// not be read, and the row then says nothing rather than counting what it
// happened to load.
type sessionCounts struct {
	interactive int
	headless    int
	known       bool
}

// launchModel is the model a launch actually starts with, and where that came
// from. A `--model` in the profile's own arguments overrides whatever the
// CLI's settings say, so it is read first: naming the settings' model beside a
// command line that passes another one was the row contradicting itself.
func launchModel(profile Profile) (model, source string) {
	if model := modelFromArgs(profile.DefaultArgs); model != "" {
		return model, "from args"
	}
	if model := profileModel(profile); model != "" {
		return model, "from settings"
	}
	return "", ""
}

// modelFromArgs finds `--model X`, `--model=X`, `-m X` or `-m=X`, the spellings
// the providers' CLIs accept. The last one wins, as it does on their command
// lines.
func modelFromArgs(args []string) string {
	model := ""
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--model" || arg == "-m":
			if index+1 < len(args) {
				model = args[index+1]
				index++
			}
		case strings.HasPrefix(arg, "--model="):
			model = strings.TrimPrefix(arg, "--model=")
		case strings.HasPrefix(arg, "-m="):
			model = strings.TrimPrefix(arg, "-m=")
		}
	}
	return model
}

// resetPhrase is when a window rolls over, spelled out in full for the line
// under the gauges: the row itself only has room for "21:40" or "thu", and
// "thu" leaves both the date and the hour to be guessed.
func resetPhrase(now, resets time.Time) (when, in string) {
	if resets.IsZero() || !resets.After(now) {
		return "", ""
	}
	local, today := resets.Local(), now.Local()
	if local.Year() == today.Year() && local.YearDay() == today.YearDay() {
		when = local.Format("15:04") + " today"
	} else {
		when = local.Format("Mon 2 Jan 15:04")
	}
	return when, "in " + formatSpan(resets.Sub(now))
}

// formatSpan is a duration in the two largest units that matter.
func formatSpan(span time.Duration) string {
	span = span.Round(time.Minute)
	switch {
	case span < time.Hour:
		return fmt.Sprintf("%dm", int(span.Minutes()))
	case span < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(span.Hours()), int(span.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(span.Hours())/24, int(span.Hours())%24)
	}
}

// folderLeaf is a folder by its last name, for the glance, where the column is
// narrow and the leaf is the part that says which project.
func folderLeaf(folder string) string {
	if folder == "" {
		return ""
	}
	return filepath.Base(folder)
}

// folderShort keeps a folder whole when it fits and otherwise collapses its
// middle, so the root (~ or /) and the leaf both survive: "~/…/ai-session".
func folderShort(folder string, width int) string {
	short := shortenHome(folder)
	if len([]rune(short)) <= width {
		return short
	}
	leaf := filepath.Base(short)
	root := "/"
	if strings.HasPrefix(short, "~") {
		root = "~/"
	}
	if collapsed := root + "…/" + leaf; len([]rune(collapsed)) <= width {
		return collapsed
	}
	return truncate(leaf, width)
}

// activityAxis labels the sparkline's hours. The histogram is the last 24
// hours with the current one last, so a label sits under each bar whose hour
// is a multiple of six, wherever in the row that falls.
func activityAxis(now time.Time) string {
	oldest := now.Local().Truncate(time.Hour).Add(-(activityHours - 1) * time.Hour)
	axis := []rune(strings.Repeat(" ", activityHours+1))
	for index := range activityHours {
		hour := oldest.Add(time.Duration(index) * time.Hour).Hour()
		if hour%6 != 0 {
			continue
		}
		label := []rune(fmt.Sprintf("%02d", hour))
		if index+len(label) > len(axis) {
			continue
		}
		copy(axis[index:], label)
	}
	return strings.TrimRight(string(axis), " ")
}

// duplicateTitles is the ids among records whose title another of them shares,
// ignoring case and surrounding space. An untitled row is never a duplicate:
// "untitled session" says nothing a second prompt would add to.
func duplicateTitles(records []recordedSession) map[string]bool {
	byTitle := map[string][]string{}
	for _, record := range records {
		title := strings.ToLower(strings.TrimSpace(record.session.title))
		if title == "" || record.session.id == "" {
			continue
		}
		byTitle[title] = append(byTitle[title], record.session.id)
	}
	duplicates := map[string]bool{}
	for _, ids := range byTitle {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			duplicates[id] = true
		}
	}
	return duplicates
}

// secondPromptKey names a conversation as it stands. A second prompt never
// changes once written, but a conversation with only one yet can grow another,
// so a session that has moved on is asked again.
func secondPromptKey(record recordedSession) string {
	return record.session.id + "@" + fmt.Sprint(record.activity().UnixNano())
}

// userPrompts is what the user typed, in order, with the blocks a CLI sends
// ahead of a prompt dropped and the ones it wraps around one opened — the same
// reduction the titles are made with.
func userPrompts(messages []handoffMessage) []string {
	var prompts []string
	for _, message := range messages {
		if !message.fromUser {
			continue
		}
		text := strings.TrimSpace(unwrapInjected(strings.TrimSpace(message.text)))
		if text == "" || isInjectedPreamble(text) {
			continue
		}
		prompts = append(prompts, text)
	}
	return prompts
}

// secondPrompt is the first line of the second thing the user typed, or
// nothing for a conversation that has only one.
func secondPrompt(messages []handoffMessage) string {
	prompts := userPrompts(messages)
	if len(prompts) < 2 {
		return ""
	}
	line, _, _ := strings.Cut(prompts[1], "\n")
	return strings.Join(strings.Fields(line), " ")
}

// recentRowsShown is how many conversations each level of the expansion
// lists. The duplicates are looked for among exactly these, so a second prompt
// is never read for a row nobody can see.
const (
	glanceRecentRows = 5
	sheetRecentRows  = 8
)

// loadProfileFacts gathers the facts for the selected profile. known carries
// the second prompts already read, so a refresh asks only about rows that are
// new or have moved on; ruledOut is the same for handoffs that have been
// matched against a conversation and did not fit.
func loadProfileFacts(profile Profile, cfg Config, live []profileInstance, recent []recordedSession, known map[string]string, ruledOut map[string]bool) (profileFacts, map[string]bool) {
	facts := profileFacts{profile: profile.Name, loaded: true, seconds: map[string]string{}, incoming: map[string]string{}}

	env := machineIntegrationEnv()
	env.running = func(p Profile) bool {
		for _, instance := range live {
			if instance.profile == p.Name {
				return true
			}
		}
		return false
	}
	facts.integrations = integrationsFor(profile, env)

	if supportsMCP(profile) {
		if servers, err := readMCPServers(profile); err == nil {
			facts.mcpKnown = true
			for _, server := range servers {
				facts.mcp = append(facts.mcp, server.Name)
			}
		}
	}
	if supportsSkills(profile) {
		if skills, err := readSkills(profile); err == nil {
			facts.skills, facts.skillsKnown = len(skills), true
		}
	}
	for _, app := range cfg.Apps {
		if contains(app.Members, profile.Name) {
			facts.apps = append(facts.apps, appMembership{name: app.Name, active: app.Active == profile.Name})
		}
	}
	facts.counts = indexedSessionCounts(profile)

	var shown []recordedSession
	for _, record := range recent {
		if !record.headless {
			shown = append(shown, record)
		}
		if len(shown) == sheetRecentRows {
			break
		}
	}
	duplicates := duplicateTitles(shown)
	for _, record := range shown {
		if !duplicates[record.session.id] {
			continue
		}
		key := secondPromptKey(record)
		if second, ok := known[key]; ok {
			facts.seconds[key] = second
			continue
		}
		messages, _, err := readSessionMessages(profile, record)
		if err != nil {
			continue
		}
		facts.seconds[key] = secondPrompt(messages)
	}

	links, ruled := resolveIncoming(profile, readLineage(), recent, ruledOut)
	for _, link := range links {
		if link.TargetProfile == profile.Name && link.TargetSessionID != "" {
			facts.incoming[link.TargetSessionID] = link.SourceProfile
		}
	}
	return facts, ruled
}

// resolveIncoming finds the conversation each handoff to this profile became,
// for the ones not resolved yet, and records it in the lineage. The handoff
// opens the target on a prompt naming the brief's path, or pastes the brief
// itself, so the conversation whose first prompt carries either is the one —
// a match on what was said rather than a guess from times and folders. Times
// and folders only pick which conversations are worth reading.
//
// ruledOut remembers a conversation already read for a link that did not
// match. A first prompt never changes, so it is never read for that link again.
func resolveIncoming(profile Profile, links []lineageLink, recent []recordedSession, ruledOut map[string]bool) ([]lineageLink, map[string]bool) {
	ruled := map[string]bool{}
	for key := range ruledOut {
		ruled[key] = true
	}
	claimed := map[string]bool{}
	for _, link := range links {
		if link.TargetSessionID != "" {
			claimed[link.TargetSessionID] = true
		}
	}
	changed := false
	for index, link := range links {
		if link.TargetProfile != profile.Name || link.TargetSessionID != "" || link.Brief == "" {
			continue
		}
		heading := briefHeading(link.Brief)
		for _, record := range recent {
			id := record.session.id
			key := link.Brief + "\x00" + id
			if id == "" || claimed[id] || ruled[key] || !handoffCandidate(link, record) {
				continue
			}
			messages, _, err := readSessionMessages(profile, record)
			if err != nil {
				continue
			}
			if !openedBy(messages, link.Brief, heading) {
				ruled[key] = true
				continue
			}
			links[index].TargetSessionID = id
			claimed[id], changed = true, true
			break
		}
	}
	if changed {
		_ = writeLineage(links)
	}
	return links, ruled
}

// handoffWindow is how long after a handoff its conversation may begin. A
// prompt handoff starts the target at once; a pasted brief waits for whoever
// pastes it, which is why the window is a day and not a minute.
const handoffWindow = 24 * time.Hour

// handoffCandidate is whether a conversation could have been started by the
// handoff: begun in its folder, around or after it.
func handoffCandidate(link lineageLink, record recordedSession) bool {
	began := record.when
	if began.IsZero() {
		began = record.activity()
	}
	if began.Before(link.When.Add(-2*time.Minute)) || began.After(link.When.Add(handoffWindow)) {
		return false
	}
	return link.Folder == "" || record.folder == "" || filepath.Clean(link.Folder) == filepath.Clean(record.folder)
}

// briefHeading is the brief's first line, which a pasted brief opens with.
func briefHeading(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(body), "\n")
	return strings.TrimSpace(line)
}

// openedBy is whether the conversation's first prompt is the handoff's.
func openedBy(messages []handoffMessage, brief, heading string) bool {
	for _, message := range messages {
		if !message.fromUser {
			continue
		}
		text := strings.TrimSpace(message.text)
		if text == "" || isInjectedPreamble(text) {
			continue
		}
		return strings.Contains(text, brief) || (heading != "" && strings.HasPrefix(text, heading))
	}
	return false
}
