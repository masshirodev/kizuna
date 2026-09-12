package main

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// shareModel is a cockpit whose cursor sits on the profile being copied into,
// which is the profile every key on the list acts on.
func shareModel(t *testing.T, root string, profiles []Profile, cursor int) tuiModel {
	t.Helper()
	_, path := cloneTestConfig(t, root, profiles...)
	m := wideModel(profiles)
	m.configPath = path
	m.cursor = cursor
	return m
}

func TestSharePickerCopiesTheTickedServers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"},"lattice":{"type":"http","url":"https://apps.example/api/mcp"}}}`,
		"max", "claude", ".claude.json")
	writeProfileFile(t, root, `{"userID":"other"}`, "spare", "claude", ".claude.json")
	m := shareModel(t, root, []Profile{claudeProfile("max"), claudeProfile("spare")}, 1)

	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	got := updated.(tuiModel)
	if got.mode != tuiShareFrom {
		t.Fatalf("m did not open the source picker, mode = %v (%s)", got.mode, got.status)
	}
	if len(got.share.sources) != 1 || got.share.sources[0].profile.Name != "max" || got.share.sources[0].count != 2 {
		t.Fatalf("sources = %+v, want only the profile with servers to lend", got.share.sources)
	}
	if view := got.View(); !strings.Contains(view, "Install MCP servers into spare") || !strings.Contains(view, "2 MCP servers") {
		t.Fatalf("the source picker does not say what it offers:\n%s", view)
	}

	updated, _ = got.updateShareFrom(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if got.mode != tuiShareItems || len(got.share.items) != 2 {
		t.Fatalf("choosing a source did not open the list, mode = %v items = %+v", got.mode, got.share.items)
	}
	if view := got.View(); !strings.Contains(view, "max → spare") || !strings.Contains(view, "https://ctx.example/mcp") {
		t.Fatalf("the multi-select does not describe its rows:\n%s", view)
	}

	// Nothing ticked is refused rather than read as "all of them".
	updated, _ = got.updateShareItems(tea.KeyMsg{Type: tea.KeyEnter})
	if got = updated.(tuiModel); got.mode != tuiShareItems || got.statusKind != statusErr {
		t.Fatalf("an empty selection was applied anyway, mode = %v status = %q", got.mode, got.status)
	}

	updated, _ = got.updateShareItems(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	got = updated.(tuiModel)
	updated, _ = got.updateShareItems(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if got.mode != tuiList || got.statusKind != statusOK {
		t.Fatalf("applying the copy failed: mode = %v status = %q", got.mode, got.status)
	}
	written := readFile(t, filepath.Join(root, appName, "profiles", "spare", "claude", ".claude.json"))
	if !strings.Contains(written, "ctx.example") {
		t.Fatalf("the ticked server was not installed:\n%s", written)
	}
	if strings.Contains(written, "apps.example") {
		t.Fatalf("a server nobody ticked was installed too:\n%s", written)
	}
}

func TestSharePickerTicksEverythingExceptWhatIsAlreadyInstalled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"},"lattice":{"type":"http","url":"https://apps.example/api/mcp"}}}`,
		"max", "claude", ".claude.json")
	writeProfileFile(t, root, `{"mcpServers":{"lattice":{"type":"http","url":"https://old.example/api/mcp"}}}`,
		"spare", "claude", ".claude.json")
	m := shareModel(t, root, []Profile{claudeProfile("max"), claudeProfile("spare")}, 1)
	m.openShare(shareMCP)
	if err := m.loadShareItems(); err != nil {
		t.Fatal(err)
	}
	m.mode = tuiShareItems

	updated, _ := m.updateShareItems(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	got := updated.(tuiModel)
	for _, item := range got.share.items {
		if item.present && item.chosen {
			t.Fatalf("a ticked-everything press selected %q, which already exists in the destination", item.name)
		}
		if !item.present && !item.chosen {
			t.Fatalf("a ticked-everything press missed %q", item.name)
		}
	}
	if view := got.View(); !strings.Contains(view, "installed") {
		t.Fatalf("the row the destination already has is not marked:\n%s", view)
	}

	// It is still possible to replace one, by ticking that row on its own.
	got.share.item = 1
	updated, _ = got.updateShareItems(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	got = updated.(tuiModel)
	updated, _ = got.updateShareItems(tea.KeyMsg{Type: tea.KeyEnter})
	if got = updated.(tuiModel); got.statusKind != statusOK {
		t.Fatalf("replacing a ticked row failed: %q", got.status)
	}
	if written := readFile(t, filepath.Join(root, appName, "profiles", "spare", "claude", ".claude.json")); strings.Contains(written, "old.example") {
		t.Fatalf("the deliberately ticked row was not replaced:\n%s", written)
	}
}

func TestShareSaysWhyItCannotOpen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"}}}`,
		"max", "claude", ".claude.json")
	profiles := []Profile{claudeProfile("max"), {Name: "gemini", Provider: "antigravity", Command: "agy"}}

	// Antigravity has MCP but no skills, so one key opens and the other says so.
	m := shareModel(t, root, profiles, 1)
	m.openShare(shareSkills)
	if m.mode != tuiList || !strings.Contains(m.status, "skill") {
		t.Fatalf("mode = %v status = %q, want a refusal naming skills", m.mode, m.status)
	}

	// And with nothing to lend, the picker is not opened onto an empty list.
	empty := shareModel(t, root, []Profile{claudeProfile("max"), claudeProfile("spare")}, 0)
	empty.openShare(shareMCP)
	if empty.mode != tuiList || !strings.Contains(empty.status, "lend") {
		t.Fatalf("mode = %v status = %q, want a refusal saying nothing can be lent", empty.mode, empty.status)
	}
}

func TestCloneKeyAddsTheProfileAndSelectsIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	claudeProfileState(t, root, "max")
	m := shareModel(t, root, []Profile{claudeProfile("max")}, 0)

	updated, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	got := updated.(tuiModel)
	if got.mode != tuiClone {
		t.Fatalf("C did not open the clone prompt, mode = %v", got.mode)
	}
	if view := got.View(); !strings.Contains(view, "Clone max") || !strings.Contains(view, "Credentials do not come with it") {
		t.Fatalf("the clone prompt does not say what it copies:\n%s", view)
	}

	updated, _ = got.updateClone(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("spare")})
	got = updated.(tuiModel)
	updated, _ = got.updateClone(tea.KeyMsg{Type: tea.KeyEnter})
	got = updated.(tuiModel)
	if got.mode != tuiList || got.statusKind != statusOK {
		t.Fatalf("the clone was not saved: mode = %v status = %q", got.mode, got.status)
	}
	if len(got.profiles) != 2 {
		t.Fatalf("profiles = %+v, want the clone beside its source", got.profiles)
	}
	if selected, ok := got.selectedProfile(); !ok || selected.Name != "spare" {
		t.Fatalf("the cursor did not follow the clone: %+v", selected)
	}
	if _, err := findProfile(mustLoadConfig(t, got.configPath), "spare"); err != nil {
		t.Fatalf("the clone was not written to profiles.json: %v", err)
	}
}

func TestShareCommandsCopyBetweenProfiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeProfileFile(t, root, `{"mcpServers":{"ctx":{"type":"http","url":"https://ctx.example/mcp"}}}`,
		"max", "claude", ".claude.json")
	writeSkill(t, root, "max", "claude", "release", "Cut a release", "")
	cloneTestConfig(t, root, claudeProfile("max"), codexProfile("cx"))

	var out strings.Builder
	if err := run([]string{"mcp", "list", "max"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ctx\thttp\thttps://ctx.example/mcp") {
		t.Fatalf("mcp list output = %q", out.String())
	}

	out.Reset()
	if err := run([]string{"mcp", "copy", "max", "cx", "ctx"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "copied 1 MCP server from max to cx") {
		t.Fatalf("mcp copy output = %q", out.String())
	}

	out.Reset()
	if err := run([]string{"skill", "copy", "max", "cx"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "copied 1 skill from max to cx") {
		t.Fatalf("skill copy output = %q", out.String())
	}
	if _, err := readSkills(codexProfile("cx")); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := run([]string{"profile", "clone", "max", "spare"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cloned max into spare (claude)") {
		t.Fatalf("clone output = %q", out.String())
	}
	if _, err := findProfile(mustLoadConfig(t, filepath.Join(root, appName, "profiles.json")), "spare"); err != nil {
		t.Fatalf("the cloned profile was not registered: %v", err)
	}
}

func mustLoadConfig(t *testing.T, path string) Config {
	t.Helper()
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
