package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// `ai mcp serve` is a stdio MCP server that lets an agent find the others and
// message them itself: list_peers, send_message and read_inbox, the same
// operations as `ai peers`, `ai send` and `ai inbox`. Messages are one JSON
// object per line, which is the MCP stdio transport; nothing here needs more
// of the protocol than initialize, tools/list and tools/call.

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var messagingTools = []map[string]any{
	{
		"name":        "list_peers",
		"description": "List every AI agent running on this machine under ai-session, across all profiles and providers: its id, the repository and checkout (main checkout or worktree) it works in, branch, busy/idle where known, and how it reads messages. Use it to find the agent already working in a repository before starting parallel work there.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo": map[string]any{"type": "string", "description": "Only agents in this repository, by name (\"ranma\") or by any path inside it."},
			},
		},
	},
	{
		"name":        "send_message",
		"description": "Leave a message for another agent (an id from list_peers, a profile with one agent running, or a Claude session name). It is queued in the agent's inbox and read at its next tool call or turn end, never typed over its work. Say what you need and enough context to act on it without asking you, e.g. a bug's repro and where to fix it.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"target", "text"},
			"properties": map[string]any{
				"target": map[string]any{"type": "string"},
				"text":   map[string]any{"type": "string"},
				"type_into_pane": map[string]any{
					"type":        "string",
					"enum":        []string{"auto", "always", "never"},
					"description": "Whether to type a one-line nudge at the agent's prompt. auto (default) does so only when it is known to be idle.",
				},
			},
		},
	},
	{
		"name":        "read_inbox",
		"description": "Read the messages other agents have left for you, and mark them read.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

func mcpServeCommand(cfg Config, stdin io.Reader, stdout io.Writer) error {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			_ = encoder.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		// A notification carries no id and gets no answer.
		if len(request.ID) == 0 {
			continue
		}
		result, failure := handleMCPRequest(cfg, request)
		response := rpcResponse{JSONRPC: "2.0", ID: request.ID, Result: result, Error: failure}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleMCPRequest(cfg Config, request rpcRequest) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		version := params.ProtocolVersion
		if version == "" {
			version = mcpProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ai-session", "version": currentBuild().short()},
			"instructions":    "ai-session's directory of the agents running on this machine. list_peers shows who is working where; send_message hands work to the agent already in a checkout instead of opening a parallel worktree; read_inbox reads what others sent you.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": messagingTools}, nil
	case "tools/call":
		var params struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		text, err := callMessagingTool(cfg, params.Name, params.Arguments)
		if err != nil {
			return map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}, nil
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found: " + request.Method}
}

func callMessagingTool(cfg Config, name string, arguments map[string]string) (string, error) {
	switch name {
	case "list_peers":
		peers := collectPeers(cfg)
		if repo := arguments["repo"]; repo != "" {
			peers = peersInRepo(peers, repo)
		}
		if peers == nil {
			peers = []peer{}
		}
		data, err := json.MarshalIndent(peers, "", "  ")
		return string(data), err
	case "send_message":
		mode := arguments["type_into_pane"]
		if mode == "" {
			mode = "auto"
		}
		result, err := sendMessage(cfg, arguments["target"], arguments["text"], sendOptions{typeMode: mode})
		if err != nil {
			return "", err
		}
		return result.Summary, nil
	case "read_inbox":
		lockDir := selfInstanceDir(cfg)
		if lockDir == "" {
			return "", fmt.Errorf("this MCP server is not running inside an ai-launched session, so it has no inbox")
		}
		messages := drainInbox(lockDir)
		if len(messages) == 0 {
			return "no messages", nil
		}
		return renderMessages(messages), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// selfInstanceDir finds the instance this process runs inside. The launcher
// exports it, but some CLIs start MCP servers with a scrubbed environment
// (Codex passes only a short allowlist), so the process tree is the
// fallback: whichever live instance recorded one of this process's
// ancestors as its CLI is the one it belongs to.
func selfInstanceDir(cfg Config) string {
	if lockDir := os.Getenv(instanceDirEnv); lockDir != "" {
		return lockDir
	}
	_, lockDir := instanceOwningProcess(cfg, os.Getpid())
	return lockDir
}

func instanceOwningProcess(cfg Config, pid int) (Profile, string) {
	ancestors := map[int]bool{}
	for current, steps := pid, 0; current > 1 && steps < 64; steps++ {
		ancestors[current] = true
		parent, ok := parentPID(current)
		if !ok {
			break
		}
		current = parent
	}
	for _, profile := range cfg.Profiles {
		instances, err := activeProfileInstances(profile)
		if err != nil {
			continue
		}
		for _, instance := range instances {
			pids, err := profileLockPIDs(filepath.Join(instance.lockDir, ".active.lock"))
			if err != nil {
				continue
			}
			for _, recorded := range pids {
				if ancestors[recorded] {
					return profile, instance.lockDir
				}
			}
		}
	}
	return Profile{}, ""
}

// parentPID reads a process's parent from /proc. The command name in stat is
// parenthesised and may itself contain spaces or parentheses, so the fields
// are counted from the last ')'.
func parentPID(pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	return parent, err == nil
}
