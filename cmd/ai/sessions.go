package main

import (
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
		if session, ok := readCodexRollout(path, folder); ok {
			return session
		}
	}
	return instanceSession{}
}

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

// readCodexRollout returns the session recorded in one rollout file when it
// belongs to folder. Reading stops at the first usable title so the model
// instructions and tool output further down the file are never decoded.
func readCodexRollout(path, folder string) (instanceSession, bool) {
	file, err := os.Open(path)
	if err != nil {
		return instanceSession{}, false
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var session instanceSession
	for lines := 0; lines < 40; lines++ {
		var line codexRolloutLine
		if err := decoder.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return instanceSession{}, false
		}
		switch line.Type {
		case "session_meta":
			var meta struct {
				SessionID string `json:"session_id"`
				CWD       string `json:"cwd"`
			}
			if err := json.Unmarshal(line.Payload, &meta); err != nil || meta.CWD != folder {
				return instanceSession{}, false
			}
			session.id = meta.SessionID
		case "response_item":
			if session.id == "" {
				continue
			}
			if title := codexUserText(line.Payload); title != "" {
				session.title = title
				return session, true
			}
		}
	}
	return session, session.id != ""
}

// codexUserText pulls the text of a user message, skipping the environment
// preamble Codex injects before the first thing the user actually typed.
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
		if text == "" || strings.HasPrefix(text, "<environment_context>") {
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
