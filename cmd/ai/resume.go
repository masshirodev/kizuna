package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

// `ai <profile> resume` opens the conversation picker rather than the
// provider's own resume flow, so every provider gets the same offer: the
// recent-sessions modal, which has read every folder the account has worked
// in, instead of a picker scoped to the folder it was started in (or, for
// OpenCode and Antigravity, no picker at all — upstream only continues the
// most recent session). `ai <profile> resume <id>` skips the picker and
// reopens one conversation directly.
//
// A single trailing "resume" is the whole trigger. Anything longer that does
// not match `resume <id>` is passed through to the provider untouched, so a
// prompt that happens to start with the word keeps meaning what it said.

// resumeLookupLimit bounds the transcript scan when finding one session by id.
// It is wider than the panel's own limit: the picker shows a handful of rows,
// but an id typed at the prompt may name an older conversation.
const resumeLookupLimit = 200

// parseResumeRequest reports whether rest asks for the resume flow: exactly
// ["resume"], or ["resume", <id>]. Anything else is an ordinary launch.
func parseResumeRequest(rest []string) (id string, isResume bool) {
	if len(rest) == 0 || rest[0] != "resume" {
		return "", false
	}
	if len(rest) == 1 {
		return "", true
	}
	if len(rest) == 2 {
		return rest[1], true
	}
	return "", false
}

// resumeProfile runs the resume flow for a profile: the picker with no id, or
// one conversation directly with one.
func resumeProfile(profile Profile, rest []string, stdout, stderr io.Writer) error {
	id, _ := parseResumeRequest(rest)
	if id == "" {
		return runTUIResume(profile.Name)
	}
	return resumeSessionByID(profile, id, stdout, stderr)
}

// resumeSessionByID reopens one conversation by id in the folder it ran in,
// mirroring what the resume picker does with the row it is sitting on: the id
// names the conversation, and the provider looks for that id under the folder
// it belongs to, so launching anywhere else reaches a different conversation
// or none at all.
func resumeSessionByID(profile Profile, id string, stdout, stderr io.Writer) error {
	for _, record := range recentSessions(profile, resumeLookupLimit) {
		if record.session.id != id {
			continue
		}
		return resumeRecordedSession(profile, record, stdout, stderr)
	}
	// Unknown to the transcripts: hand the id to the provider and let it
	// answer. An id from another folder, or another account's history, is the
	// provider's to report on rather than this launcher's to refuse.
	args, err := reopenArgs(profile.Provider, instanceSession{id: id})
	if err != nil {
		return err
	}
	return launch(profile, args, stdout, stderr)
}

// resumeRecordedSession reopens a conversation read back off disk, in the
// folder it belongs to. It shares its guards with the picker's own Enter key:
// a moved folder is reported rather than swapped, and an OpenCode session
// still living in a running instance's private copy waits for the merge.
func resumeRecordedSession(profile Profile, record recordedSession, stdout, stderr io.Writer) error {
	workdir, err := os.Getwd()
	if err != nil {
		return err
	}
	folder, err := recordedFolder(record, workdir)
	if err != nil {
		return err
	}
	if profile.Provider == "opencode" && !opencodeSessionInProfileDB(profile, record.session.id) {
		return fmt.Errorf("that session is still in a running instance and has not merged yet; stop that instance first")
	}
	args, err := reopenArgs(profile.Provider, record.session)
	if err != nil {
		return err
	}
	return launchInFolder(profile, profileRunArgs(profile, args), folder, stdout, stderr)
}

// launchInFolder runs a profile's command in an explicit folder. A resume
// belongs to the folder its session ran in, not to wherever the launcher
// happens to be pointed — which is why this exists beside launch, which
// inherits the current directory.
func launchInFolder(profile Profile, args []string, folder string, stdout, stderr io.Writer) error {
	workdir, err := ensureProfileState(profile)
	if err != nil {
		return err
	}
	cmd := exec.Command(profile.Command, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Dir = folder
	lockDir, unlock, err := acquireProfileRunLock(profile, workdir)
	if err != nil {
		return err
	}
	cmd.Env = launchEnvironment(profile, workdir, lockDir, os.Environ())
	cmd, err = applyIndicator(cmd, profile, lockDir)
	if err != nil {
		unlock()
		return err
	}
	return runCommandWithLock(cmd, lockDir, unlock, sessionTitle(profile))
}

// runTUIResume opens the cockpit with the resume picker already up for the
// named profile. With nothing recorded — an account that has not run yet, or
// a provider whose transcripts are not read — it falls back to the provider's
// own resume flow in the current folder, which is the same offer the R key
// makes from inside the TUI.
func runTUIResume(name string) error {
	model, err := resumeTUIModel(name)
	if err != nil {
		return err
	}
	if model == nil {
		return runResumeFallback(name)
	}
	_, err = tea.NewProgram(*model, tea.WithAltScreen()).Run()
	return err
}

// runResumeFallback launches the provider's own resume flow: the picker for
// Claude and Codex, the most recent session for OpenCode, Antigravity, and
// DeepSeek, which have no picker upstream to open.
func runResumeFallback(name string) error {
	configPath, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	profile, err := resolveProfile(cfg, name)
	if err != nil {
		return err
	}
	args, err := resumeArgs(profile.Provider)
	if err != nil {
		return err
	}
	// launch applies the defaults exactly once; passing the provider's resume
	// arguments through it is what used to double them.
	return launch(profile, args, os.Stdout, os.Stderr)
}

// resumeTUIModel builds the cockpit with the resume picker open on the named
// profile, returning a nil model when there is nothing to pick. It is
// separate from the tea run so tests can assert the opening state without a
// terminal.
func resumeTUIModel(name string) (*tuiModel, error) {
	configPath, err := configPath()
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}
	profile, err := resolveProfile(cfg, name)
	if err != nil {
		return nil, err
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	recent := recentSessions(profile, recentSessionLimit)
	if len(recent) == 0 {
		return nil, nil
	}
	profiles := sortedProfiles(cfg.Profiles)
	cursor := 0
	for index, candidate := range profiles {
		if candidate.Name == profile.Name {
			cursor = index
			break
		}
	}
	model := tuiModel{
		configPath: configPath,
		profiles:   profiles,
		cursor:     cursor,
		mode:       tuiRecent,
		workingDir: workingDir,
		autoSwap:   cfg.Settings.AutoSwap,
		lineage:    handedOff(readLineage()),
		recent:     recent,
		previews:   map[string]sessionPreview{},
	}
	// The pane beside the list reads the row under the cursor, and the cursor
	// starts on the first row, so read it now rather than leaving the pane on
	// "reading the transcript…" until the cursor first moves.
	preview := readSessionPreview(profile, recent[0])
	model.preview = preview
	model.previews[recent[0].session.id] = preview
	for _, note := range reclaimStrayIsolatedInstances(cfg) {
		model.log = append([]logEntry{{kind: statusOK, text: note}}, model.log...)
	}
	return &model, nil
}
