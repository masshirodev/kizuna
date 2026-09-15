package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// recentArgumentLimit is how many unpinned argument sets are remembered. It is
// small because the list is read at a glance inside a prompt: past a handful,
// finding the set you meant costs more than typing it again.
const recentArgumentLimit = 5

// argumentSet is one set of extra arguments typed at the p prompt, and the
// account it last went to. The account is kept because the history is shared
// across every provider, and a flag one CLI understands is one another rejects:
// the row has to say where it was used for that to be visible before it runs.
type argumentSet struct {
	Args     []string  `json:"args"`
	Profile  string    `json:"profile,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Used     time.Time `json:"used"`
}

// argumentHistory is what the prompt offers. Pinned sets stay until unpinned,
// in the order they were pinned, so a row does not move under the hand that
// learned where it is. Recent holds the newest first and never repeats a set
// that is already pinned: a pin is already on screen, and listing it twice
// would spend one of the five slots on it.
type argumentHistory struct {
	Pinned []argumentSet `json:"pinned,omitempty"`
	Recent []argumentSet `json:"recent,omitempty"`
}

func argumentHistoryPath() (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(root), "arguments.json"), nil
}

// loadArgumentHistory reads the history, treating a missing file as an empty
// one. A file that is present but unreadable is an error rather than an empty
// history, because every change is written back whole, and the write that
// followed a silent reset would erase the pins the file still holds.
func loadArgumentHistory() (argumentHistory, error) {
	path, err := argumentHistoryPath()
	if err != nil {
		return argumentHistory{}, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return argumentHistory{}, nil
	}
	if err != nil {
		return argumentHistory{}, err
	}
	var history argumentHistory
	if err := json.Unmarshal(body, &history); err != nil {
		return argumentHistory{}, fmt.Errorf("read %s: %w", path, err)
	}
	return history, nil
}

func saveArgumentHistory(history argumentHistory) error {
	path, err := argumentHistoryPath()
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(body, '\n'))
}

// updateArgumentHistory applies one change to what is on disk now, not to the
// copy the prompt opened with, so two TUIs open at once do not undo each
// other's pins by writing back a snapshot that predates them.
func updateArgumentHistory(change func(argumentHistory) argumentHistory) (argumentHistory, error) {
	history, err := loadArgumentHistory()
	if err != nil {
		return argumentHistory{}, err
	}
	history = change(history)
	return history, saveArgumentHistory(history)
}

// entries is every row the prompt lists, pinned first.
func (h argumentHistory) entries() []argumentSet {
	return append(slices.Clone(h.Pinned), h.Recent...)
}

func (h argumentHistory) pinnedIndex(args []string) int {
	return slices.IndexFunc(h.Pinned, func(set argumentSet) bool { return slices.Equal(set.Args, args) })
}

func (h argumentHistory) recentIndex(args []string) int {
	return slices.IndexFunc(h.Recent, func(set argumentSet) bool { return slices.Equal(set.Args, args) })
}

// record notes a launch. A pinned set is updated where it sits; anything else
// moves to the front of recent, and the oldest falls off the end. An empty set
// is not recorded, since running with no extra arguments is what Enter does.
func (h argumentHistory) record(set argumentSet) argumentHistory {
	if len(set.Args) == 0 {
		return h
	}
	set.Args = slices.Clone(set.Args)
	if index := h.pinnedIndex(set.Args); index >= 0 {
		h.Pinned = slices.Clone(h.Pinned)
		h.Pinned[index] = set
		return h
	}
	recent := make([]argumentSet, 0, recentArgumentLimit)
	recent = append(recent, set)
	for _, existing := range h.Recent {
		if len(recent) == recentArgumentLimit {
			break
		}
		if !slices.Equal(existing.Args, set.Args) {
			recent = append(recent, existing)
		}
	}
	h.Recent = recent
	return h
}

// togglePin pins a set that is not pinned and unpins one that is. A set that
// was never recorded can be pinned too, which is how a set gets kept without
// launching it first.
//
// Unpinning puts the set at the front of recent rather than dropping it, so a
// pin removed by mistake is still one row away. That can push the oldest
// recent set off the end, the same as running it again would.
func (h argumentHistory) togglePin(set argumentSet) argumentHistory {
	if len(set.Args) == 0 {
		return h
	}
	if index := h.pinnedIndex(set.Args); index >= 0 {
		unpinned := h.Pinned[index]
		h.Pinned = slices.Delete(slices.Clone(h.Pinned), index, index+1)
		return h.record(unpinned)
	}
	if index := h.recentIndex(set.Args); index >= 0 {
		set = h.Recent[index]
		h.Recent = slices.Delete(slices.Clone(h.Recent), index, index+1)
	}
	set.Args = slices.Clone(set.Args)
	h.Pinned = append(slices.Clone(h.Pinned), set)
	return h
}
