package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The launch-folder picker is a tree: a directory list on the left, what the
// folder under the cursor holds on the right. It replaces the one-line prompt
// the command used to be, so that setting the folder is a choice rather than a
// path typed from memory.
//
// Direction 1b of the Claude Design handoff ("File tree", 2026-09-27): files
// are shown dimmed and skipped by the cursor, ↵ opens a folder, s sets it, and
// / opens the path field the prompt used to be, where ↵ still resolves and sets
// exactly as before.

const (
	// The two halves fold at the same widths the pickers use: a row of names
	// and a folder's read-out are each given what they need to say anything,
	// and below both the read-out folds away rather than being squeezed.
	folderTreeMin     = 46
	folderReadOutMin  = 34
	folderTreeShare   = 48
	folderTreeMax     = 62
	folderReadOutName = 12
	// folderPathWidth bounds the typed-path field drawn in the heading's corner
	// when / is open.
	folderPathWidth = 44
)

// treeEntry is one name in a directory. isDir decides whether the cursor may
// land on it; readable is whether its contents can be listed, which is shown on
// the row rather than discovered by trying.
type treeEntry struct {
	name     string
	path     string
	isDir    bool
	readable bool
	hidden   bool
}

// treeRow is one visible line: an entry and how far it is indented.
type treeRow struct {
	entry treeEntry
	depth int
}

// treeRootLabel is what the root row is called. The tree is rooted at the home
// directory, which is drawn as ~ rather than by its own name.
const treeRootLabel = "~"

// readTreeEntries lists one directory. Hidden entries are included; the caller
// filters them, so the hidden count stays available either way. Directories come
// first, then files, each case-insensitively.
func readTreeEntries(dir string) []treeEntry {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	entries := make([]treeEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		entry := treeEntry{
			name:     name,
			path:     filepath.Join(dir, name),
			isDir:    item.IsDir(),
			readable: true,
			hidden:   strings.HasPrefix(name, "."),
		}
		if entry.isDir {
			entry.readable = dirReadable(entry.path)
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})
	return entries
}

// dirReadable reports whether a directory may be listed. Opening it is the test:
// a directory without read permission fails at open, which is the fact the row
// is drawn from.
func dirReadable(path string) bool {
	handle, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = handle.Close()
	return true
}

// flattenTree walks the expanded set depth-first, reading children through the
// given loader so the tree itself does not have to care where they came from.
// Files are leaves and always appear; hidden entries are dropped unless asked
// for, which is the toggle the picker offers.
func flattenTree(children func(string) []treeEntry, root string, expanded map[string]bool, showHidden bool) []treeRow {
	rows := []treeRow{{entry: treeEntry{name: treeRootLabel, path: root, isDir: true, readable: true}, depth: 0}}
	appendTreeChildren(&rows, children, root, 1, expanded, showHidden)
	return rows
}

func appendTreeChildren(rows *[]treeRow, children func(string) []treeEntry, dir string, depth int, expanded map[string]bool, showHidden bool) {
	for _, entry := range children(dir) {
		if entry.hidden && !showHidden {
			continue
		}
		*rows = append(*rows, treeRow{entry: entry, depth: depth})
		if entry.isDir && entry.readable && expanded[entry.path] {
			appendTreeChildren(rows, children, entry.path, depth+1, expanded, showHidden)
		}
	}
}

// repoInfo is what the read-out says about a folder as a repository. It is a
// lighter read than the handoff's gitState: the pane answers "is this a
// repository, and does it have uncommitted work", which two git calls do.
type repoInfo struct {
	repo    bool
	branch  string
	changed int
}

func folderRepoInfo(dir string) repoInfo {
	ctx, cancel := context.WithTimeout(context.Background(), gitStateTimeout)
	defer cancel()
	if strings.TrimSpace(gitOutput(ctx, dir, "rev-parse", "--is-inside-work-tree")) != "true" {
		return repoInfo{}
	}
	changed := 0
	for _, line := range strings.Split(strings.TrimRight(gitOutput(ctx, dir, "status", "--porcelain=v1"), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			changed++
		}
	}
	return repoInfo{
		repo:    true,
		branch:  strings.TrimSpace(gitOutput(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")),
		changed: changed,
	}
}

// folderTree is the picker's state. The two caches keep a cursor move off the
// disk: a directory is listed once, and a folder's repository is read once, for
// as long as the box is open.
type folderTree struct {
	root     string
	expanded map[string]bool
	cursor   int
	rows     []treeRow
	hidden   bool
	pathMode bool
	children map[string][]treeEntry
	repos    map[string]repoInfo
}

// childrenOf lists a directory, caching the answer. It is the loader flattenTree
// walks through, so an expanded directory is read exactly once.
func (t *folderTree) childrenOf(dir string) []treeEntry {
	if entries, ok := t.children[dir]; ok {
		return entries
	}
	entries := readTreeEntries(dir)
	t.children[dir] = entries
	return entries
}

func (t *folderTree) repoOf(dir string) repoInfo {
	if info, ok := t.repos[dir]; ok {
		return info
	}
	info := folderRepoInfo(dir)
	t.repos[dir] = info
	return info
}

// rebuild flattens the tree again after the open set or the hidden toggle
// changed. The cursor stays on the row it was on where that row survives, and
// otherwise lands on the nearest folder to where it was rather than jumping to
// the top of the tree.
func (t *folderTree) rebuild() {
	path, hint := "", t.cursor
	if hint >= 0 && hint < len(t.rows) {
		path = t.rows[hint].entry.path
	}
	t.rows = flattenTree(t.childrenOf, t.root, t.expanded, t.hidden)
	if at := t.indexOf(path); at >= 0 {
		t.cursor = at
		return
	}
	t.cursor = t.nearestDir(hint)
}

func (t *folderTree) indexOf(path string) int {
	for index, row := range t.rows {
		if row.entry.path == path {
			return index
		}
	}
	return -1
}

func (t *folderTree) rowFor(path string) int {
	if at := t.indexOf(path); at >= 0 {
		return at
	}
	return t.nearestDir(0)
}

// nearestDir settles the cursor on a directory, since files are shown but not
// selectable.
func (t *folderTree) nearestDir(from int) int {
	for index := max(from, 0); index < len(t.rows); index++ {
		if t.rows[index].entry.isDir {
			return index
		}
	}
	for index := min(from, len(t.rows)-1); index >= 0; index-- {
		if t.rows[index].entry.isDir {
			return index
		}
	}
	return 0
}

// move walks the cursor to the next selectable row. It clamps rather than
// wrapping: a tree has a top and a bottom, and a list that jumps from the last
// folder to the first is a list that lost its place.
func (t *folderTree) move(delta int) {
	index := t.cursor
	for {
		index += delta
		if index < 0 || index >= len(t.rows) {
			return
		}
		if t.rows[index].entry.isDir {
			t.cursor = index
			return
		}
	}
}

// toggle opens the folder under the cursor, or closes it if it is already open.
// An unreadable folder cannot be opened; the row says so.
func (t *folderTree) toggle() {
	row, ok := t.selected()
	if !ok || !row.isDir || !row.readable {
		return
	}
	if t.expanded[row.path] {
		delete(t.expanded, row.path)
	} else {
		t.childrenOf(row.path)
		t.expanded[row.path] = true
	}
	t.rebuild()
	t.cursor = t.rowFor(row.path)
}

// collapseOrUp closes the open folder under the cursor, or moves to its parent
// when it is already closed. It is ← on the box: one key for the two things a
// tree's left arrow does.
func (t *folderTree) collapseOrUp() {
	row, ok := t.selected()
	if !ok {
		return
	}
	if row.isDir && t.expanded[row.path] {
		delete(t.expanded, row.path)
		t.rebuild()
		return
	}
	parent := filepath.Dir(row.path)
	if parent == row.path || parent == filepath.Dir(t.root) {
		return
	}
	t.cursor = t.rowFor(parent)
}

func (t *folderTree) selected() (treeEntry, bool) {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return treeEntry{}, false
	}
	return t.rows[t.cursor].entry, true
}

// treeRoot returns the root the picker opens at and the chain from it down to
// dir that should be open. Home is the root; a folder outside home roots the
// tree at that folder rather than hiding it under a root it is not in.
func treeRoot(home, dir string) (string, []string) {
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return dir, nil
	}
	var chain []string
	current := home
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		chain = append(chain, current)
	}
	return home, chain
}

// openFolderTree starts the picker at the launch folder: rooted at home, opened
// down to where the launch folder is, with the cursor on it.
func (m *tuiModel) openFolderTree() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = m.workingDir
	}
	root, chain := treeRoot(home, m.workingDir)
	tree := folderTree{
		root:     root,
		expanded: map[string]bool{root: true},
		children: map[string][]treeEntry{},
		repos:    map[string]repoInfo{},
	}
	tree.childrenOf(root)
	for _, dir := range chain {
		tree.childrenOf(dir)
		tree.expanded[dir] = true
	}
	tree.rebuild()
	tree.cursor = tree.rowFor(m.workingDir)
	m.tree = tree
	m.folderPath, m.folderTail = m.workingDir, 0
}

// ---- rendering ------------------------------------------------------------

// folderTreeContent is the box: the tree, and the folder under the cursor read
// out beside it.
func (m tuiModel) folderTreeContent(width, rows int) []string {
	heading := spread(boxTitle("change launch folder"), m.folderTreeCorner(width), width)
	lines := append([]string{heading, ""}, m.folderTreeBody(width, rows)...)
	return append(lines, "", m.folderTreeFooter(width))
}

// folderTreeBody splits into two panes when the box is wide enough for both,
// and folds the read-out away when it is not, the way the pickers do. The tree
// keeps the space because it is what the keys act on.
func (m tuiModel) folderTreeBody(width, rows int) []string {
	if width < folderTreeMin+dividerWidth+folderReadOutMin {
		return m.folderTreePane(width, rows)
	}
	columns := width - dividerWidth
	tree := min(max(columns*folderTreeShare/100, folderTreeMin), columns-folderReadOutMin, folderTreeMax)
	readOut := columns - tree
	return joinPanes(m.folderTreePane(tree, rows), m.folderReadOut(readOut, rows), tree, readOut, rows)
}

// folderTreeCorner is the heading's right-hand side: the path field while / is
// open, and otherwise the way to it, the hidden toggle, and its key.
func (m tuiModel) folderTreeCorner(width int) string {
	if m.tree.pathMode {
		field := caretView(pen{}, fieldValueStyle, m.folderPath, m.folderTail, folderPathWidth)
		return truncate(helpKeyStyle.Render("/")+" "+field, width)
	}
	state := sectionLabelStyle.Render("off")
	if m.tree.hidden {
		state = liveStyle.Render("on")
	}
	corner := helpKeyStyle.Render("/") + " " + helpDescStyle.Render("go to a path") + keyGap +
		dimStyle.Render("hidden ") + state + helpKeyStyle.Render("  .")
	return truncate(corner, width)
}

// folderTreePane is the tree itself: a heading, the rows windowed around the
// cursor, and a count of what the folder the cursor sits in holds.
func (m tuiModel) folderTreePane(width, rows int) []string {
	head := []string{sectionLabelStyle.Render("  FOLDER"), ""}
	foot := []string{"", dimStyle.Render(truncate(m.folderTreeCount(), width))}
	visible := max(rows-len(head)-len(foot), 1)
	lines := append(head, windowRows(m.folderTreeRowLines(width), m.tree.cursor, visible)...)
	lines = append(lines, foot...)
	return padToRows(lines, rows, -1)
}

// folderTreeRowLines draws every visible row once; folderTreePane windows them.
// A directory keeps its slash, an unreadable one its cross, and the launch
// folder a green mark at the right edge.
func (m tuiModel) folderTreeRowLines(width int) []string {
	lines := make([]string, 0, len(m.tree.rows))
	for index, row := range m.tree.rows {
		selected := index == m.tree.cursor
		ink := selectedPen(selected)
		bar, name := ink.render(lipgloss.NewStyle(), "  "), fieldValueStyle
		if selected {
			bar, name = ink.render(cursorBarStyle, "▌ "), nameActiveStyle
		}
		icon := "  "
		switch {
		case row.entry.isDir && !row.entry.readable:
			icon = "✗ "
			name = statusErrStyle
		case row.entry.isDir && m.tree.expanded[row.entry.path]:
			icon = "▾ "
		case row.entry.isDir:
			icon = "▸ "
		default:
			name = dimStyle
		}
		label := row.entry.name
		if row.entry.isDir && row.entry.path != m.tree.root {
			label += "/"
		}
		indent := strings.Repeat("  ", row.depth)
		marker := ""
		if row.entry.isDir && row.entry.path == m.workingDir {
			marker = liveStyle.Render("● launch folder")
		}
		head := 2 + lipgloss.Width(indent) + 2
		label = truncate(label, max(width-head-lipgloss.Width(marker)-1, 4))
		line := bar + ink.render(lipgloss.NewStyle(), indent) + ink.render(name, icon) + ink.render(name, label)
		if marker != "" {
			if gap := width - lipgloss.Width(line) - lipgloss.Width(marker); gap > 0 {
				line += ink.render(lipgloss.NewStyle(), strings.Repeat(" ", gap)) + marker
			}
		}
		lines = append(lines, padStyled(ink, ansi.Truncate(line, width, ""), width))
	}
	return lines
}

// folderTreeCount says what the directory the cursor row sits in holds: the
// folder itself, how many directories and files are in it, and how many are
// hidden while the toggle is off.
func (m tuiModel) folderTreeCount() string {
	entry, ok := m.tree.selected()
	if !ok {
		return ""
	}
	dir := entry.path
	if entry.path != m.tree.root {
		dir = filepath.Dir(entry.path)
	}
	dirs, files, hidden := 0, 0, 0
	for _, kid := range m.tree.childrenOf(dir) {
		if kid.hidden {
			hidden++
		}
		if kid.isDir {
			dirs++
		} else {
			files++
		}
	}
	parts := []string{"  " + shortenHome(dir), fmt.Sprintf("%d folders, %d files", dirs, files)}
	if hidden > 0 && !m.tree.hidden {
		parts = append(parts, fmt.Sprintf("%d hidden", hidden))
	}
	return strings.Join(parts, " · ")
}

// folderReadOut is what the folder under the cursor is: its path, whether it is
// a repository and on what branch, what it resolves to, and what is inside it.
func (m tuiModel) folderReadOut(width, rows int) []string {
	entry, ok := m.tree.selected()
	if !ok {
		return nil
	}
	label := func(text string) string { return fieldLabelStyle.Render(pad(text, folderReadOutName)) }
	lines := []string{
		sectionLabelStyle.Render("AT ") + fieldValueStyle.Render(truncate(shortenHome(entry.path), max(width-3, 6))),
		"",
	}
	if info := m.tree.repoOf(entry.path); info.repo {
		git := label("git") + fieldValueStyle.Render(truncate(info.branch, max(width-folderReadOutName-18, 6)))
		if info.changed > 0 {
			git += dimStyle.Render(fmt.Sprintf(" · %d files changed", info.changed))
		}
		lines = append(lines, truncateStyled(git, width))
	} else {
		lines = append(lines, truncateStyled(label("git")+dimStyle.Render("not a repository"), width))
	}
	lines = append(lines, truncateStyled(label("resolves to")+dimStyle.Render(truncate(entry.path, max(width-folderReadOutName, 6))), width))
	lines = append(lines, "", sectionLabelStyle.Render("CONTAINS"))
	if !entry.readable {
		lines = append(lines, statusErrStyle.Render("cannot be read"))
		return padToRows(lines, rows, -1)
	}
	var kids []treeEntry
	for _, kid := range m.tree.childrenOf(entry.path) {
		if kid.hidden && !m.tree.hidden {
			continue
		}
		kids = append(kids, kid)
	}
	budget := max(rows-len(lines)-2, 0)
	for index, kid := range kids {
		if index >= budget {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("+ %d more", len(kids)-index)))
			break
		}
		if kid.isDir {
			lines = append(lines, fieldValueStyle.Render(truncate(kid.name+"/", width)))
		} else {
			lines = append(lines, dimStyle.Render(truncate(kid.name, width)))
		}
	}
	lines = append(lines, "", dimStyle.Render(truncate("↵ opens it in the tree. s makes it the launch folder.", width)))
	return padToRows(lines, rows, -1)
}

func (m tuiModel) folderTreeFooter(width int) string {
	set := "set"
	if entry, ok := m.tree.selected(); ok {
		set = "set " + shortenHome(entry.path)
	}
	return boxFooter(width, helpEntry{"esc", "cancel"},
		helpEntry{"↑↓", "move"}, helpEntry{"↵", "open"}, helpEntry{"←", "close / up"},
		helpEntry{"s", truncate(set, 32)}, helpEntry{"/", "path"}, helpEntry{".", "hidden"})
}
