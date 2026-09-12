package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeProfile(name string) Profile {
	return Profile{Name: name, Provider: "claude", Command: "claude"}
}

func codexProfile(name string) Profile {
	return Profile{Name: name, Provider: "codex", Command: "codex"}
}

func TestReadMCPServersReadsEachProvidersOwnSyntax(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"lattice":{"type":"http","url":"https://apps.example/api/mcp","headers":{"Authorization":"Bearer t"}}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, "model = \"gpt\"\n\n[mcp_servers.graph]\ncommand = \"graph\"\nargs = [\"serve\"]\ntype = \"stdio\"\n\n[mcp_servers.graph.env]\nMODE = \"fast\"\n\n[mcp_servers.graph.tools.query]\napproval_mode = \"approve\"\n",
		"cx", "codex", "config.toml")
	writeProfileFile(t, root, `{"mcp":{"ctx":{"type":"remote","url":"https://ctx.example/mcp","enabled":true}}}`,
		"oc", "config", "opencode", "opencode.json")
	writeProfileFile(t, root, `{"mcpServers":{"lattice":{"serverUrl":"https://apps.example/api/mcp","disabled":false}}}`,
		"ag", "home", ".gemini", "config", "mcp_config.json")

	for _, testCase := range []struct {
		profile   Profile
		name      string
		transport string
		endpoint  string
	}{
		{claudeProfile("cl"), "lattice", mcpHTTP, "https://apps.example/api/mcp"},
		{codexProfile("cx"), "graph", mcpStdio, "graph serve"},
		{Profile{Name: "oc", Provider: "opencode"}, "ctx", mcpHTTP, "https://ctx.example/mcp"},
		{Profile{Name: "ag", Provider: "antigravity"}, "lattice", mcpHTTP, "https://apps.example/api/mcp"},
	} {
		servers, err := readMCPServers(testCase.profile)
		if err != nil {
			t.Fatalf("%s: %v", testCase.profile.Provider, err)
		}
		if len(servers) != 1 {
			t.Fatalf("%s: read %d servers, want 1", testCase.profile.Provider, len(servers))
		}
		got := servers[0]
		if got.Name != testCase.name || got.transport() != testCase.transport || got.endpoint() != testCase.endpoint {
			t.Fatalf("%s: read %+v, want %s/%s/%s", testCase.profile.Provider, got, testCase.name, testCase.transport, testCase.endpoint)
		}
	}
}

func TestReadCodexMCPIgnoresKeysOfOtherTables(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, "[mcp_servers.graph]\ncommand = \"graph\" # the shim\n\n[tui]\ncommand = \"not-a-server\"\n",
		"cx", "codex", "config.toml")
	servers, err := readMCPServers(codexProfile("cx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Command != "graph" {
		t.Fatalf("read %+v, want only the graph server with its comment stripped", servers)
	}
}

func TestCopyMCPServerTranslatesIntoTheDestinationsSyntax(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp","headers":{"Authorization":"Bearer t"}},"graph":{"type":"stdio","command":"graph","args":["serve"],"env":{"MODE":"fast"}}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, "model = \"gpt\"\n", "cx", "codex", "config.toml")

	copied, err := copyMCPServers(claudeProfile("cl"), codexProfile("cx"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(copied, ",") != "ctx,graph" {
		t.Fatalf("copied %v, want both servers", copied)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "cx", "codex", "config.toml"))
	for _, want := range []string{
		"model = \"gpt\"",
		"[mcp_servers.ctx]",
		"url = \"https://ctx.example/mcp\"",
		"[mcp_servers.ctx.http_headers]",
		"Authorization = \"Bearer t\"",
		"[mcp_servers.graph]",
		"command = \"graph\"",
		"args = [\"serve\"]",
		"[mcp_servers.graph.env]",
		"MODE = \"fast\"",
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("config.toml is missing %q:\n%s", want, written)
		}
	}
	// The translation has to survive a round trip, or a copy on from here
	// would lose what this one carried.
	servers, err := readMCPServers(codexProfile("cx"))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Headers["Authorization"] != "Bearer t" || servers[1].Env["MODE"] != "fast" {
		t.Fatalf("re-read %+v, want both servers with their headers and environment", servers)
	}
}

func TestCopyMCPServerLeavesTheRestOfClaudesStateAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, `{"userID":"abc","projects":{"/tmp":{"history":[1,2]}},"numStartups":41}`,
		"other", "claude", ".claude.json")

	if _, err := copyMCPServers(claudeProfile("cl"), claudeProfile("other"), []string{"ctx"}, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "other", "claude", ".claude.json"))
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(written), &document); err != nil {
		t.Fatalf("rewritten .claude.json does not parse: %v\n%s", err, written)
	}
	for _, key := range []string{"userID", "projects", "numStartups", "mcpServers"} {
		if _, ok := document[key]; !ok {
			t.Fatalf(".claude.json lost %q:\n%s", key, written)
		}
	}
	// The account's own keys keep their order, so a diff of this file shows
	// the one thing the copy actually changed.
	if order := strings.Index(written, "userID"); order > strings.Index(written, "projects") {
		t.Fatalf("existing keys were reordered:\n%s", written)
	}
	if strings.Index(written, "numStartups") > strings.Index(written, "mcpServers") {
		t.Fatalf("the new key was not appended last:\n%s", written)
	}
}

func TestCopyMCPServerRefusesToReplaceWithoutBeingTold(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://new.example/mcp"}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://old.example/mcp"}}}`,
		"other", "claude", ".claude.json")

	if _, err := copyMCPServers(claudeProfile("cl"), claudeProfile("other"), nil, false); err == nil {
		t.Fatal("copying over an existing server was allowed without --replace")
	}
	if got := readFile(t, filepath.Join(root, appName, "profiles", "other", "claude", ".claude.json")); !strings.Contains(got, "old.example") {
		t.Fatalf("the refused copy still wrote something:\n%s", got)
	}
	if _, err := copyMCPServers(claudeProfile("cl"), claudeProfile("other"), nil, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, appName, "profiles", "other", "claude", ".claude.json")); !strings.Contains(got, "new.example") {
		t.Fatalf("--replace did not overwrite the server:\n%s", got)
	}
}

func TestCopyMCPServerReportsAnUnknownName(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"}}}`,
		"cl", "claude", ".claude.json")
	_, err := copyMCPServers(claudeProfile("cl"), claudeProfile("other"), []string{"ctx", "nope"}, false)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v, want one naming the server that does not exist", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, appName, "profiles", "other")); statErr == nil {
		t.Fatal("a copy that named a missing server still wrote to the destination")
	}
}

func TestMergeCodexMCPReplacesEveryTableOfTheServer(t *testing.T) {
	existing := "[mcp_servers.graph]\ncommand = \"old\"\n\n[mcp_servers.graph.tools.query]\napproval_mode = \"approve\"\n\n[tui]\ntheme = \"dark\"\n"
	merged, err := mergeCodexMCP([]byte(existing), []mcpServer{{Name: "graph", Transport: mcpStdio, Command: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(merged)
	if strings.Contains(got, "approval_mode") || strings.Contains(got, "\"old\"") {
		t.Fatalf("the replaced server left tables behind:\n%s", got)
	}
	if !strings.Contains(got, "theme = \"dark\"") {
		t.Fatalf("an unrelated table was removed:\n%s", got)
	}
	if !strings.Contains(got, "command = \"new\"") {
		t.Fatalf("the new definition was not written:\n%s", got)
	}
}

func TestMergeOpenCodeRefusesAConfigItCannotPreserve(t *testing.T) {
	_, err := mergeOpenCodeMCP([]byte("{\n  // the remote one\n  \"mcp\": {}\n}"), []mcpServer{{Name: "ctx", URL: "https://ctx.example/mcp"}})
	if err == nil || !strings.Contains(err.Error(), "comments") {
		t.Fatalf("error = %v, want a refusal naming the comments it would have deleted", err)
	}
}

func TestMCPIsRefusedForProvidersWithNoIsolatedConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := readMCPServers(Profile{Name: "ds", Provider: "deepseek"}); err == nil {
		t.Fatal("deepseek reported an MCP configuration it does not have an isolated copy of")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
