package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// A lineage record is the one durable fact a handoff produces: that two
// sessions, in two profiles and possibly two providers, are the same piece of
// work. It lives in ai-session's own directory because it is a fact about the
// user's accounts, which is the one thing no provider's state directory has any
// business knowing.
//
// It is a record of a baton pass, not of a fork. The source session is finished
// when it is written — that is why it was handed over — so the chain is read
// forwards and never merged.
type lineageLink struct {
	When            time.Time `json:"when"`
	SourceProfile   string    `json:"source_profile"`
	SourceProvider  string    `json:"source_provider"`
	SourceSessionID string    `json:"source_session_id"`
	SourceTitle     string    `json:"source_title,omitempty"`
	TargetProfile   string    `json:"target_profile"`
	TargetProvider  string    `json:"target_provider"`
	Folder          string    `json:"folder,omitempty"`
	Brief           string    `json:"brief,omitempty"`
}

func lineagePath() (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), "lineage.json"), nil
}

func readLineage() []lineageLink {
	path, err := lineagePath()
	if err != nil {
		return nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var links []lineageLink
	if json.Unmarshal(body, &links) != nil {
		return nil
	}
	return links
}

// appendLineage records one handoff. A failure here is deliberately not fatal
// to the handoff itself: the work has already moved, and refusing to launch the
// destination because a bookkeeping file would not open would be the tool
// getting its own priorities backwards.
func appendLineage(link lineageLink) error {
	path, err := lineagePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	links := append(readLineage(), link)
	body, err := json.MarshalIndent(links, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0600)
}

// handedOff reports which profile a session was handed to, for the marker the
// recent list shows. A session appearing twice keeps the newest, since handing
// the same work over again supersedes the earlier attempt.
func handedOff(links []lineageLink) map[string]lineageLink {
	bySession := make(map[string]lineageLink, len(links))
	for _, link := range links {
		if existing, seen := bySession[link.SourceSessionID]; seen && existing.When.After(link.When) {
			continue
		}
		bySession[link.SourceSessionID] = link
	}
	return bySession
}
