package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInboxModInstallsIdempotentlyAndComesOff(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	dir := filepath.Join(root, appName, "profiles", "max", "claude", "skills", inboxModName)

	if err := installInboxMod(profile); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.ts", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("installed mod lacks %s: %v", rel, err)
		}
	}
	if !inboxModInstalled(profile) {
		t.Fatal("an installed mod should read as installed")
	}

	// A file the embedded copy does not carry goes; one already holding the
	// same bytes is not rewritten, so Claude Code has nothing to reload.
	stale := filepath.Join(dir, "hooks", "old.ts")
	if err := os.WriteFile(stale, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, ".claude-plugin", "plugin.json")
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(manifest, past, past); err != nil {
		t.Fatal(err)
	}
	if err := installInboxMod(profile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a file the mod no longer has should be removed, stat = %v", err)
	}
	if info, err := os.Stat(manifest); err != nil || !info.ModTime().Equal(past) {
		t.Fatalf("an unchanged file was rewritten (%v)", err)
	}

	if err := removeInboxMod(profile); err != nil {
		t.Fatal(err)
	}
	if inboxModInstalled(profile) {
		t.Fatal("the mod should be gone after removal")
	}
}

func TestIntegrateMessagingCarriesTheModForClaudeOnly(t *testing.T) {
	isolatePeers(t)
	claude := Profile{Name: "max", Provider: "claude", Command: "claude"}
	codex := Profile{Name: "work", Provider: "codex", Command: "codex"}

	for _, profile := range []Profile{claude, codex} {
		if err := integrateMessaging(profile, false, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if !inboxModInstalled(claude) {
		t.Fatal("a Claude profile should get the mod with the hooks")
	}
	if inboxModInstalled(codex) {
		t.Fatal("only Claude Code loads the mod")
	}

	if err := integrateMessaging(claude, true, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if inboxModInstalled(claude) {
		t.Fatal("--off should remove the mod with the hooks")
	}
	// A launch must not bring back what --off took away.
	refreshInboxMod(claude)
	if inboxModInstalled(claude) {
		t.Fatal("a profile with messaging off should not get the mod at launch")
	}
}

func TestModPollRefreshesTheHeartbeatAndDrainsOnce(t *testing.T) {
	lockDir := t.TempDir()
	if modAlive(lockDir, time.Now()) {
		t.Fatal("an instance whose mod never polled is not alive")
	}
	if err := deliverMessage(lockDir, message{From: "pro2/run-3", Sent: time.Now(), Text: "the resize bug too"}); err != nil {
		t.Fatal(err)
	}

	poll := func(drain bool) modPoll {
		t.Helper()
		var out bytes.Buffer
		if err := answerModPoll(lockDir, drain, &out); err != nil {
			t.Fatal(err)
		}
		var p modPoll
		if err := json.Unmarshal(out.Bytes(), &p); err != nil {
			t.Fatalf("poll answer %q: %v", out.String(), err)
		}
		return p
	}

	if p := poll(false); p.Unread != 1 || p.Text != "" {
		t.Fatalf("a busy poll = %+v, want the count and no text", p)
	}
	if !modAlive(lockDir, time.Now()) {
		t.Fatal("a poll should leave a fresh heartbeat")
	}
	if modAlive(lockDir, time.Now().Add(modHeartbeatFresh)) {
		t.Fatal("a heartbeat older than the freshness window is a dead mod")
	}

	p := poll(true)
	if !strings.Contains(p.Text, "the resize bug too") || !strings.Contains(p.Text, "not from the user") || !strings.Contains(p.Text, "kizuna send pro2/run-3") {
		t.Fatalf("a drain should hand over the message as the hooks render it, got %q", p.Text)
	}
	if p := poll(true); p.Text != "" {
		t.Fatalf("a drained inbox delivered again: %q", p.Text)
	}

	if err := answerModPoll("", true, &bytes.Buffer{}); err == nil {
		t.Fatal("a poll outside a kizuna-launched session should fail, so the mod stops polling")
	}
}

func TestSendNeverTypesIntoAnAgentWhoseModIsAlive(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	if err := setMessagingHooks(profile, "/bin/kizuna "+messagingHookArgs, true); err != nil {
		t.Fatal(err)
	}
	lockDir := livePeerInstance(t, root, profile, "run-1", "/work/ranma")
	cfg := Config{Profiles: []Profile{profile}}

	peer := func() peer {
		t.Helper()
		for _, p := range collectPeers(cfg) {
			if p.lockDir == lockDir {
				return p
			}
		}
		t.Fatal("the instance was not listed")
		return peer{}
	}
	if got := peer().Delivery; got != "hooks" {
		t.Fatalf("without a heartbeat delivery = %q, want hooks", got)
	}
	if err := answerModPoll(lockDir, false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	p := peer()
	if p.Delivery != "mod" {
		t.Fatalf("with a fresh heartbeat delivery = %q, want mod", p.Delivery)
	}

	// The state comes from `claude agents`, which this test has no session
	// for, so set it by hand: idle is the case that used to type.
	p.State = "idle"
	if shouldTypeNudge(p, "auto") {
		t.Fatal("an idle agent whose mod is alive must not be typed into")
	}
	if !shouldTypeNudge(p, "always") {
		t.Fatal("--type still types, whatever the delivery")
	}
	p.Delivery = "hooks"
	if !shouldTypeNudge(p, "auto") {
		t.Fatal("an idle agent without the mod is still woken by typing")
	}
	p.Delivery = "mod"
	if !strings.Contains(deliverySummary(p, false), "kizuna-inbox") {
		t.Fatalf("summary %q should say the mod hands it over", deliverySummary(p, false))
	}
	if _, err := sendMessage(cfg, "max", "hello", sendOptions{typeMode: "auto"}); err != nil {
		t.Fatal(err)
	}
	if len(inboxMessages(lockDir)) != 1 {
		t.Fatal("the message should wait in the inbox for the mod")
	}
}
