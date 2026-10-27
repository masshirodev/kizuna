package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The kizuna-inbox mod is a Claude Code plugin that delivers a message to an
// idle session as a prompt of its own, so nothing is typed into the terminal:
// a typed nudge lands on top of whatever the user is typing, and Claude Code
// can take its Enter for part of a paste and leave it unsent. The mod is
// embedded here and written into each Claude profile by `kizuna integrate
// messaging`; doc/agent-messaging.md is the account.

//go:embed all:inboxmod
var inboxModFiles embed.FS

const (
	inboxModName = "kizuna-inbox"
	// modHeartbeatFile sits in the instance directory. The mod touches it on
	// every poll, so a fresh one says this very session has the mod running,
	// which the profile's settings cannot: a session started before the mod
	// was installed has the hooks and still needs the typed nudge.
	modHeartbeatFile = "mod-alive"
	// modHeartbeatFresh is generous beside the mod's two-second poll, so a
	// slow `kizuna` start or a busy machine does not flip a session back to
	// being typed into.
	modHeartbeatFresh = 15 * time.Second
)

// inboxModDir is where Claude Code loads the mod from: a plugin folder under
// the config dir's skills/ is auto-loaded as <name>@skills-dir.
func inboxModDir(profile Profile) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, profile.Name, "claude", "skills", inboxModName), nil
}

// installInboxMod writes the embedded mod into a Claude profile. A file that
// already holds the same bytes is left alone, because Claude Code reloads a
// skills-dir plugin when its files change, and a file the embedded copy no
// longer has is removed.
func installInboxMod(profile Profile) error {
	dir, err := inboxModDir(profile)
	if err != nil {
		return err
	}
	wanted := map[string]bool{}
	err = fs.WalkDir(inboxModFiles, "inboxmod", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel("inboxmod", path)
		if err != nil {
			return err
		}
		wanted[rel] = true
		data, err := inboxModFiles.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, data) {
			return nil
		}
		return writeFileAtomic(target, data)
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !wanted[rel] {
			return os.Remove(path)
		}
		return nil
	})
}

// refreshInboxMod brings the mod in a profile with messaging on up to the
// copy this binary carries, before a launch reads it: a profile integrated by
// an older kizuna gets the mod at all, and an upgraded kizuna's changes reach
// the next session without another `kizuna integrate messaging`. A failure is
// not worth refusing the launch for; the hooks and typed nudges still work.
func refreshInboxMod(profile Profile) {
	if profile.Provider == "claude" && messagingDelivery(profile) == "hooks" {
		_ = installInboxMod(profile)
	}
}

func removeInboxMod(profile Profile) error {
	dir, err := inboxModDir(profile)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func inboxModInstalled(profile Profile) bool {
	dir, err := inboxModDir(profile)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	return err == nil
}

// modAlive reports whether the instance's mod has polled recently.
func modAlive(lockDir string, now time.Time) bool {
	info, err := os.Stat(filepath.Join(lockDir, modHeartbeatFile))
	return err == nil && now.Sub(info.ModTime()) < modHeartbeatFresh
}

type modPoll struct {
	Unread int    `json:"unread"`
	Text   string `json:"text,omitempty"`
}

// answerModPoll is `kizuna inbox --mod [--drain]`, the mod's one call per
// tick: refresh the heartbeat, then report how much is unread, or with drain
// hand over the messages rendered as the hooks render them.
func answerModPoll(lockDir string, drain bool, stdout io.Writer) error {
	if lockDir == "" {
		return errors.New("this is not running inside a kizuna-launched session, so it has no inbox to poll")
	}
	heartbeat := filepath.Join(lockDir, modHeartbeatFile)
	now := time.Now()
	if err := os.Chtimes(heartbeat, now, now); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(heartbeat, nil, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	var poll modPoll
	if drain {
		if messages := drainInbox(lockDir); len(messages) > 0 {
			poll.Text = renderMessages(messages)
		}
	} else {
		poll.Unread = len(inboxMessages(lockDir))
	}
	return json.NewEncoder(stdout).Encode(poll)
}
