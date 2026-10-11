package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func names(entries []treeEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.name)
	}
	return out
}

func TestReadTreeEntriesDirectoriesFirstAndCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"zeta", "Alpha"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"readme.md", ".hidden"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries := readTreeEntries(root)
	got := strings.Join(names(entries), ",")
	if want := "Alpha,zeta,.hidden,readme.md"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
	if !entries[len(entries)-2].hidden {
		t.Fatal("a dotfile was not marked hidden")
	}
}

func TestFlattenTreeHidesDotfilesAndKeepsFiles(t *testing.T) {
	children := map[string][]treeEntry{
		"/r": {
			{name: ".git", path: "/r/.git", isDir: true, readable: true, hidden: true},
			{name: "a", path: "/r/a", isDir: true, readable: true},
			{name: "f.txt", path: "/r/f.txt"},
		},
		"/r/a": {{name: "b", path: "/r/a/b", isDir: true, readable: true}},
	}
	load := func(dir string) []treeEntry { return children[dir] }
	expanded := map[string]bool{"/r": true, "/r/a": true}

	var paths []string
	for _, row := range flattenTree(load, "/r", expanded, false) {
		paths = append(paths, row.entry.path)
	}
	if got, want := strings.Join(paths, ","), "/r,/r/a,/r/a/b,/r/f.txt"; got != want {
		t.Fatalf("rows = %s, want %s", got, want)
	}
	paths = paths[:0]
	for _, row := range flattenTree(load, "/r", expanded, true) {
		paths = append(paths, row.entry.path)
	}
	if got, want := strings.Join(paths, ","), "/r,/r/.git,/r/a,/r/a/b,/r/f.txt"; got != want {
		t.Fatalf("with hidden rows = %s, want %s", got, want)
	}
}

func TestFolderTreeRootsAtHomeAndOpensToLaunchFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	launch := filepath.Join(home, "projects", "alpha")
	if err := os.MkdirAll(launch, 0700); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{workingDir: launch}
	m.openFolderTree()
	if m.tree.root != home {
		t.Fatalf("root = %q, want home %q", m.tree.root, home)
	}
	for _, dir := range []string{filepath.Join(home, "projects"), launch} {
		if !m.tree.expanded[dir] {
			t.Fatalf("chain to the launch folder was not opened: %q", dir)
		}
	}
	if got, _ := m.tree.selected(); got.path != launch {
		t.Fatalf("cursor = %q, want the launch folder %q", got.path, launch)
	}
}

func TestFolderTreeOpensSetsAndSkipsFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha", "beta"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "zzz.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{profiles: testProfiles(), workingDir: root}
	m.openFolderTree()
	if got, _ := m.tree.selected(); got.path != root {
		t.Fatalf("cursor started on %q, want the launch folder", got.path)
	}
	m.tree.move(1)
	if got, _ := m.tree.selected(); got.name != "alpha" {
		t.Fatalf("down = %q, want alpha", got.name)
	}
	// The next row is a file, which the cursor must skip rather than land on.
	m.tree.move(1)
	if got, _ := m.tree.selected(); got.name != "alpha" {
		t.Fatalf("the cursor landed on a file: %q", got.name)
	}
	updated, _ := m.updateFolder(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if !m.tree.expanded[filepath.Join(root, "alpha")] {
		t.Fatal("↵ did not open alpha")
	}
	m.tree.move(1)
	if got, _ := m.tree.selected(); got.name != "beta" {
		t.Fatalf("after opening alpha, down = %q, want beta", got.name)
	}
	updated, _ = m.updateFolder(runeKey('s'))
	m = updated.(tuiModel)
	if m.mode != tuiList || m.workingDir != filepath.Join(root, "alpha", "beta") {
		t.Fatalf("s = mode %v, folder %q", m.mode, m.workingDir)
	}
}

func TestFolderTreeHiddenToggle(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".config", "visible"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	m := tuiModel{workingDir: root}
	m.openFolderTree()
	if len(m.tree.rows) != 2 {
		t.Fatalf("rows with hidden off = %d, want the root and one folder", len(m.tree.rows))
	}
	updated, _ := m.updateFolder(runeKey('.'))
	m = updated.(tuiModel)
	if len(m.tree.rows) != 3 {
		t.Fatalf("rows with hidden on = %d, want the root and two folders", len(m.tree.rows))
	}
}

func TestFolderTreeCollapseKeepsTheCursorOnTheClosedFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha", "beta"), 0700); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{workingDir: root}
	m.openFolderTree()
	m.tree.move(1)
	updated, _ := m.updateFolder(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	m.tree.move(1)
	if got, _ := m.tree.selected(); got.name != "beta" {
		t.Fatalf("cursor = %q, want beta", got.name)
	}
	// ← steps to the parent when the folder under the cursor is closed...
	updated, _ = m.updateFolder(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(tuiModel)
	if got, _ := m.tree.selected(); got.name != "alpha" {
		t.Fatalf("← = %q, want the parent alpha", got.name)
	}
	// ...and closes it when it is open, leaving the cursor on it.
	updated, _ = m.updateFolder(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(tuiModel)
	if m.tree.expanded[filepath.Join(root, "alpha")] {
		t.Fatal("alpha left open after ←")
	}
	if got, _ := m.tree.selected(); got.name != "alpha" {
		t.Fatalf("collapse moved the cursor to %q", got.name)
	}
}

// TestFolderTreeFrame pins the frame and the measurements of direction 1b: the
// box is the share-picker width and full height, the body is two panes joined
// at the inner width, and the read-out answers "is this a repository". The
// values are the handoff's, so a later tidy-up cannot quietly redraw the box.
func TestFolderTreeFrame(t *testing.T) {
	if got := modalCeiling(tuiFolder); got != shareModalWidth {
		t.Fatalf("folder box ceiling = %d, want the share picker's %d", got, shareModalWidth)
	}
	if !fullHeightModal(tuiFolder) {
		t.Fatal("the folder box does not fill the frame")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "alpha"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := tuiModel{profiles: testProfiles(), workingDir: root}
	m.openFolderTree()

	const inner, rows = 118, 20
	content := m.folderTreeContent(inner, rows)
	if len(content) != rows+4 {
		t.Fatalf("frame = %d lines, want %d", len(content), rows+4)
	}
	if !strings.Contains(content[0], "CHANGE LAUNCH FOLDER") {
		t.Fatalf("heading = %q", content[0])
	}
	body := strings.Join(content[2:len(content)-2], "\n")
	for _, want := range []string{"FOLDER", "│", "AT ", "CONTAINS", "alpha/", "go.mod"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body is missing %q:\n%s", want, body)
		}
	}
	for index, line := range content[2 : len(content)-2] {
		if width := lipgloss.Width(line); width != inner {
			t.Fatalf("body line %d is %d cells, want %d: %q", index, width, inner, line)
		}
	}
	if !strings.Contains(content[len(content)-1], "cancel") {
		t.Fatalf("footer = %q", content[len(content)-1])
	}
}
