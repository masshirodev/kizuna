package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// An MCP server is configured once and then wanted everywhere: the same
// endpoint, the same token, in every account that has to reach it. Each CLI
// writes that configuration in its own syntax, in its own file, inside its own
// isolated state directory — so what travels between two profiles cannot be a
// fragment of one provider's config. It has to be the server itself.
//
// mcpServer is that server: the fields all four providers agree on, read out of
// whichever file the source keeps them in and written back in whatever shape
// the destination's CLI expects. Nothing else crosses. A field only one
// provider understands stays where it was, because a setting invented for one
// CLI means nothing in another and guessing at a translation is how a copy
// silently changes what a server does.
type mcpServer struct {
	Name      string
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	URL       string
	Headers   map[string]string
}

const (
	mcpStdio = "stdio"
	mcpHTTP  = "http"
	mcpSSE   = "sse"
)

// transport is the declared transport, or the one implied by what the entry
// actually carries. Several of these CLIs let the key be omitted when a URL or
// a command already settles the question.
func (server mcpServer) transport() string {
	switch server.Transport {
	case mcpStdio, mcpHTTP, mcpSSE:
		return server.Transport
	}
	if server.URL != "" {
		return mcpHTTP
	}
	return mcpStdio
}

// endpoint is the one line that says where a server actually is, for a picker
// row with no room for the whole definition.
func (server mcpServer) endpoint() string {
	if server.URL != "" {
		return server.URL
	}
	if server.Command == "" {
		return ""
	}
	return formatArguments(append([]string{server.Command}, server.Args...))
}

// mcpConfigFile is the file a provider keeps its MCP servers in, inside the
// profile's own state directory. A provider missing from this switch is one
// whose MCP configuration this launcher does not know how to read, which is
// said plainly rather than guessed at.
//
// deepseek is deliberately absent even though it runs through the OpenCode
// binary: profileEnv sets no XDG directories for it, so a deepseek profile
// reads the machine's ordinary OpenCode config and has no isolated one to copy
// into.
func mcpConfigFile(profile Profile) (string, error) {
	root, err := profileRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, profile.Name)
	switch profile.Provider {
	case "claude":
		return filepath.Join(dir, "claude", ".claude.json"), nil
	case "codex":
		return filepath.Join(dir, "codex", "config.toml"), nil
	case "opencode":
		return openCodeConfigFile(filepath.Join(dir, "config", "opencode")), nil
	case "antigravity":
		return filepath.Join(dir, "home", ".gemini", "config", "mcp_config.json"), nil
	default:
		return "", fmt.Errorf("provider %q keeps no MCP configuration this launcher can read", profile.Provider)
	}
}

// openCodeConfigFile picks the config OpenCode itself would load. Both
// spellings are valid and only one is ever present; a directory with neither
// gets the plain JSON one, because a file this launcher creates has no comments
// in it to justify the other.
func openCodeConfigFile(dir string) string {
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return filepath.Join(dir, "opencode.json")
}

func supportsMCP(profile Profile) bool {
	_, err := mcpConfigFile(profile)
	return err == nil
}

// readMCPServers lists what a profile can lend, sorted by name so two reads of
// an unchanged config produce the same list — a picker whose rows move between
// frames is a picker you cannot act on.
func readMCPServers(profile Profile) ([]mcpServer, error) {
	path, err := mcpConfigFile(profile)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var servers []mcpServer
	switch profile.Provider {
	case "claude":
		servers, err = readClaudeMCP(data)
	case "codex":
		servers, err = readCodexMCP(data)
	case "opencode":
		servers, err = readOpenCodeMCP(data)
	case "antigravity":
		servers, err = readAntigravityMCP(data)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return servers, nil
}

// mergeMCPServers adds servers to a profile's config, replacing any entry of
// the same name and leaving every other key of that file exactly as it was.
// The file belongs to the provider's CLI, not to ai-session: a copy that
// rewrote the parts it does not understand would be indistinguishable from
// corruption the next time that CLI started.
func mergeMCPServers(profile Profile, servers []mcpServer) error {
	if len(servers) == 0 {
		return nil
	}
	path, err := mcpConfigFile(profile)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data = nil
	} else if err != nil {
		return err
	}
	var merged []byte
	switch profile.Provider {
	case "claude":
		merged, err = mergeClaudeMCP(data, servers)
	case "codex":
		merged, err = mergeCodexMCP(data, servers)
	case "opencode":
		merged, err = mergeOpenCodeMCP(data, servers)
	case "antigravity":
		merged, err = mergeAntigravityMCP(data, servers)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return writeFileAtomic(path, merged)
}

// copyMCPServers is the whole feature in one call: take these servers from that
// profile and make them exist in this one. names selects a subset; an empty
// names copies everything the source has.
//
// A name already present in the destination is refused unless replace says
// otherwise, matching every other write in this launcher — an existing
// definition is the user's, and a copy that silently took its place would be
// the one operation here with no way back.
func copyMCPServers(source, destination Profile, names []string, replace bool) ([]string, error) {
	if source.Name == destination.Name {
		return nil, errors.New("source and destination are the same profile")
	}
	if !supportsMCP(destination) {
		return nil, fmt.Errorf("profile %q (%s) keeps no MCP configuration this launcher can write",
			destination.Name, destination.Provider)
	}
	available, err := readMCPServers(source)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, fmt.Errorf("profile %q has no MCP servers configured", source.Name)
	}
	chosen, err := selectByName(available, names, func(server mcpServer) string { return server.Name },
		fmt.Sprintf("profile %q has no MCP server named", source.Name))
	if err != nil {
		return nil, err
	}
	existing, err := readMCPServers(destination)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(existing))
	for _, server := range existing {
		present[server.Name] = true
	}
	copied := make([]string, 0, len(chosen))
	for _, server := range chosen {
		if present[server.Name] && !replace {
			return nil, fmt.Errorf("profile %q already has an MCP server named %q; pass --replace to overwrite it",
				destination.Name, server.Name)
		}
		copied = append(copied, server.Name)
	}
	if err := mergeMCPServers(destination, chosen); err != nil {
		return nil, err
	}
	return copied, nil
}

// selectByName narrows a list to the named members, keeping the list's own
// order rather than the order the names were typed in, and reporting the first
// name that matches nothing instead of quietly copying fewer things than were
// asked for.
func selectByName[T any](all []T, names []string, nameOf func(T) string, missing string) ([]T, error) {
	if len(names) == 0 {
		return all, nil
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	chosen := make([]T, 0, len(names))
	for _, candidate := range all {
		if wanted[nameOf(candidate)] {
			delete(wanted, nameOf(candidate))
			chosen = append(chosen, candidate)
		}
	}
	for _, name := range names {
		if wanted[name] {
			return nil, fmt.Errorf("%s %q", missing, name)
		}
	}
	return chosen, nil
}

// writeFileAtomic replaces a provider's config in one step, so a CLI reading it
// at the wrong moment sees the old file or the new one and never a half-written
// one. An existing file keeps its own permissions; a new one is created private,
// because these files carry MCP tokens.
func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// --- Claude Code -----------------------------------------------------------

type claudeMCPEntry struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func readClaudeMCP(data []byte) ([]mcpServer, error) {
	entries, err := jsonObjectField(data, "mcpServers")
	if err != nil {
		return nil, err
	}
	servers := make([]mcpServer, 0, len(entries))
	for name, raw := range entries {
		var entry claudeMCPEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		servers = append(servers, mcpServer{
			Name: name, Transport: entry.Type, Command: entry.Command, Args: entry.Args,
			Env: entry.Env, URL: entry.URL, Headers: entry.Headers,
		})
	}
	return servers, nil
}

func mergeClaudeMCP(data []byte, servers []mcpServer) ([]byte, error) {
	entries, err := jsonObjectField(data, "mcpServers")
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		encoded, err := json.Marshal(claudeMCPEntry{
			Type: server.transport(), Command: server.Command, Args: server.Args,
			Env: server.Env, URL: server.URL, Headers: server.Headers,
		})
		if err != nil {
			return nil, err
		}
		entries[server.Name] = encoded
	}
	return setJSONField(data, "mcpServers", orderedJSONObject(entries))
}

// --- OpenCode --------------------------------------------------------------

type openCodeMCPEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

func readOpenCodeMCP(data []byte) ([]mcpServer, error) {
	entries, err := jsonObjectField(sanitizeJSONC(data), "mcp")
	if err != nil {
		return nil, err
	}
	servers := make([]mcpServer, 0, len(entries))
	for name, raw := range entries {
		var entry openCodeMCPEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		server := mcpServer{Name: name, Env: entry.Environment, URL: entry.URL, Headers: entry.Headers}
		if entry.Type == "remote" || entry.URL != "" {
			server.Transport = mcpHTTP
		} else {
			server.Transport = mcpStdio
		}
		if len(entry.Command) > 0 {
			server.Command, server.Args = entry.Command[0], entry.Command[1:]
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func mergeOpenCodeMCP(data []byte, servers []mcpServer) ([]byte, error) {
	// OpenCode accepts comments and trailing commas in opencode.jsonc, and
	// encoding/json cannot write them back. Rewriting the file would delete
	// whatever the comments said, so a file that has any is refused instead —
	// the same answer the statusline integration gives when the key it wants to
	// set is already taken.
	if sanitized := sanitizeJSONC(data); !bytes.Equal(bytes.TrimSpace(sanitized), bytes.TrimSpace(data)) {
		return nil, errors.New("config has comments or trailing commas this launcher cannot preserve; " +
			"remove them, or copy the server across by hand")
	}
	entries, err := jsonObjectField(data, "mcp")
	if err != nil {
		return nil, err
	}
	enabled := true
	for _, server := range servers {
		entry := openCodeMCPEntry{Type: "local", Environment: server.Env, Enabled: &enabled}
		if server.transport() != mcpStdio {
			entry = openCodeMCPEntry{Type: "remote", URL: server.URL, Headers: server.Headers, Enabled: &enabled}
		} else if server.Command != "" {
			entry.Command = append([]string{server.Command}, server.Args...)
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		entries[server.Name] = encoded
	}
	return setJSONField(data, "mcp", orderedJSONObject(entries))
}

// --- Antigravity -----------------------------------------------------------

type antigravityMCPEntry struct {
	ServerURL string            `json:"serverUrl,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Disabled  bool              `json:"disabled"`
}

func readAntigravityMCP(data []byte) ([]mcpServer, error) {
	entries, err := jsonObjectField(data, "mcpServers")
	if err != nil {
		return nil, err
	}
	servers := make([]mcpServer, 0, len(entries))
	for name, raw := range entries {
		var entry antigravityMCPEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		server := mcpServer{
			Name: name, Command: entry.Command, Args: entry.Args,
			Env: entry.Env, URL: entry.ServerURL, Headers: entry.Headers,
		}
		if entry.ServerURL != "" {
			server.Transport = mcpHTTP
		} else {
			server.Transport = mcpStdio
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func mergeAntigravityMCP(data []byte, servers []mcpServer) ([]byte, error) {
	entries, err := jsonObjectField(data, "mcpServers")
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		entry := antigravityMCPEntry{Env: server.Env}
		if server.transport() == mcpStdio {
			entry.Command, entry.Args = server.Command, server.Args
		} else {
			entry.ServerURL, entry.Headers = server.URL, server.Headers
		}
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		entries[server.Name] = encoded
	}
	return setJSONField(data, "mcpServers", orderedJSONObject(entries))
}

// --- JSON helpers ----------------------------------------------------------

// jsonObjectField reads one top-level object out of a config, returning an
// empty map for a missing key or an empty file so a caller can merge into a
// config that does not exist yet.
func jsonObjectField(data []byte, key string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	entries := map[string]json.RawMessage{}
	if raw, ok := document[key]; ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	return entries, nil
}

// orderedJSONObject renders a map back as an object with its keys sorted, so
// adding one server rewrites one line of the file rather than shuffling every
// entry into whatever order the map happened to iterate in.
func orderedJSONObject(entries map[string]json.RawMessage) json.RawMessage {
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var out bytes.Buffer
	out.WriteByte('{')
	for index, name := range names {
		if index > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		out.Write(key)
		out.WriteByte(':')
		out.Write(entries[name])
	}
	out.WriteByte('}')
	return out.Bytes()
}

// setJSONField replaces one top-level key and leaves every other key of the
// document byte for byte as it was, in the order it was already in. These files
// are not ours — .claude.json is Claude Code's own state, ninety kilobytes of
// it — and a copy that reordered the whole document would be impossible to read
// back to check what it actually changed.
func setJSONField(data []byte, key string, value json.RawMessage) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
	}
	order, err := jsonKeyOrder(data)
	if err != nil {
		return nil, err
	}
	if _, replaced := fields[key]; !replaced {
		order = append(order, key)
	}
	fields[key] = value

	var compact bytes.Buffer
	compact.WriteByte('{')
	for index, name := range order {
		if index > 0 {
			compact.WriteByte(',')
		}
		encoded, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		compact.Write(encoded)
		compact.WriteByte(':')
		compact.Write(fields[name])
	}
	compact.WriteByte('}')

	var indented bytes.Buffer
	if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	return append(indented.Bytes(), '\n'), nil
}

// jsonKeyOrder lists a document's top-level keys in the order they appear.
// encoding/json unmarshals an object into a map, which has none.
func jsonKeyOrder(data []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("configuration is not a JSON object")
	}
	var order []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("configuration has a non-string key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		order = append(order, name)
	}
	return order, nil
}

// --- Codex TOML ------------------------------------------------------------

const codexMCPTable = "mcp_servers"

// readCodexMCP pulls [mcp_servers.<name>] tables out of config.toml. Codex is
// the one provider keeping MCP servers in TOML and ai-session carries no TOML
// parser, for the same reason it hand-reads the one model key it needs: the
// shape being read is table headers and flat key/value lines, and a dependency
// that can parse all of TOML would be carried for that.
//
// A key this scan does not recognise is left alone rather than guessed at, so a
// server copied out of Codex arrives with its endpoint and its environment and
// none of the per-tool approval settings that mean nothing anywhere else.
func readCodexMCP(data []byte) ([]mcpServer, error) {
	servers := map[string]*mcpServer{}
	var order []string
	name, section := "", ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, section = codexMCPHeader(line)
			if name != "" && servers[name] == nil {
				servers[name] = &mcpServer{Name: name}
				order = append(order, name)
			}
			continue
		}
		if name == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"'`)
		value = strings.TrimSpace(value)
		server := servers[name]
		switch section {
		case "":
			switch key {
			case "type":
				server.Transport, _ = tomlString(value)
			case "command":
				server.Command, _ = tomlString(value)
			case "url":
				server.URL, _ = tomlString(value)
			case "args":
				server.Args, _ = tomlStringArray(value)
			case "env":
				server.Env, _ = tomlInlineTable(value)
			case "http_headers":
				server.Headers, _ = tomlInlineTable(value)
			}
		case "env":
			if text, ok := tomlString(value); ok {
				if server.Env == nil {
					server.Env = map[string]string{}
				}
				server.Env[key] = text
			}
		case "http_headers":
			if text, ok := tomlString(value); ok {
				if server.Headers == nil {
					server.Headers = map[string]string{}
				}
				server.Headers[key] = text
			}
		}
	}
	list := make([]mcpServer, 0, len(order))
	for _, name := range order {
		list = append(list, *servers[name])
	}
	return list, nil
}

// codexMCPHeader reads a table header, answering which MCP server it belongs to
// and which part of that server it configures. A header for anything else
// returns an empty name, which is what stops the scan from reading [tui] keys
// into whichever server happened to be declared above it.
func codexMCPHeader(line string) (string, string) {
	header := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "["), "]"))
	parts := splitTOMLKey(header)
	if len(parts) < 2 || parts[0] != codexMCPTable {
		return "", ""
	}
	return parts[1], strings.Join(parts[2:], ".")
}

// splitTOMLKey splits a dotted key on the dots that are not inside quotes, so a
// server whose name contains one stays a single part.
func splitTOMLKey(key string) []string {
	var parts []string
	var current strings.Builder
	var quote rune
	for _, char := range key {
		switch {
		case quote != 0:
			if char == quote {
				quote = 0
			} else {
				current.WriteRune(char)
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '.':
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}
	return append(parts, strings.TrimSpace(current.String()))
}

// stripTOMLComment drops a trailing comment, counting a # inside a string as
// part of the value — which is where a bearer token's fragment would live.
func stripTOMLComment(line string) string {
	var quote rune
	escaped := false
	for index, char := range line {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && char == '\\':
			escaped = true
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '#':
			return line[:index]
		}
	}
	return line
}

func tomlString(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	if strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return strings.Trim(value, `"`), true
		}
		return unquoted, true
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return strings.Trim(value, "'"), true
	}
	return "", false
}

func tomlStringArray(value string) ([]string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, false
	}
	items := splitTOMLList(value[1 : len(value)-1])
	values := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := tomlString(item)
		if !ok {
			return nil, false
		}
		values = append(values, text)
	}
	return values, true
}

func tomlInlineTable(value string) (map[string]string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
		return nil, false
	}
	table := map[string]string{}
	for _, item := range splitTOMLList(value[1 : len(value)-1]) {
		key, raw, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		if text, ok := tomlString(raw); ok {
			table[strings.Trim(strings.TrimSpace(key), `"'`)] = text
		}
	}
	return table, true
}

// splitTOMLList splits an array or inline table body on its top-level commas.
func splitTOMLList(body string) []string {
	var items []string
	var current strings.Builder
	var quote rune
	depth, escaped := 0, false
	for _, char := range body {
		switch {
		case escaped:
			escaped = false
		case quote == '"' && char == '\\':
			escaped = true
			current.WriteRune(char)
			continue
		case quote != 0:
			if char == quote {
				quote = 0
			}
		case char == '"' || char == '\'':
			quote = char
		case char == '[' || char == '{':
			depth++
		case char == ']' || char == '}':
			depth--
		case char == ',' && depth == 0:
			items = append(items, strings.TrimSpace(current.String()))
			current.Reset()
			continue
		}
		current.WriteRune(char)
	}
	if trimmed := strings.TrimSpace(current.String()); trimmed != "" {
		items = append(items, trimmed)
	}
	return items
}

// mergeCodexMCP rewrites config.toml with these servers defined. An existing
// definition is removed whole — every table belonging to that server, including
// the per-tool ones Codex writes underneath it — and the new one appended, so a
// replaced server cannot end up half described by its predecessor's leftovers.
func mergeCodexMCP(data []byte, servers []mcpServer) ([]byte, error) {
	text := string(data)
	for _, server := range servers {
		text = removeCodexMCPTables(text, server.Name)
	}
	var out strings.Builder
	if trimmed := strings.TrimRight(text, "\n"); trimmed != "" {
		out.WriteString(trimmed)
		out.WriteString("\n")
	}
	for _, server := range servers {
		out.WriteString("\n")
		out.WriteString(renderCodexMCP(server))
	}
	return []byte(out.String()), nil
}

func removeCodexMCPTables(text, name string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	dropping := false
	for _, line := range lines {
		if trimmed := strings.TrimSpace(stripTOMLComment(line)); strings.HasPrefix(trimmed, "[") {
			header, _ := codexMCPHeader(trimmed)
			dropping = header == name
		}
		if !dropping {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func renderCodexMCP(server mcpServer) string {
	key := codexKey(server.Name)
	var out strings.Builder
	fmt.Fprintf(&out, "[%s.%s]\n", codexMCPTable, key)
	fmt.Fprintf(&out, "type = %s\n", strconv.Quote(server.transport()))
	if server.transport() == mcpStdio {
		if server.Command != "" {
			fmt.Fprintf(&out, "command = %s\n", strconv.Quote(server.Command))
		}
		if len(server.Args) > 0 {
			quoted := make([]string, len(server.Args))
			for index, arg := range server.Args {
				quoted[index] = strconv.Quote(arg)
			}
			fmt.Fprintf(&out, "args = [%s]\n", strings.Join(quoted, ", "))
		}
	} else if server.URL != "" {
		fmt.Fprintf(&out, "url = %s\n", strconv.Quote(server.URL))
	}
	out.WriteString(renderCodexTable(key, "env", server.Env))
	if server.transport() != mcpStdio {
		out.WriteString(renderCodexTable(key, "http_headers", server.Headers))
	}
	return out.String()
}

func renderCodexTable(server, section string, values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var out strings.Builder
	fmt.Fprintf(&out, "\n[%s.%s.%s]\n", codexMCPTable, server, section)
	for _, name := range names {
		fmt.Fprintf(&out, "%s = %s\n", codexKey(name), strconv.Quote(values[name]))
	}
	return out.String()
}

// codexKey quotes a key TOML would not accept bare. Server names come from
// another CLI's config, where the allowed characters are not the same.
func codexKey(name string) string {
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return strconv.Quote(name)
		}
	}
	if name == "" {
		return `""`
	}
	return name
}
