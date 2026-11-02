package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Where an agent is working, as opposed to where it was launched. An agent
// started in ~/projects that then works on one repository in it lists under
// the launch folder, which is no repository at all, and `kizuna peers <repo>`
// misses it. Claude Code's process keeps the launch folder as its cwd and
// `claude agents --json` reports that too, but its transcript records the
// session's working directory on every line, and the tool calls in it name
// the files being worked on. doc/agent-messaging.md, "Where an agent works".

const (
	// workFolderTailBytes is how much of the end of a transcript is read. A
	// tool result can run to hundreds of kilobytes, so this is sized to hold a
	// few dozen turns rather than a few dozen lines.
	workFolderTailBytes = 4 << 20
	// workFolderHintLimit caps the tool calls considered, newest first: enough
	// that one glance at another repository's docs does not move an agent,
	// few enough that what it worked on an hour ago does not hold it in place.
	workFolderHintLimit = 40
)

// transcriptActivity is what the tail of a transcript says about where the
// session is: its newest working directory, and the folders its most recent
// tool calls touched, newest first.
type transcriptActivity struct {
	cwd   string
	hints []string
}

var (
	// A `cd` that starts a command or follows a separator, and git's -C: the
	// two ways an agent that never changes directory names the checkout it
	// means from a shell command.
	cdPattern   = regexp.MustCompile(`(?:^|&&|\|\||[;|(\n])\s*cd\s+("[^"]+"|'[^']+'|[^\s;&|)]+)`)
	gitCPattern = regexp.MustCompile(`\bgit\s+-C\s+("[^"]+"|'[^']+'|[^\s;&|)]+)`)
)

// readTranscriptActivity reads a Claude Code transcript's tail. A missing or
// unreadable file says nothing, and the caller keeps the launch folder.
func readTranscriptActivity(path string) transcriptActivity {
	data, err := readTail(path, workFolderTailBytes)
	if err != nil {
		return transcriptActivity{}
	}
	lines := bytes.Split(data, []byte("\n"))
	var activity transcriptActivity
	for index := len(lines) - 1; index >= 0 && len(activity.hints) < workFolderHintLimit; index-- {
		line := lines[index]
		if len(line) == 0 {
			continue
		}
		// Only lines naming a cwd or a tool call can change the answer, and
		// the rest (tool output above all) are most of the bytes.
		hasCWD := activity.cwd == "" && bytes.Contains(line, []byte(`"cwd"`))
		hasTool := bytes.Contains(line, []byte(`"tool_use"`))
		if !hasCWD && !hasTool {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			CWD     string `json:"cwd"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		// The first line of the tail is usually cut mid-way and fails here,
		// which is the right outcome for it.
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		if activity.cwd == "" && entry.CWD != "" {
			activity.cwd = entry.CWD
		}
		if entry.Type == "assistant" {
			// Calls within one message ran in order, so the last is newest.
			calls := toolCallFolders(entry.Message.Content, entry.CWD)
			for call := len(calls) - 1; call >= 0 && len(activity.hints) < workFolderHintLimit; call-- {
				activity.hints = append(activity.hints, calls[call])
			}
		}
	}
	return activity
}

// readTail returns up to limit bytes from the end of a file.
func readTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := info.Size() - limit
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

// toolCallFolders lists the folders the tool calls in one assistant message
// name, in the order they were made. A file names the folder it is in.
func toolCallFolders(content json.RawMessage, cwd string) []string {
	var items []struct {
		Type  string          `json:"type"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(content, &items) != nil {
		return nil
	}
	var folders []string
	for _, item := range items {
		if item.Type != "tool_use" {
			continue
		}
		var input struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
			Path         string `json:"path"`
			Command      string `json:"command"`
		}
		if json.Unmarshal(item.Input, &input) != nil {
			continue
		}
		for _, file := range []string{input.FilePath, input.NotebookPath} {
			if folder := absoluteFolder(file, cwd); folder != "" {
				folders = append(folders, filepath.Dir(folder))
			}
		}
		if folder := absoluteFolder(input.Path, cwd); folder != "" {
			folders = append(folders, folder)
		}
		folders = append(folders, commandFolders(input.Command, cwd)...)
	}
	return folders
}

// commandFolders reads the directories a shell command moves into or points
// git at. A target built from a variable or a substitution cannot be read
// without running the shell, and is left out rather than guessed.
func commandFolders(command, cwd string) []string {
	if command == "" {
		return nil
	}
	var folders []string
	for _, pattern := range []*regexp.Regexp{cdPattern, gitCPattern} {
		for _, match := range pattern.FindAllStringSubmatch(command, -1) {
			target := strings.Trim(match[1], `"'`)
			if target == "" || target == "-" || strings.ContainsAny(target, "$`*") {
				continue
			}
			if folder := absoluteFolder(target, cwd); folder != "" {
				folders = append(folders, folder)
			}
		}
	}
	return folders
}

// absoluteFolder makes a path from a tool call absolute: a `~` is the home
// folder, and a relative path is relative to the directory the call ran in.
func absoluteFolder(path, cwd string) string {
	switch {
	case path == "":
		return ""
	case path == "~" || strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	case filepath.IsAbs(path):
		return filepath.Clean(path)
	case cwd != "":
		return filepath.Join(cwd, path)
	}
	return ""
}

// workPlace is where an instance is working: the folder, the checkout that
// folder is in, and whether it was inferred from tool calls rather than read
// as the session's working directory.
type workPlace struct {
	folder   string
	checkout gitCheckout
	inferred bool
}

// resolveWorkPlace settles where an instance is working. A working directory
// inside a repository is trusted as it is. One outside any (the folder that
// holds the repositories, typically) gives way to the checkout most of the
// recent tool calls touched, the newest of those winning a tie; with no tool
// call inside a repository either, the working directory stands.
func resolveWorkPlace(cwd string, hints []string, lookup func(string) gitCheckout) workPlace {
	place := workPlace{folder: cwd}
	if cwd != "" {
		place.checkout = lookup(cwd)
		if place.checkout.repo != "" {
			return place
		}
	}
	counts := map[string]int{}
	checkouts := map[string]gitCheckout{}
	var order []string
	for _, hint := range hints {
		folder := existingFolder(hint)
		if folder == "" {
			continue
		}
		checkout := lookup(folder)
		if checkout.repo == "" {
			continue
		}
		if _, seen := counts[checkout.top]; !seen {
			order = append(order, checkout.top)
			checkouts[checkout.top] = checkout
		}
		counts[checkout.top]++
	}
	best := ""
	for _, top := range order {
		if best == "" || counts[top] > counts[best] {
			best = top
		}
	}
	if best == "" {
		return place
	}
	return workPlace{folder: best, checkout: checkouts[best], inferred: true}
}

// existingFolder walks up from a path to the nearest folder that exists, for a
// file since deleted or a folder a command was about to create.
func existingFolder(path string) string {
	for path != "" {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return ""
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
	return ""
}

// liveClaudeTranscript finds a session's transcript, under whichever project
// folder it was filed. Should a copy exist under two, the one written last is
// the live one.
func liveClaudeTranscript(profile Profile, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	root, err := profileRoot()
	if err != nil {
		return ""
	}
	paths, _ := filepath.Glob(filepath.Join(root, profile.Name, "claude", "projects", "*", sessionID+".jsonl"))
	if len(paths) == 0 {
		return ""
	}
	return pathsByModTime(paths)[0]
}
