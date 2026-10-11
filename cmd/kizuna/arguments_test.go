package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func argumentLabels(sets []argumentSet) []string {
	labels := make([]string, len(sets))
	for index, set := range sets {
		labels[index] = formatArguments(set.Args)
	}
	return labels
}

func TestArgumentHistoryKeepsTheNewestFiveWithoutRepeats(t *testing.T) {
	history := argumentHistory{}
	for _, args := range [][]string{{"-a"}, {"-b"}, {"-c"}, {"-d"}, {"-e"}, {"-f"}, {"-c"}} {
		history = history.record(argumentSet{Args: args, Profile: "max", Provider: "claude"})
	}
	if got, want := argumentLabels(history.Recent), []string{"-c", "-f", "-e", "-d", "-b"}; !slices.Equal(got, want) {
		t.Fatalf("recent = %v, want %v", got, want)
	}

	if got := history.record(argumentSet{}); len(got.Recent) != recentArgumentLimit {
		t.Fatalf("running with no arguments was recorded: %v", argumentLabels(got.Recent))
	}
}

// The history is shared by every provider, so the same set run from a second
// account is the same row, now naming the account it went to last.
func TestArgumentHistoryIsSharedAcrossProviders(t *testing.T) {
	history := argumentHistory{}.
		record(argumentSet{Args: []string{"--model", "x"}, Profile: "max", Provider: "claude"}).
		record(argumentSet{Args: []string{"--search"}, Profile: "codex-work", Provider: "codex"}).
		record(argumentSet{Args: []string{"--model", "x"}, Profile: "go", Provider: "opencode"})
	if len(history.Recent) != 2 {
		t.Fatalf("recent = %v, want one row per set", argumentLabels(history.Recent))
	}
	if first := history.Recent[0]; first.Provider != "opencode" || first.Profile != "go" {
		t.Fatalf("the repeated set does not name where it last went: %+v", first)
	}
}

func TestPinnedArgumentsStayPutAndOutOfRecent(t *testing.T) {
	history := argumentHistory{}
	for _, args := range []string{"-a", "-b", "-c"} {
		history = history.record(argumentSet{Args: []string{args}, Profile: "max", Provider: "claude"})
	}
	history = history.togglePin(argumentSet{Args: []string{"-b"}})
	history = history.togglePin(argumentSet{Args: []string{"-a"}})
	if got, want := argumentLabels(history.Pinned), []string{"-b", "-a"}; !slices.Equal(got, want) {
		t.Fatalf("pinned = %v, want pin order %v", got, want)
	}
	if got, want := argumentLabels(history.Recent), []string{"-c"}; !slices.Equal(got, want) {
		t.Fatalf("recent = %v, want pinned sets out of it", got)
	}
	if history.Pinned[0].Profile != "max" {
		t.Fatalf("pinning a recent set lost where it was used: %+v", history.Pinned[0])
	}

	// Running a pinned set updates it in place: neither its slot in the pins nor
	// one of the recent ones.
	history = history.record(argumentSet{Args: []string{"-a"}, Profile: "codex-work", Provider: "codex"})
	if got := argumentLabels(history.Pinned); !slices.Equal(got, []string{"-b", "-a"}) || history.Pinned[1].Provider != "codex" {
		t.Fatalf("running a pin moved it or did not note the run: %+v", history.Pinned)
	}
	if len(history.Recent) != 1 {
		t.Fatalf("running a pin also listed it as recent: %v", argumentLabels(history.Recent))
	}

	for _, args := range []string{"-d", "-e", "-f", "-g", "-h"} {
		history = history.record(argumentSet{Args: []string{args}})
	}
	if len(history.Pinned) != 2 {
		t.Fatalf("pins aged out with the recent sets: %v", argumentLabels(history.Pinned))
	}
}

func TestUnpinningPutsTheSetBackAtTheFrontOfRecent(t *testing.T) {
	history := argumentHistory{}.
		record(argumentSet{Args: []string{"-a"}}).
		record(argumentSet{Args: []string{"-b"}})
	history = history.togglePin(argumentSet{Args: []string{"-a"}})
	history = history.togglePin(argumentSet{Args: []string{"-a"}})
	if len(history.Pinned) != 0 {
		t.Fatalf("pinned = %v, want it unpinned", argumentLabels(history.Pinned))
	}
	if got, want := argumentLabels(history.Recent), []string{"-a", "-b"}; !slices.Equal(got, want) {
		t.Fatalf("recent = %v, want %v", got, want)
	}
}

func TestPinningASetThatNeverRanKeepsNoAccount(t *testing.T) {
	history := argumentHistory{}.togglePin(argumentSet{Args: []string{"--model", "gpt 5"}})
	if len(history.Pinned) != 1 || history.Pinned[0].Profile != "" || !history.Pinned[0].Used.IsZero() {
		t.Fatalf("pinned = %+v, want one set that has not run", history.Pinned)
	}
}

func TestArgumentHistoryRoundTripsThroughItsFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	when := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if _, err := updateArgumentHistory(func(history argumentHistory) argumentHistory {
		return history.record(argumentSet{Args: []string{"--model", "gpt 5"}, Profile: "codex-work", Provider: "codex", Used: when})
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadArgumentHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Recent) != 1 || !slices.Equal(history.Recent[0].Args, []string{"--model", "gpt 5"}) || !history.Recent[0].Used.Equal(when) {
		t.Fatalf("history did not survive a round trip: %+v", history)
	}
	path := filepath.Join(root, appName, "arguments.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("arguments.json mode = %v, want private: a set can carry a prompt", info.Mode().Perm())
	}
}

// Every change writes the whole file back, so a file that cannot be read has
// to stop the write rather than be replaced by an empty history.
func TestUnreadableArgumentHistoryIsNotOverwritten(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, appName, "arguments.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"pinned": [`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := updateArgumentHistory(func(history argumentHistory) argumentHistory {
		return history.record(argumentSet{Args: []string{"-a"}})
	})
	if err == nil || !strings.Contains(err.Error(), "arguments.json") {
		t.Fatalf("err = %v, want the unreadable file named", err)
	}
	if body, _ := os.ReadFile(path); string(body) != `{"pinned": [` {
		t.Fatalf("the unreadable file was replaced: %q", body)
	}
}
