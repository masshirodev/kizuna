package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// livePeerInstance makes a running-looking instance of profile: a lock holding
// a live PID, and a meta file naming the folder it works in. The PID is a
// sleeping child rather than the test's own, because an instance whose
// process is one of the caller's ancestors is the caller's own instance.
func livePeerInstance(t *testing.T, root string, profile Profile, run, folder string) string {
	t.Helper()
	lockDir := filepath.Join(root, appName, "profiles", profile.Name, instancesDirectory, run)
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		t.Fatal(err)
	}
	sleeper := exec.Command("sleep", "300")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	if err := os.WriteFile(filepath.Join(lockDir, ".active.lock"), []byte(fmt.Sprintf("%d\n", sleeper.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := setProfileInstanceMeta(lockDir, folder, false, nil, ""); err != nil {
		t.Fatal(err)
	}
	return lockDir
}

// isolatePeers keeps a test away from the instance the suite itself may be
// running inside: an agent's own AI_INSTANCE_DIR would otherwise make it
// "self", and its inbox a real one.
func isolatePeers(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(instanceDirEnv, "")
	t.Setenv(configHomeEnv, "")
	t.Setenv(profileNameEnv, "")
	return root
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
}

func TestReadGitCheckoutTellsAWorktreeFromItsMainCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	base := t.TempDir()
	main := filepath.Join(base, "ranma")
	gitInit(t, main)
	worktree := filepath.Join(base, "ranma-fix")
	if output, err := exec.Command("git", "-C", main, "worktree", "add", "-q", "-b", "fix", worktree).CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, output)
	}

	inMain := readGitCheckout(filepath.Join(main))
	if inMain.repo != "ranma" || inMain.worktree || inMain.branch != "main" {
		t.Fatalf("main checkout = %+v, want ranma on main, not a worktree", inMain)
	}
	inWorktree := readGitCheckout(worktree)
	if inWorktree.repo != "ranma" || !inWorktree.worktree || inWorktree.branch != "fix" {
		t.Fatalf("worktree = %+v, want ranma's worktree on fix", inWorktree)
	}
	if inWorktree.root != inMain.root {
		t.Fatalf("worktree root %q, want the main checkout %q, so both match one repo", inWorktree.root, inMain.root)
	}
	if outside := readGitCheckout(base); outside.repo != "" {
		t.Fatalf("a folder outside any repository = %+v, want nothing", outside)
	}
}

func TestCollectPeersSpansProfilesAndFiltersByRepo(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	home := Profile{Name: "home", Provider: "codex", Command: "codex"}
	livePeerInstance(t, root, work, "run-1", "/work/ranma")
	livePeerInstance(t, root, home, "run-2", "/work/kumiko")
	cfg := Config{Profiles: []Profile{work, home}}

	peers := collectPeers(cfg)
	if len(peers) != 2 {
		t.Fatalf("peers = %+v, want one from each profile", peers)
	}
	ids := []string{peers[0].ID, peers[1].ID}
	if !strings.Contains(strings.Join(ids, " "), "work/run-1") || !strings.Contains(strings.Join(ids, " "), "home/run-2") {
		t.Fatalf("ids = %v, want profile/run-dir for each", ids)
	}
	if kept := peersInRepo(peers, "/work/ranma"); len(kept) != 1 || kept[0].Profile != "work" {
		t.Fatalf("filtered by folder = %+v, want only work's", kept)
	}
}

func TestResolvePeerRefusesAnAmbiguousName(t *testing.T) {
	peers := []peer{
		{ID: "max/run-1", Profile: "max", Name: "ranma-18"},
		{ID: "max/run-2", Profile: "max", Name: "kumiko-66"},
		{ID: "pro2/run-3", Profile: "pro2", Name: "ranma-9b"},
	}
	for target, want := range map[string]string{"max/run-2": "max/run-2", "pro2": "pro2/run-3", "ranma-18": "max/run-1"} {
		got, err := resolvePeer(peers, target)
		if err != nil || got.ID != want {
			t.Fatalf("resolvePeer(%q) = %q, %v; want %q", target, got.ID, err, want)
		}
	}
	if _, err := resolvePeer(peers, "max"); err == nil || !strings.Contains(err.Error(), "max/run-1") {
		t.Fatalf("an ambiguous profile should list what it could mean, got %v", err)
	}
	if _, err := resolvePeer(peers, "nobody"); err == nil {
		t.Fatal("an unknown name should be refused")
	}
}

func TestSendLeavesTheMessageInTheTargetInbox(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	lockDir := livePeerInstance(t, root, work, "run-1", "/work/ranma")
	cfg := Config{Profiles: []Profile{work}}

	result, err := sendMessage(cfg, "work", "second bug: x panics on y", sendOptions{typeMode: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Typed {
		t.Fatal("an agent whose state is unknown must not be typed into")
	}
	if !strings.Contains(result.Summary, "--type") {
		t.Fatalf("summary %q should say how to wake an agent nothing reads for", result.Summary)
	}
	messages := inboxMessages(lockDir)
	if len(messages) != 1 || messages[0].Text != "second bug: x panics on y" {
		t.Fatalf("inbox = %+v, want the one message", messages)
	}
	if _, err := sendMessage(cfg, "work", "   ", sendOptions{}); err == nil {
		t.Fatal("an empty message should be refused")
	}
}

func TestSendRefusesTheSendersOwnInstance(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	lockDir := livePeerInstance(t, root, work, "run-1", "/work/ranma")
	t.Setenv(instanceDirEnv, lockDir)
	if _, err := sendMessage(Config{Profiles: []Profile{work}}, "work/run-1", "hello me", sendOptions{}); err == nil {
		t.Fatal("sending to oneself should be refused")
	}
}

func TestHookDeliversOnceAndAnswersEachEvent(t *testing.T) {
	lockDir := t.TempDir()
	sent := time.Date(2026, 10, 8, 14, 2, 0, 0, time.UTC)
	deliver := func() {
		if err := deliverMessage(lockDir, message{From: "pro2/run-3", FromFolder: "/work/ranma", Sent: sent, Text: "fix the resize bug too"}); err != nil {
			t.Fatal(err)
		}
	}

	deliver()
	var out bytes.Buffer
	writeHookResponse(lockDir, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), &out)
	var post struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &post); err != nil {
		t.Fatalf("PostToolUse answer %q: %v", out.String(), err)
	}
	if post.HookSpecificOutput.HookEventName != "PostToolUse" || !strings.Contains(post.HookSpecificOutput.AdditionalContext, "fix the resize bug too") ||
		!strings.Contains(post.HookSpecificOutput.AdditionalContext, "not from the user") ||
		!strings.Contains(post.HookSpecificOutput.AdditionalContext, "ai send pro2/run-3") {
		t.Fatalf("PostToolUse context = %+v", post)
	}

	out.Reset()
	writeHookResponse(lockDir, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), &out)
	if out.Len() != 0 {
		t.Fatalf("a drained inbox delivered again: %q", out.String())
	}

	deliver()
	out.Reset()
	writeHookResponse(lockDir, strings.NewReader(`{"hook_event_name":"Stop","stop_hook_active":false}`), &out)
	var stop struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(out.Bytes(), &stop); err != nil || stop.Decision != "block" || !strings.Contains(stop.Reason, "resize bug") {
		t.Fatalf("Stop answer %q (%v), want a block carrying the message", out.String(), err)
	}

	deliver()
	out.Reset()
	writeHookResponse(lockDir, strings.NewReader(`{"hook_event_name":"Notification"}`), &out)
	if out.Len() != 0 || len(inboxMessages(lockDir)) != 1 {
		t.Fatalf("an event the hook cannot answer must leave the message unread, inbox = %d, out = %q", len(inboxMessages(lockDir)), out.String())
	}
}

func TestMessagingHooksMergeAndComeOffCleanly(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	path := filepath.Join(root, appName, "profiles", "max", "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"model":"opus","hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"reparse"}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := setMessagingHooks(profile, "/bin/ai "+messagingHookArgs, true); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path)
	if count := strings.Count(string(data), messagingHookArgs); count != len(messagingHookEvents) {
		t.Fatalf("installing twice left %d hook entries, want one per event:\n%s", count, data)
	}
	if !strings.Contains(string(data), "reparse") || !strings.Contains(string(data), `"opus"`) {
		t.Fatalf("the user's own settings were lost:\n%s", data)
	}
	if messagingDelivery(profile) != "hooks" {
		t.Fatal("a profile with the hooks should read as delivering by hooks")
	}

	if err := setMessagingHooks(profile, "", false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), messagingHookArgs) || !strings.Contains(string(data), "reparse") {
		t.Fatalf("turning off should remove only ai's hooks:\n%s", data)
	}
	if strings.Contains(string(data), "Stop") {
		t.Fatalf("an event left with no hooks should go:\n%s", data)
	}
}

func TestRemoveMCPServerKeepsTheOthers(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	if err := mergeMCPServers(profile, []mcpServer{
		{Name: messagingMCPName, Transport: mcpStdio, Command: "/bin/ai", Args: []string{"mcp", "serve"}},
		{Name: "lattice", Transport: mcpHTTP, URL: "https://example.test/mcp"},
	}); err != nil {
		t.Fatal(err)
	}
	if messagingDelivery(profile) != "mcp" {
		t.Fatal("a profile with only the server should read as delivering by mcp")
	}
	if err := removeMCPServer(profile, messagingMCPName); err != nil {
		t.Fatal(err)
	}
	servers, err := readMCPServers(profile)
	if err != nil || len(servers) != 1 || servers[0].Name != "lattice" {
		t.Fatalf("servers = %+v (%v), want only lattice left", servers, err)
	}
	if err := removeMCPServer(profile, "absent"); err != nil {
		t.Fatalf("removing a server that is not there: %v", err)
	}
	_ = root
}

func TestConfigBaseFollowsTheLauncherOnlyFromInsideAProfile(t *testing.T) {
	real := t.TempDir()
	t.Setenv(configHomeEnv, real)

	// An OpenCode launch: XDG_CONFIG_HOME is the profile's own config tree.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(real, appName, "profiles", "opencode3", "config"))
	if base, err := configBase(); err != nil || base != real {
		t.Fatalf("inside a profile, configBase = %q (%v), want the launcher's %q", base, err, real)
	}

	// Anything else that set XDG_CONFIG_HOME, a test above all, keeps its own.
	elsewhere := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", elsewhere)
	if base, err := configBase(); err != nil || base != elsewhere {
		t.Fatalf("outside any profile, configBase = %q (%v), want %q", base, err, elsewhere)
	}
}

func TestInstanceMetaRecordsTheRanmaPane(t *testing.T) {
	lockDir := t.TempDir()
	env := []string{ranmaSocketEnv + "=/run/ranma/1.sock", ranmaPaneEnv + "=13"}
	if err := setProfileInstanceMeta(lockDir, "/work/ranma", false, env, ""); err != nil {
		t.Fatal(err)
	}
	if meta := readInstanceMeta(lockDir); meta.RanmaPane != "13" || meta.RanmaSocket != "/run/ranma/1.sock" {
		t.Fatalf("meta = %+v, want the pane and socket", meta)
	}
	if err := setProfileInstanceMeta(lockDir, "/work/ranma", false, []string{ranmaPaneEnv + "=13"}, ""); err != nil {
		t.Fatal(err)
	}
	if meta := readInstanceMeta(lockDir); meta.RanmaPane != "" {
		t.Fatalf("a pane without its socket is not a ranma pane, meta = %+v", meta)
	}
}

func TestLaunchEnvironmentNamesTheInstanceAndTheConfigHome(t *testing.T) {
	root := isolatePeers(t)
	profile := Profile{Name: "max", Provider: "claude", Command: "claude"}
	lockDir := filepath.Join(root, "run-1")
	env := launchEnvironment(profile, filepath.Join(root, "max"), lockDir, []string{instanceDirEnv + "=/stale", "PATH=/bin"})
	if got := envValue(env, instanceDirEnv); got != lockDir {
		t.Fatalf("%s = %q, want this launch's %q", instanceDirEnv, got, lockDir)
	}
	if strings.Count(strings.Join(env, "\n"), instanceDirEnv+"=") != 1 {
		t.Fatalf("an inherited %s should be replaced, not kept beside:\n%v", instanceDirEnv, env)
	}
	if got := envValue(env, configHomeEnv); got != root {
		t.Fatalf("%s = %q, want %q", configHomeEnv, got, root)
	}
}

func TestMCPServeAnswersTheHandshakeAndTheTools(t *testing.T) {
	isolatePeers(t)
	lockDir := t.TempDir()
	t.Setenv(instanceDirEnv, lockDir)
	if err := deliverMessage(lockDir, message{From: "max/run-1", Sent: time.Now(), Text: "ping from ranma"}); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_inbox","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"send_message","arguments":{"target":"nobody","text":"x","to_worker":true}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"bogus"}`,
	}, "\n")
	var out bytes.Buffer
	if err := mcpServeCommand(Config{}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d responses, want 5 (the notification gets none):\n%s", len(lines), out.String())
	}
	for index, want := range []string{`"protocolVersion":"2025-06-18"`, `"list_peers"`, `ping from ranma`, `"isError":true`, `-32601`} {
		if !strings.Contains(lines[index], want) {
			t.Fatalf("response %d = %s, want it to contain %s", index+1, lines[index], want)
		}
	}
}

func TestParentPIDReadsThisProcess(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	parent, ok := parentPID(os.Getpid())
	if !ok || parent != os.Getppid() {
		t.Fatalf("parentPID = %d, %v; want %d", parent, ok, os.Getppid())
	}
}

func TestHookLeavesTheSessionsMailAloneInsideASubagent(t *testing.T) {
	lockDir := t.TempDir()
	if err := deliverMessage(lockDir, message{From: "pro2/run-3", Sent: time.Now(), Text: "for the lead"}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{
		`{"hook_event_name":"PostToolUse","agent_id":"a1b2","agent_type":"Explore"}`,
		`{"hook_event_name":"SubagentStop","agent_id":"a1b2","stop_hook_active":false}`,
	} {
		var out bytes.Buffer
		writeHookResponse(lockDir, strings.NewReader(event), &out)
		if out.Len() != 0 || len(inboxMessages(lockDir)) != 1 {
			t.Fatalf("%s: a subagent was handed the session's mail (out %q, inbox %d)", event, out.String(), len(inboxMessages(lockDir)))
		}
	}
	var out bytes.Buffer
	writeHookResponse(lockDir, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), &out)
	if !strings.Contains(out.String(), "for the lead") {
		t.Fatalf("the main thread's next tool call should read it, got %q", out.String())
	}
}

// setLead rewrites an instance's meta as one launched by lead.
func setLead(t *testing.T, lockDir, folder, lead string) {
	t.Helper()
	if err := setProfileInstanceMeta(lockDir, folder, true, nil, lead); err != nil {
		t.Fatal(err)
	}
}

func TestPeersSayWhoLaunchedWhom(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	livePeerInstance(t, root, work, "run-1", "/work/ranma")
	worker := livePeerInstance(t, root, work, "run-2", "/work/ranma-wt")
	setLead(t, worker, "/work/ranma-wt", "work/run-1")
	orphan := livePeerInstance(t, root, work, "run-3", "/work/other")
	setLead(t, orphan, "/work/other", "work/run-9")

	peers := map[string]peer{}
	for _, p := range collectPeers(Config{Profiles: []Profile{work}}) {
		peers[p.ID] = p
	}
	if lead := peers["work/run-1"]; lead.Role != "lead" || len(lead.Workers) != 1 || lead.Workers[0] != "work/run-2" || lead.roleLabel() != "lead of 1" {
		t.Fatalf("lead = %+v (%q)", lead, lead.roleLabel())
	}
	if w := peers["work/run-2"]; w.Role != "worker" || w.Lead != "work/run-1" || !w.leadRunning || w.roleLabel() != "worker of work/run-1" {
		t.Fatalf("worker = %+v (%q)", w, w.roleLabel())
	}
	if o := peers["work/run-3"]; o.Role != "worker" || o.leadRunning || o.roleLabel() != "worker of work/run-9 (ended)" {
		t.Fatalf("orphan = %+v (%q)", o, o.roleLabel())
	}

	var out bytes.Buffer
	if err := peersCommand(Config{Profiles: []Profile{work}}, nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ROLE") || !strings.Contains(out.String(), "worker of work/run-1") {
		t.Fatalf("the table should carry the role:\n%s", out.String())
	}
}

func TestSendToSomeoneElsesWorkerPointsAtItsLead(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	lead := livePeerInstance(t, root, work, "run-1", "/work/ranma")
	worker := livePeerInstance(t, root, work, "run-2", "/work/ranma-wt")
	setLead(t, worker, "/work/ranma-wt", "work/run-1")
	orphan := livePeerInstance(t, root, work, "run-3", "/work/other")
	setLead(t, orphan, "/work/other", "work/run-9")
	cfg := Config{Profiles: []Profile{work}}

	_, err := sendMessage(cfg, "work/run-2", "fix this too", sendOptions{})
	if err == nil || !strings.Contains(err.Error(), "send to the lead, work/run-1") {
		t.Fatalf("a message to another agent's worker should name its lead, got %v", err)
	}
	if len(inboxMessages(worker)) != 0 {
		t.Fatal("a refused message was delivered anyway")
	}
	if _, err := sendMessage(cfg, "work/run-2", "really for you", sendOptions{toWorker: true}); err != nil {
		t.Fatalf("--worker should let it through: %v", err)
	}
	if _, err := sendMessage(cfg, "work/run-3", "your lead is gone", sendOptions{}); err != nil {
		t.Fatalf("a worker whose lead ended has nobody to redirect to: %v", err)
	}

	t.Setenv(instanceDirEnv, lead)
	t.Setenv(profileNameEnv, "work")
	if _, err := sendMessage(cfg, "work/run-2", "from your lead", sendOptions{}); err != nil {
		t.Fatalf("a lead should reach its own worker: %v", err)
	}
	if got := len(inboxMessages(worker)); got != 2 {
		t.Fatalf("worker inbox = %d, want the two allowed messages", got)
	}
}

func TestLaunchRecordsTheInstanceThatStartedIt(t *testing.T) {
	root := isolatePeers(t)
	work := Profile{Name: "work", Provider: "codex", Command: "codex"}
	if err := os.MkdirAll(filepath.Join(root, appName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveConfig(filepath.Join(root, appName, "profiles.json"), Config{Profiles: []Profile{work}}); err != nil {
		t.Fatal(err)
	}
	if got := launchingInstanceID(); got != "" {
		t.Fatalf("outside any instance the lead = %q, want none", got)
	}
	lead := livePeerInstance(t, root, work, "run-1", "/work/ranma")
	t.Setenv(instanceDirEnv, lead)
	t.Setenv(profileNameEnv, "work")
	if got := launchingInstanceID(); got != "work/run-1" {
		t.Fatalf("lead = %q, want work/run-1", got)
	}
}
