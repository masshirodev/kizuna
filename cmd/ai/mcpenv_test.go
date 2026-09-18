package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestShellToOpenCodeRewritesEverySpelling(t *testing.T) {
	cases := map[string]string{
		"Bearer ${SOME_TOKEN}":        "Bearer {env:SOME_TOKEN}",
		"Bearer $SOME_TOKEN":          "Bearer {env:SOME_TOKEN}",
		"${A}-${B}":                   "{env:A}-{env:B}",
		"https://${HOST}/mcp":         "https://{env:HOST}/mcp",
		"no reference here":           "no reference here",
		"${MISSING:-fallback}":        "{env:MISSING}",
		`echo "${_X%o}"`:              `echo "${_X%o}"`,
		"price is $5":                 "price is $5",
		"already {env:KEPT} spelling": "already {env:KEPT} spelling",
	}
	for input, want := range cases {
		if got := shellToOpenCode(input); got != want {
			t.Errorf("shellToOpenCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestOpenCodeToShellRewritesBack(t *testing.T) {
	cases := map[string]string{
		"Bearer {env:SOME_TOKEN}": "Bearer ${SOME_TOKEN}",
		"{env:A}-{env:B}":         "${A}-${B}",
		"no reference here":       "no reference here",
		"already ${KEPT}":         "already ${KEPT}",
		"{env:lower1}":            "${lower1}",
		"{env:9bad}":              "{env:9bad}",
	}
	for input, want := range cases {
		if got := openCodeToShell(input); got != want {
			t.Errorf("openCodeToShell(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCopyMCPRewritesClaudeRefsForOpenCode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"lattice":{"type":"http","url":"https://${MCP_HOST}/mcp","headers":{"Authorization":"Bearer ${SOME_TOKEN}"}}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, `{"mcp":{"other":{"type":"remote","url":"https://old.example/mcp","enabled":true}}}`,
		"oc", "config", "opencode", "opencode.json")

	if _, err := copyMCPServers(claudeProfile("cl"), Profile{Name: "oc", Provider: "opencode"}, nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "oc", "config", "opencode", "opencode.json"))
	for _, want := range []string{
		`"Authorization": "Bearer {env:SOME_TOKEN}"`,
		`"url": "https://{env:MCP_HOST}/mcp"`,
		`"url": "https://old.example/mcp"`,
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("opencode.json is missing %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, "${") {
		t.Fatalf("a shell-style reference survived the copy:\n%s", written)
	}
}

func TestCopyMCPRewritesOpenCodeRefsForClaude(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcp":{"lattice":{"type":"remote","url":"https://ctx.example/mcp","headers":{"Authorization":"Bearer {env:SOME_TOKEN}"},"enabled":true}}}`,
		"oc", "config", "opencode", "opencode.json")
	writeProfileFile(t, root, `{}`, "cl", "claude", ".claude.json")

	if _, err := copyMCPServers(Profile{Name: "oc", Provider: "opencode"}, claudeProfile("cl"), nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "cl", "claude", ".claude.json"))
	if !strings.Contains(written, "Bearer ${SOME_TOKEN}") {
		t.Fatalf("the header was not rewritten into Claude's spelling:\n%s", written)
	}
	if strings.Contains(written, "{env:") {
		t.Fatalf("an OpenCode reference survived the copy:\n%s", written)
	}
}

func TestCopyMCPLeavesSameSpellingAlone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp","headers":{"Authorization":"Bearer ${TOKEN}"}}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, `{"mcpServers":{"ag":{"serverUrl":"https://ag.example/mcp","headers":{"Authorization":"Bearer ${OTHER}"}}}}`,
		"ag", "home", ".gemini", "config", "mcp_config.json")

	// Claude and Antigravity expand the same syntax, so nothing is rewritten.
	if _, err := copyMCPServers(claudeProfile("cl"), Profile{Name: "ag", Provider: "antigravity"}, nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "ag", "home", ".gemini", "config", "mcp_config.json"))
	if !strings.Contains(written, "Bearer ${TOKEN}") {
		t.Fatalf("a same-spelling copy rewrote the value:\n%s", written)
	}
}

func TestCopyMCPToCodexUsesEnvironmentKeys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{
			"remote":{"type":"http","url":"https://ctx.example/mcp","headers":{"Authorization":"Bearer ${BEARER}","X-Key":"${KEY}","X-Static":"literal","X-Mixed":"prefix-${KEY}"}},
			"local":{"type":"stdio","command":"serve","args":["--x"],"env":{"FORWARDED":"${FORWARDED}","RENAMED":"${OTHER}","STATIC":"literal"}}}}`,
		"cl", "claude", ".claude.json")
	writeProfileFile(t, root, "model = \"gpt\"\n", "cx", "codex", "config.toml")

	if _, err := copyMCPServers(claudeProfile("cl"), codexProfile("cx"), nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "cx", "codex", "config.toml"))
	for _, want := range []string{
		`bearer_token_env_var = "BEARER"`,
		`[mcp_servers.remote.env_http_headers]`,
		`X-Key = "KEY"`,
		`X-Static = "literal"`,
		`X-Mixed = "prefix-${KEY}"`,
		`env_vars = ["FORWARDED"]`,
		`RENAMED = "${OTHER}"`,
		`STATIC = "literal"`,
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("config.toml is missing %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, "Authorization") {
		t.Fatalf("the bearer header was also written static:\n%s", written)
	}
}

func TestCopyMCPFromCodexNormalisesEnvironmentKeys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, "[mcp_servers.remote]\ntype = \"http\"\nurl = \"https://ctx.example/mcp\"\nbearer_token_env_var = \"BEARER\"\n\n[mcp_servers.remote.env_http_headers]\nX-Key = \"KEY\"\n\n[mcp_servers.local]\ntype = \"stdio\"\ncommand = \"serve\"\nenv_vars = [\"FORWARDED\"]\n",
		"cx", "codex", "config.toml")
	writeProfileFile(t, root, `{}`, "cl", "claude", ".claude.json")

	if _, err := copyMCPServers(codexProfile("cx"), claudeProfile("cl"), nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "cl", "claude", ".claude.json"))
	for _, want := range []string{
		`Bearer ${BEARER}`,
		`"X-Key": "${KEY}"`,
		`"FORWARDED": "${FORWARDED}"`,
	} {
		if !strings.Contains(written, want) {
			t.Fatalf(".claude.json is missing %q:\n%s", want, written)
		}
	}
}

func TestCopyMCPFromCodexToOpenCodeRewritesRefs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, "[mcp_servers.remote]\ntype = \"http\"\nurl = \"https://ctx.example/mcp\"\n\n[mcp_servers.remote.env_http_headers]\nAuthorization = \"TOKEN\"\n",
		"cx", "codex", "config.toml")
	writeProfileFile(t, root, `{"mcp":{}}`, "oc", "config", "opencode", "opencode.json")

	if _, err := copyMCPServers(codexProfile("cx"), Profile{Name: "oc", Provider: "opencode"}, nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "oc", "config", "opencode", "opencode.json"))
	if !strings.Contains(written, `"Authorization": "{env:TOKEN}"`) {
		t.Fatalf("the env-sourced header was not rewritten:\n%s", written)
	}
}

func TestCopyMCPCodexToCodexRoundTripsEnvironmentKeys(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, "[mcp_servers.remote]\ntype = \"http\"\nurl = \"https://ctx.example/mcp\"\nbearer_token_env_var = \"BEARER\"\n\n[mcp_servers.remote.env_http_headers]\nX-Key = \"KEY\"\n",
		"src", "codex", "config.toml")
	writeProfileFile(t, root, "model = \"gpt\"\n", "dst", "codex", "config.toml")

	if _, err := copyMCPServers(codexProfile("src"), codexProfile("dst"), nil, false); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "dst", "codex", "config.toml"))
	for _, want := range []string{
		`bearer_token_env_var = "BEARER"`,
		`[mcp_servers.remote.env_http_headers]`,
		`X-Key = "KEY"`,
	} {
		if !strings.Contains(written, want) {
			t.Fatalf("the round trip lost %q:\n%s", want, written)
		}
	}
	if strings.Contains(written, "Bearer ${BEARER}") || strings.Contains(written, `"${KEY}"`) {
		t.Fatalf("an env-sourced value was written static:\n%s", written)
	}
}

func TestReadCodexMCPParsesInlineEnvKeys(t *testing.T) {
	servers, err := readCodexMCP([]byte("[mcp_servers.remote]\ntype = \"http\"\nurl = \"https://ctx.example/mcp\"\n" +
		"http_headers = { X-Static = \"literal\" }\nenv_http_headers = { X-Key = \"KEY\" }\nbearer_token_env_var = \"BEARER\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("read %d servers, want 1", len(servers))
	}
	got := servers[0]
	if got.Headers["X-Static"] != "literal" || got.Headers["X-Key"] != "${KEY}" || got.Headers["Authorization"] != "Bearer ${BEARER}" {
		t.Fatalf("headers = %+v, want static kept and env keys normalised", got.Headers)
	}
}

func TestReadCodexMCPSkipsRemoteEnvVarEntries(t *testing.T) {
	servers, err := readCodexMCP([]byte("[mcp_servers.local]\ncommand = \"serve\"\n" +
		"env_vars = [\"LOCAL_TOKEN\", { name = \"REMOTE_TOKEN\", source = \"remote\" }]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Env["LOCAL_TOKEN"] != "${LOCAL_TOKEN}" {
		t.Fatalf("env = %+v, want the local entry forwarded", servers[0].Env)
	}
	if _, ok := servers[0].Env["REMOTE_TOKEN"]; ok {
		t.Fatalf("env = %+v, want the remote entry skipped", servers[0].Env)
	}
}

func TestSplitCodexHeadersKeepsEmbeddedRefsStatic(t *testing.T) {
	static, sourced, bearer := splitCodexHeaders(map[string]string{
		"Authorization": "Bearer ${BEARER}",
		"X-Key":         "${KEY}",
		"X-Mixed":       "prefix-${KEY}",
		"X-Plain":       "literal",
		"X-Braces":      "{env:BRACED}",
	})
	if bearer != "BEARER" {
		t.Fatalf("bearer = %q, want BEARER", bearer)
	}
	if sourced["X-Key"] != "KEY" || sourced["X-Braces"] != "BRACED" || len(sourced) != 2 {
		t.Fatalf("sourced = %+v, want exactly the whole-value references", sourced)
	}
	if static["X-Mixed"] != "prefix-${KEY}" || static["X-Plain"] != "literal" || len(static) != 2 {
		t.Fatalf("static = %+v, want the literal and the unspellable reference", static)
	}
}

func TestSplitCodexEnvForwardsOnlySelfReferences(t *testing.T) {
	static, forwarded := splitCodexEnv(map[string]string{
		"FORWARDED": "${FORWARDED}",
		"BRACED":    "{env:BRACED}",
		"RENAMED":   "${OTHER}",
		"STATIC":    "literal",
	})
	if len(forwarded) != 2 || forwarded[0] != "BRACED" || forwarded[1] != "FORWARDED" {
		t.Fatalf("forwarded = %v, want the sorted self-references", forwarded)
	}
	if static["RENAMED"] != "${OTHER}" || static["STATIC"] != "literal" || len(static) != 2 {
		t.Fatalf("static = %+v, want the rename and the literal kept", static)
	}
}

func TestCodexEnvKeysSurviveMergeWithoutOtherTables(t *testing.T) {
	merged, err := mergeCodexMCP([]byte("model = \"gpt\"\n"),
		[]mcpServer{{Name: "remote", Transport: mcpHTTP, URL: "https://ctx.example/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${BEARER}", "X-Key": "${KEY}"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(merged)
	for _, want := range []string{
		"model = \"gpt\"",
		`bearer_token_env_var = "BEARER"`,
		`[mcp_servers.remote.env_http_headers]`,
		`X-Key = "KEY"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged config is missing %q:\n%s", want, got)
		}
	}
	if _, err := readCodexMCP(merged); err != nil {
		t.Fatalf("merged config does not re-read: %v\n%s", err, got)
	}
	servers, _ := readCodexMCP(merged)
	if servers[0].Headers["Authorization"] != "Bearer ${BEARER}" || servers[0].Headers["X-Key"] != "${KEY}" {
		t.Fatalf("re-read headers = %+v", servers[0].Headers)
	}
}
