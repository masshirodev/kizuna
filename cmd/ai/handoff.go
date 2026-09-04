package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A handoff moves work, not a conversation. Nothing is copied into another
// provider's state directory: the outgoing session is read, reduced to what a
// fresh agent needs to carry on, and written as a brief that the incoming CLI
// is pointed at. The conversation itself stays where it was recorded.
//
// The reduction is the point. A Claude transcript on this machine measured
// 1140 KB, of which the prose the user and the model actually exchanged was
// about 5 KB — the rest is tool traffic and the CLI's own bookkeeping, none of
// which survives a change of provider anyway. Handing over the folder and
// saying "continue" makes the next agent read all of it.

// handoffMessage is one thing that was said, reduced to who said it and what.
type handoffMessage struct {
	fromUser bool
	text     string
}

// briefLimits keep a pathological transcript from producing a brief that costs
// as much as the transcript did. They are generous: the whole point is that a
// conversation's prose is small, and a brief that hits these is a signal the
// session was long enough to be worth trimming by hand before it is sent.
const (
	maxBriefPrompt  = 2000
	maxBriefPrompts = 40
	maxBriefNotes   = 3
	maxBriefNote    = 800
	// minBriefNote is what separates a conclusion from narration. The model's
	// last few messages are usually its shortest — "Now the modal views:" is
	// what sits between two tool calls — so taking the tail by position alone
	// reliably picks the least informative thing it said. Length is a crude
	// proxy for substance, but it is the one available without asking a model,
	// and asking one is what a handoff exists to avoid.
	minBriefNote = 400
)

// sessionBrief is everything the outgoing session contributes. Git state is
// collected separately, because it is a fact about the folder now rather than
// about the conversation then.
type sessionBrief struct {
	source     recordedSession
	profile    Profile
	prompts    []string
	notes      []string
	transcript string
	truncated  bool
}

// readSessionMessages reads a whole recorded conversation, not just far enough
// to title it. It is only ever called for one session at a time, on a keypress
// the user pressed on purpose, so unlike the panel readers it can afford the
// whole file.
func readSessionMessages(profile Profile, record recordedSession) ([]handoffMessage, string, error) {
	path, err := transcriptPath(profile, record)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()

	var messages []handoffMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxTranscriptLine)
	for scanner.Scan() {
		if message, ok := decodeHandoffLine(profile.Provider, scanner.Bytes()); ok {
			messages = append(messages, message)
		}
	}
	return messages, path, scanner.Err()
}

// decodeHandoffLine pulls one said thing out of either provider's log. Both
// shapes reduce to the same pair, which is what lets a Codex session hand off
// to Claude and back.
func decodeHandoffLine(provider string, line []byte) (handoffMessage, bool) {
	switch provider {
	case "claude":
		var entry struct {
			Type    string `json:"type"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.IsMeta {
			return handoffMessage{}, false
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			return handoffMessage{}, false
		}
		text := claudeMessageText(entry.Message.Content)
		if text == "" {
			return handoffMessage{}, false
		}
		return handoffMessage{fromUser: entry.Type == "user", text: text}, true
	case "codex":
		var entry codexRolloutLine
		if json.Unmarshal(line, &entry) != nil || entry.Type != "response_item" {
			return handoffMessage{}, false
		}
		var message struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(entry.Payload, &message) != nil || message.Type != "message" {
			return handoffMessage{}, false
		}
		if message.Role != "user" && message.Role != "assistant" {
			return handoffMessage{}, false
		}
		for _, part := range message.Content {
			if text := userText(part.Text); text != "" {
				return handoffMessage{fromUser: message.Role == "user", text: text}, true
			}
		}
	}
	return handoffMessage{}, false
}

// transcriptPath finds the file a recorded session was read out of. The recent
// list carries the id and the folder but not the path, because until now
// nothing needed to open the same file twice.
func transcriptPath(profile Profile, record recordedSession) (string, error) {
	if record.session.id == "" {
		return "", errors.New("this session has no id to read back")
	}
	switch profile.Provider {
	case "claude":
		for _, path := range claudeTranscripts(profile) {
			if claudeSessionID(path) == record.session.id {
				return path, nil
			}
		}
	case "codex":
		root, err := profileRoot()
		if err != nil {
			return "", err
		}
		for _, path := range codexRollouts(filepath.Join(root, profile.Name, "codex", "sessions")) {
			if strings.Contains(filepath.Base(path), record.session.id) {
				return path, nil
			}
		}
	default:
		return "", fmt.Errorf("provider %q records nothing to hand off", profile.Provider)
	}
	return "", errors.New("the transcript for this session is no longer on disk")
}

// buildBrief reduces a recorded session to what the next agent needs. The user
// turns are kept in full and in order, because they are the intent and there is
// no way to infer them back; the model's own prose is kept only at the tail,
// where the conclusions are.
func buildBrief(profile Profile, record recordedSession) (sessionBrief, error) {
	messages, path, err := readSessionMessages(profile, record)
	if err != nil {
		return sessionBrief{}, err
	}
	brief := sessionBrief{source: record, profile: profile, transcript: path}
	var notes []string
	for _, message := range messages {
		if message.fromUser {
			if len(brief.prompts) >= maxBriefPrompts {
				brief.truncated = true
				continue
			}
			brief.prompts = appendPrompt(brief.prompts, clip(message.text, maxBriefPrompt))
			continue
		}
		if len([]rune(message.text)) >= minBriefNote {
			notes = append(notes, clip(message.text, maxBriefNote))
		}
	}
	if len(notes) > maxBriefNotes {
		notes = notes[len(notes)-maxBriefNotes:]
	}
	brief.notes = notes
	if len(brief.prompts) == 0 {
		return brief, errors.New("nothing was asked in this session; there is nothing to hand over")
	}
	return brief, nil
}

// appendPrompt drops the half-written copy of a request. Interrupting a turn
// and carrying on records the message twice — once as it stood when the tool
// noticed it and once complete — and the longer one is what was actually asked.
//
// The comparison ignores trailing punctuation, because the short copy is
// terminated rather than cut: a message that really ends "…on another; i dont
// mind" is recorded mid-flight as "…on another." That full stop is the only
// difference, and it is enough to defeat a plain prefix test.
func appendPrompt(prompts []string, prompt string) []string {
	if len(prompts) == 0 {
		return append(prompts, prompt)
	}
	previous := prompts[len(prompts)-1]
	if continues(previous, prompt) {
		prompts[len(prompts)-1] = prompt
		return prompts
	}
	if continues(prompt, previous) {
		return prompts
	}
	return append(prompts, prompt)
}

// continues reports whether full is the finished version of partial.
func continues(partial, full string) bool {
	partial = strings.TrimRight(partial, " \t\n.,;:…")
	return partial != "" && len(full) > len(partial) && strings.HasPrefix(full, partial)
}

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n\n[…trimmed]"
}

// gitState is what the folder says about the work right now. It is collected at
// handoff time rather than read out of the transcript on purpose: reconstructing
// which files were touched from the tool calls gave two files for a session that
// edited six, because the work went through shell heredocs and no tool argument
// ever named a path. What the repository says does not depend on how the
// previous agent happened to hold its tools.
type gitState struct {
	repo    bool
	branch  string
	status  string
	diffs   string
	commits string
}

const gitStateTimeout = 5 * time.Second

func collectGitState(folder string) gitState {
	if folder == "" {
		return gitState{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitStateTimeout)
	defer cancel()
	inside := strings.TrimSpace(gitOutput(ctx, folder, "rev-parse", "--is-inside-work-tree"))
	if inside != "true" {
		return gitState{}
	}
	return gitState{
		repo:    true,
		branch:  strings.TrimSpace(gitOutput(ctx, folder, "rev-parse", "--abbrev-ref", "HEAD")),
		status:  limitLines(gitOutput(ctx, folder, "status", "--porcelain=v1"), 40),
		diffs:   limitLines(gitOutput(ctx, folder, "diff", "--stat", "HEAD"), 40),
		commits: limitLines(gitOutput(ctx, folder, "log", "--oneline", "-5"), 5),
	}
}

func gitOutput(ctx context.Context, folder string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = folder
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(output)
}

func limitLines(text string, limit int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > limit {
		lines = append(lines[:limit], fmt.Sprintf("… and %d more", len(lines)-limit))
	}
	// Only the trailing blank goes: git status leads each line with two status
	// columns, and trimming the left edge would silently shift the first row
	// out of alignment with every row under it.
	return strings.TrimRight(strings.Join(lines, "\n"), " \t\n")
}

// renderBrief writes the markdown the incoming agent reads. It is addressed to
// that agent rather than to the user, and it says plainly that the work came
// from somewhere else — a brief that pretends to be the agent's own memory
// invites it to claim decisions it was not there for.
func renderBrief(brief sessionBrief, state gitState, now time.Time) string {
	var out strings.Builder
	title := brief.source.session.title
	if title == "" {
		title = "an untitled session"
	}
	fmt.Fprintf(&out, "# Handoff: %s\n\n", title)
	fmt.Fprintf(&out, "You are picking up work started in another CLI. It ran as the `%s` profile (%s)",
		brief.profile.Name, brief.profile.Provider)
	if !brief.source.when.IsZero() {
		fmt.Fprintf(&out, ", beginning %s", brief.source.when.Local().Format("2 Jan 2006 15:04"))
	}
	fmt.Fprintf(&out, ", in `%s`.\nHanded over %s.\n\n", brief.source.folder, now.Local().Format("2 Jan 2006 15:04"))
	out.WriteString("The reasoning behind the work below is not included and you are not expected to reproduce it. Read the repository for the current state, and ask if something here contradicts what you find.\n\n")

	out.WriteString("## What was asked, in order\n\n")
	for _, prompt := range brief.prompts {
		fmt.Fprintf(&out, "- %s\n", indentBlock(prompt))
	}
	if brief.truncated {
		fmt.Fprintf(&out, "\n_Only the first %d requests are listed._\n", maxBriefPrompts)
	}

	if len(brief.notes) > 0 {
		out.WriteString("\n## Where the previous agent left off\n\n")
		for _, note := range brief.notes {
			fmt.Fprintf(&out, "- %s\n", indentBlock(note))
		}
	}

	if state.repo {
		out.WriteString("\n## The repository, as of the handoff\n\n")
		if state.branch != "" {
			fmt.Fprintf(&out, "Branch `%s`.\n\n", state.branch)
		}
		writeBlock(&out, "Uncommitted changes", state.status, "working tree clean")
		writeBlock(&out, "Diff against HEAD", state.diffs, "no diff against HEAD")
		writeBlock(&out, "Recent commits", state.commits, "")
	}

	if brief.transcript != "" {
		out.WriteString("\n## If you need more\n\n")
		fmt.Fprintf(&out, "The full transcript is at `%s`. It is large and mostly tool traffic — read it only if this brief leaves a real gap, and grep it rather than opening it whole.\n", brief.transcript)
	}
	return out.String()
}

func writeBlock(out *strings.Builder, heading, body, whenEmpty string) {
	if body == "" {
		if whenEmpty == "" {
			return
		}
		fmt.Fprintf(out, "%s: %s.\n\n", heading, whenEmpty)
		return
	}
	fmt.Fprintf(out, "%s:\n\n```\n%s\n```\n\n", heading, body)
}

// indentBlock keeps a multi-line request inside its bullet.
func indentBlock(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n  ")
}

// writeBriefFile puts the brief somewhere both this tool and the incoming CLI
// can reach it. It lives under ai-session's own directory and never inside a
// provider's state, which is the line this feature does not cross.
func writeBriefFile(brief sessionBrief, body string) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(root), "handoffs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, brief.source.session.id+".md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return "", err
	}
	return path, nil
}

// handoffPrompt is what the incoming CLI is actually started with. The brief
// goes in as a path rather than as text: a megabyte of markdown in argv is a
// different failure every shell, and a file can be re-read after a compaction
// while an opening prompt cannot.
func handoffPrompt(path string) string {
	return "Read the handoff brief at " + path + " and continue that work. Start by confirming what you found there against the repository."
}

// promptArgs spells the opening prompt for each provider that can take one.
// Providers absent from this list can still be handed off *from* — the brief is
// written either way — but they have to be started by hand, which is said
// rather than silently half-done.
func promptArgs(provider, prompt string) ([]string, error) {
	switch provider {
	case "claude", "codex":
		return []string{prompt}, nil
	default:
		return nil, fmt.Errorf("cannot open %q with a prompt; the brief is written, start it by hand", provider)
	}
}

// handoffDestinations ranks where the work could go. Quota is the whole reason
// this feature exists, so the account with the most of it left leads; profiles
// whose quota is unknown sort last rather than first, because an unknown
// remainder is not a good one.
func handoffDestinations(profiles []Profile, source Profile, usage map[string]usageRemaining) []Profile {
	var candidates []Profile
	for _, profile := range profiles {
		if profile.Name == source.Name {
			continue
		}
		if _, err := promptArgs(profile.Provider, ""); err != nil {
			continue
		}
		candidates = append(candidates, profile)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return headroom(usage[candidates[i].Name]) > headroom(usage[candidates[j].Name])
	})
	return candidates
}

// headroom scores an account by the window that will run out first. A weekly
// quota with room is no help when the five-hour window is spent, which is the
// exact moment this feature is reached for.
func headroom(usage usageRemaining) int {
	switch {
	case usage.FiveHour.Known && usage.Weekly.Known:
		return min(usage.FiveHour.Percent, usage.Weekly.Percent)
	case usage.FiveHour.Known:
		return usage.FiveHour.Percent
	case usage.Weekly.Known:
		return usage.Weekly.Percent
	default:
		return -1
	}
}
