package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// instanceSession names the conversation a running instance has open. Both
// fields are best effort: providers that expose neither leave them empty, and
// the TUI simply shows the instance without a title.
type instanceSession struct {
	id    string
	title string
}

// sessionLookupTimeout bounds the provider CLI call made to list live
// sessions, so a hung binary cannot freeze the picker that asked for it.
const sessionLookupTimeout = 5 * time.Second

// describeInstances fills in the session each instance is working on. It is
// meant to run off the UI thread: claude is asked over a subprocess and codex
// is answered by reading its session log, both of which take long enough to be
// felt as a stutter if done during a keypress.
func describeInstances(profile Profile, instances []profileInstance) []profileInstance {
	described := append([]profileInstance(nil), instances...)
	switch profile.Provider {
	case "claude":
		live := claudeLiveSessions(profile)
		for index, instance := range described {
			described[index].session = matchClaudeSession(live, instance)
		}
	case "codex":
		for index, instance := range described {
			described[index].session = codexSessionInFolder(profile, instance.folder)
		}
	}
	return described
}

type claudeAgent struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// claudeLiveSessions asks Claude Code for the sessions running under this
// profile. The profile environment is applied so the answer covers that
// profile's isolated state and nothing else.
func claudeLiveSessions(profile Profile) []claudeAgent {
	ctx, cancel := context.WithTimeout(context.Background(), sessionLookupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, profile.Command, "agents", "--json")
	cmd.Env = append(cleanEnvironment(os.Environ()), profileEnv(profile)...)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	var agents []claudeAgent
	if err := json.Unmarshal(output, &agents); err != nil {
		return nil
	}
	return agents
}

// matchClaudeSession prefers the PID recorded at launch. The launch folder is
// the fallback, which is what answers when the CLI reports a different process
// in the same tree than the one ai-session started.
func matchClaudeSession(agents []claudeAgent, instance profileInstance) instanceSession {
	for _, agent := range agents {
		if agent.PID == instance.pid {
			return instanceSession{id: agent.SessionID, title: agent.Name}
		}
	}
	if instance.folder == "" {
		return instanceSession{}
	}
	for _, agent := range agents {
		if agent.CWD == instance.folder {
			return instanceSession{id: agent.SessionID, title: agent.Name}
		}
	}
	return instanceSession{}
}

// codexSessionSearchLimit caps how many recorded sessions are opened while
// looking for the newest one in a folder. Rollout names sort by timestamp, so
// the match is almost always in the first few.
const codexSessionSearchLimit = 60

// codexSessionInFolder finds the most recent Codex session recorded for a
// folder. Codex has no command that lists sessions as data, so its own rollout
// log is read instead — the session id from the header, and the first real
// user message as the title.
func codexSessionInFolder(profile Profile, folder string) instanceSession {
	if folder == "" {
		return instanceSession{}
	}
	root, err := profileRoot()
	if err != nil {
		return instanceSession{}
	}
	rollouts := codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions"))
	for index, path := range rollouts {
		if index >= codexSessionSearchLimit {
			break
		}
		if record, ok := readCodexRollout(path, folder); ok {
			return record.session
		}
	}
	return instanceSession{}
}

// describeLiveInstances names what each running instance is working on using
// only the transcripts already on disk. The hijack picker asks the provider CLI
// instead, because reattaching needs the session id the CLI is holding open;
// this panel refreshes on a timer and cannot spend a subprocess per profile on
// every tick.
func describeLiveInstances(profiles []Profile, instances []profileInstance) []profileInstance {
	described := append([]profileInstance(nil), instances...)
	byProfile := make(map[string][]recordedSession, len(profiles))
	for index, instance := range described {
		if instance.folder == "" {
			continue
		}
		records, read := byProfile[instance.profile]
		if !read {
			records = recentSessions(profileNamed(profiles, instance.profile), recentSessionLimit)
			byProfile[instance.profile] = records
		}
		// recentSessions is newest first, so the first match in the launch
		// folder is the conversation this instance most likely has open.
		for _, record := range records {
			if record.folder == instance.folder {
				described[index].session = record.session
				break
			}
		}
	}
	return described
}

func profileNamed(profiles []Profile, name string) Profile {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile
		}
	}
	return Profile{}
}

// recordedSession is one conversation read back out of a provider's own log:
// what it was about, where it ran, and when it started. It is what the recent
// list is built from, and it is deliberately not instanceSession — a recorded
// session need not still be running.
type recordedSession struct {
	session instanceSession
	folder  string
	when    time.Time
}

// recentSessionLimit caps a profile's recent list. The panel shows a handful of
// rows; reading more only to throw them away is work done on every refresh.
const recentSessionLimit = 12

// recentSessions lists what a profile has worked on lately, newest first. Both
// providers keep their own transcript on disk, so this reads their logs rather
// than asking either CLI: the answer has to arrive for five profiles at once,
// and a subprocess per profile is not a refresh, it is a stall.
func recentSessions(profile Profile, limit int) []recordedSession {
	if limit <= 0 {
		limit = recentSessionLimit
	}
	var paths []string
	switch profile.Provider {
	case "codex":
		root, err := profileRoot()
		if err != nil {
			return nil
		}
		paths = codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions"))
	case "claude":
		paths = claudeTranscripts(profile)
	default:
		return nil
	}

	records := make([]recordedSession, 0, limit)
	for index, path := range paths {
		if len(records) >= limit || index >= codexSessionSearchLimit {
			break
		}
		var record recordedSession
		var ok bool
		if profile.Provider == "codex" {
			record, ok = readCodexRollout(path, "")
		} else {
			record, ok = readClaudeTranscript(path)
		}
		if ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].when.After(records[j].when) })
	return records
}

// claudeTranscripts lists a profile's Claude conversation logs newest first.
// Claude names them by session id rather than by time, so unlike the Codex
// rollouts these have to be stat-ed to be ordered.
func claudeTranscripts(profile Profile) []string {
	root, err := profileRoot()
	if err != nil {
		return nil
	}
	projects := filepath.Join(root, profile.Name, "claude", "projects")
	type transcript struct {
		path    string
		modTime time.Time
	}
	var files []transcript
	_ = filepath.WalkDir(projects, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, transcript{path: path, modTime: info.ModTime()})
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

// readClaudeTranscript pulls one conversation out of a Claude log. Everything
// needed sits on the first user entry — the folder, the time, and the first
// thing the user typed — so reading stops there rather than decoding a
// transcript that can run to megabytes of tool output.
func readClaudeTranscript(path string) (recordedSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return recordedSession{}, false
	}
	defer file.Close()

	record := recordedSession{session: instanceSession{id: claudeSessionID(path)}}
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	for lines := 0; lines < claudeTranscriptScanLines && scanner.Scan(); lines++ {
		var entry struct {
			Type      string `json:"type"`
			CWD       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
			IsMeta    bool   `json:"isMeta"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "user" {
			continue
		}
		// Where and when come from the first user entry even when its text is
		// something the CLI wrote, so a conversation whose opening lines are all
		// preamble is still placed and dated.
		if !found {
			found = true
			record.folder = entry.CWD
			if when, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				record.when = when
			}
		}
		if entry.IsMeta {
			continue
		}
		if title := summariseTitle(claudeMessageText(entry.Message.Content)); title != "" {
			record.session.title = title
			return record, true
		}
	}
	return record, found
}

// claudeMessageText flattens the two shapes a Claude message body takes: a bare
// string for a plain prompt, and a list of parts once anything is attached. Text
// the CLI injected is skipped, so an attached reminder never becomes the title
// of the message it was attached to.
func claudeMessageText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		if trimmed := strings.TrimSpace(text); !isInjectedPreamble(trimmed) {
			return trimmed
		}
		return ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part.Text); trimmed != "" && !isInjectedPreamble(trimmed) {
			return trimmed
		}
	}
	return ""
}

// injectedPreambles are the blocks both CLIs put in front of a conversation:
// environment dumps, slash-command echoes, harness reminders, and caveats about
// who actually typed what. None of them names a session, and every one of them
// arrives as a user message — so a reader that takes the first user message
// titles half the list "Caveat: The messages below…".
// The two command families are matched by prefix rather than by tag: each has
// several members (caveat, stdout, stderr; name, message, args) and enumerating
// them means a new one silently becomes a session title.
var injectedPreambles = []string{
	"<environment_context>",
	"<local-command-",
	"<command-",
	"<system-reminder>",
	"<user-prompt-submit-hook>",
	"Caveat: The messages below",
}

func isInjectedPreamble(text string) bool {
	for _, preamble := range injectedPreambles {
		if strings.HasPrefix(text, preamble) {
			return true
		}
	}
	return false
}

func claudeSessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

const (
	// claudeTranscriptScanLines bounds how far into a transcript the first user
	// entry is looked for. Claude writes several settings records before it,
	// never a long run of them.
	claudeTranscriptScanLines = 40
	maxTranscriptLine         = 16 * 1024 * 1024
)

// codexRollouts lists Codex session logs newest first. The timestamp is part of
// the file name, so sorting the paths in reverse orders them by recency without
// stat-ing every file.
func codexRollouts(sessionsDir string) []string {
	var paths []string
	_ = filepath.WalkDir(sessionsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	return paths
}

type codexRolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// readCodexRollout returns the session recorded in one rollout file. A non-empty
// folder restricts the answer to that directory and abandons the file as soon as
// its header says otherwise, which is what keeps the hijack picker's scan over
// dozens of rollouts to one decoded line each. Reading stops at the first usable
// title so the model instructions and tool output further down are never decoded.
func readCodexRollout(path, folder string) (recordedSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return recordedSession{}, false
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var record recordedSession
	for lines := 0; lines < 40; lines++ {
		var line codexRolloutLine
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return recordedSession{}, false
		}
		switch line.Type {
		case "session_meta":
			var meta struct {
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
				Timestamp string `json:"timestamp"`
			}
			if err := json.Unmarshal(line.Payload, &meta); err != nil {
				return recordedSession{}, false
			}
			if folder != "" && meta.CWD != folder {
				return recordedSession{}, false
			}
			record.session.id = meta.SessionID
			record.folder = meta.CWD
			if when, err := time.Parse(time.RFC3339Nano, meta.Timestamp); err == nil {
				record.when = when
			}
		case "response_item":
			if record.session.id == "" {
				continue
			}
			if title := codexUserText(line.Payload); title != "" {
				record.session.title = title
				return record, true
			}
		}
	}
	return record, record.session.id != ""
}

// codexUserText pulls the text of a user message, skipping the preamble Codex
// injects before the first thing the user actually typed.
func codexUserText(payload json.RawMessage) string {
	var message struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &message); err != nil || message.Role != "user" {
		return ""
	}
	for _, part := range message.Content {
		text := strings.TrimSpace(part.Text)
		if text == "" || isInjectedPreamble(text) {
			continue
		}
		return summariseTitle(text)
	}
	return ""
}

// summariseTitle reduces a message to one short line so it fits a picker row.
func summariseTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
