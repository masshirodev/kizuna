package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func pressKey(t *testing.T, m tuiModel, key tea.KeyMsg) (tuiModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(key)
	return updated.(tuiModel), cmd
}

func boxRows(m tuiModel) int {
	return len(boxContent(m.View()))
}

func TestCtrlFSwitchesThePickerBetweenItsTwoSearchesWithoutResizingIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	said := boxRows(m)
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.search.on {
		t.Fatal("ctrl+f did not leave the search of what was said")
	}
	m.recentAll, m.recentAllLoaded = []recordedSession{{session: instanceSession{id: "s", title: "a conversation"}, folder: "/work", profile: "claude-max"}}, true
	box := strings.Join(boxContent(m.View()), "\n")
	if !strings.Contains(box, "/ search title, folder or id") || strings.Contains(box, "searches what was said") {
		t.Fatalf("titles mode is not drawn as the titles filter:\n%s", box)
	}
	if titles := boxRows(m); titles != said {
		t.Fatalf("the box is %d rows in titles mode and %d searching what was said", titles, said)
	}
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.search.on || m.search.query != "sqlite busy" {
		t.Fatalf("back in search mode = %v with %q, want the query kept", m.search.on, m.search.query)
	}
}

func TestTypingSearchesAfterAPauseAndDimsWhatWasThereUntilItLands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.search.focus = searchOnField
	m, cmd := pressKey(t, m, keyRunes("n"))
	if m.search.query != "sqlite busyn" || cmd == nil {
		t.Fatalf("typing n in the field: query %q, cmd %v; want it typed, not a match step", m.search.query, cmd)
	}
	if !m.search.stale() || !m.search.prefix {
		t.Fatalf("stale %v prefix %v, want the old result dimmed and the word matched as a prefix", m.search.stale(), m.search.prefix)
	}
	if _, ok := m.selectedHit(); ok {
		t.Fatal("a stale result still offers a hit to act on")
	}
	list := strings.Join(boxContent(m.View()), "\n")
	if !strings.Contains(list, "searching  sqlite  AND  busyn*  …") || !strings.Contains(list, "searching…") {
		t.Fatalf("the box does not say it is searching:\n%s", list)
	}
	// A result for an older query is dropped.
	updated, _ := m.Update(searchResultMsg{seq: m.search.seq - 1, result: searchResult{hits: nil}})
	if got := updated.(tuiModel); len(got.search.result.hits) == 0 {
		t.Fatal("a result for an older query replaced the one on screen")
	}
	// The tick for the current query starts it.
	if _, cmd := m.Update(searchTickMsg{seq: m.search.seq}); cmd == nil {
		t.Fatal("the current query's tick did not start a search")
	}
	if _, cmd := m.Update(searchTickMsg{seq: m.search.seq - 1}); cmd != nil {
		t.Fatal("an older query's tick started a search")
	}
}

func TestDownHandsTheKeysToTheResultsAndSlashTakesThemBack(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.search.focus = searchOnField
	seq := m.search.seq
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.search.focus != searchOnResults || m.search.seq != seq {
		t.Fatalf("focus %v seq %d→%d, want the results to have the keys and the query left as it was", m.search.focus, seq, m.search.seq)
	}
	m, _ = pressKey(t, m, keyRunes("n"))
	if m.search.at != bestAt(m.visibleHits()[0])+1 || m.search.query != "sqlite busy" {
		t.Fatalf("n on the results: at %d, query %q; want the next match", m.search.at, m.search.query)
	}
	m, _ = pressKey(t, m, keyRunes("j"))
	if m.search.cursor != 1 || m.search.at != 0 {
		t.Fatalf("j: cursor %d at %d, want the second hit on its best match", m.search.cursor, m.search.at)
	}
	m, _ = pressKey(t, m, keyRunes("/"))
	if m.search.focus != searchOnField {
		t.Fatal("/ did not give the keys back to the field")
	}
}

func TestAHitIsHandedOffOrCopiedFromTheResults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	copied, cmd := pressKey(t, m, keyRunes("y"))
	if cmd == nil || copied.status != "copied  kizuna claude-personal resume 3c9e0d12-7b44-4f0e-9a51-2d6c0b1e8f37" {
		t.Fatalf("y: status %q, cmd %v", copied.status, cmd)
	}
	handed, _ := pressKey(t, m, keyRunes("H"))
	if handed.mode != tuiHandoffTo || handed.handoff.source.session.id != "3c9e0d12-7b44-4f0e-9a51-2d6c0b1e8f37" {
		t.Fatalf("H: mode %v, leaving %q; want the hit leaving and the destinations asked", handed.mode, handed.handoff.source.session.id)
	}
}

func TestEnterResumesTheHitInItsOwnFolderOrSaysWhyNot(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	gone := filepath.Join(t.TempDir(), "moved-away")
	m.search.result.hits[0].record.folder = gone
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiRecent || !strings.Contains(m.status, "is gone; that session cannot be resumed") {
		t.Fatalf("mode %v status %q, want the box kept open and the moved folder reported", m.mode, m.status)
	}
}

func TestTheHandoffPickerSearchesToo(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.mode = tuiHandoff
	box := strings.Join(boxContent(m.View()), "\n")
	if !strings.Contains(box, "HAND OFF") || !strings.Contains(box, "what was said") || !strings.Contains(box, "hand off from ~/projects/kizuna") {
		t.Fatalf("the H picker in search mode:\n%s", box)
	}
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != tuiHandoffTo {
		t.Fatalf("↵ in the H picker = mode %v, want the next step", m.mode)
	}
}

func TestThePaletteAndTheBoardOpenTheSearchOnEveryAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := wideModel(testProfiles())
	m.search.coverage = indexCoverage{searched: 612, total: 965, known: true}
	if note, _ := m.paletteNote("ctrl+f"); note != "612 of 965 indexed" {
		t.Fatalf("palette note = %q", note)
	}
	m.search.coverage.searched = 965
	if note, _ := m.paletteNote("ctrl+f"); note != "965 indexed" {
		t.Fatalf("palette note = %q", note)
	}
	m.openPalette()
	m.paletteFilter = "search what"
	m.settlePaletteRow()
	if matches := m.paletteMatches(); len(matches) != 1 || matches[0].key != "ctrl+f" {
		t.Fatalf("palette matches = %+v", matches)
	}
	opened, cmd := pressKey(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if opened.mode != tuiRecent || !opened.search.on || !opened.pickerAll || opened.search.focus != searchOnField || cmd == nil {
		t.Fatalf("palette ↵: mode %v on %v all %v focus %v", opened.mode, opened.search.on, opened.pickerAll, opened.search.focus)
	}
}

func TestBackfillProgressFillsTheListInWithoutDimmingIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.search.backfilling = true
	updates := make(chan backfillMsg)
	updated, cmd := m.Update(backfillMsg{coverage: indexCoverage{searched: 700, total: 965, known: true}, updates: updates})
	got := updated.(tuiModel)
	if got.search.stale() || got.search.coverage.searched != 700 || !got.search.backfilling || cmd == nil {
		t.Fatalf("after progress: stale %v coverage %+v backfilling %v", got.search.stale(), got.search.coverage, got.search.backfilling)
	}
	updated, _ = got.Update(backfillMsg{coverage: indexCoverage{searched: 965, total: 965, known: true}, done: true})
	if got = updated.(tuiModel); got.search.backfilling {
		t.Fatal("the backfill is still marked running after its last report")
	}
}

func TestTheNarrowFoldKeepsTheSnippetAndDropsTheMatchKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.width = 88
	box := boxContent(m.View())
	text := strings.Join(box, "\n")
	if strings.Contains(text, "n N") || strings.Contains(text, "PREVIEW") || !strings.Contains(text, "you  “…so resume fails") {
		t.Fatalf("narrow box:\n%s", text)
	}
	if !strings.HasPrefix(box[0], "RESUME    all accounts   a") {
		t.Fatalf("narrow heading = %q, want only the current chips", box[0])
	}
}

func TestALongMatchingTurnOpensAtItsMatch(t *testing.T) {
	text := strings.Repeat("filler words that say nothing at all. ", 20) + "then " + matchOpen + "sqlite" + matchClose + " came up"
	rows := fromFirstMatch(wrapCells(markedCells(text), 40), matchTurnRows, 40)
	shown := capRows(rows, matchTurnRows, 40)
	found := false
	for _, row := range shown {
		for _, cell := range row {
			found = found || cell.match
		}
	}
	if !found || shown[0][0].r != '…' {
		t.Fatalf("the match is not on screen, or the cut is unmarked: %q", renderCells(pen{}, shown[0], hintStyle, matchStyle))
	}
	short := wrapCells(markedCells(matchOpen+"sqlite"+matchClose+" first"), 40)
	if got := fromFirstMatch(short, matchTurnRows, 40); len(got) != 1 || got[0][0].r == '…' {
		t.Fatal("a turn whose match is already on screen was moved")
	}
}

func TestJapaneseWrapsByWidthAndNeverStartsALineWithClosingPunctuation(t *testing.T) {
	text := "待ってから再試行します。飛ばしてしまうと「まだ読んでいない」と「何も言っていない」の区別がつかなくなるので。"
	for width := 10; width <= 30; width++ {
		for _, row := range wrapCells(markedCells(text), width) {
			if cellsWidth(row) > width {
				t.Fatalf("width %d: a row is %d cells", width, cellsWidth(row))
			}
			if len(row) > 0 && strings.ContainsRune(noLineStart, row[0].r) {
				t.Fatalf("width %d: a row starts with %q", width, string(row[0].r))
			}
		}
	}
	if got := truncate("セッション検索のインデックス設計", 10); ansi.StringWidth(got) > 10 {
		t.Fatalf("truncate overflowed: %q is %d cells", got, ansi.StringWidth(got))
	}
}

func TestSearchCommandPrintsHitsAndJSON(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	writeConversation(t, root, "cl", "ses-a", at, "why does resume hit SQLITE_BUSY", "a second reader")
	cfg := Config{Profiles: indexedProfiles()}
	var out, errs strings.Builder
	if err := searchCommand(cfg, []string{"--print", "sqlite", "busy"}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "you: why does resume hit [SQLITE]_[BUSY]") || !strings.Contains(out.String(), "kizuna cl resume ses-a") {
		t.Fatalf("--print:\n%s", out.String())
	}
	out.Reset()
	if err := searchCommand(cfg, []string{"--json", "--profile", "cl", "sqlite"}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"read_as": "sqlite"`, `"said_by": "you"`, `"resume": "kizuna cl resume ses-a"`, `"searched": 1`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("--json lacks %s:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := searchCommand(cfg, []string{"--print", "kubernetes"}, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Nothing said matches kubernetes") {
		t.Fatalf("empty --print:\n%s", out.String())
	}
	if err := searchCommand(cfg, []string{"--print"}, &out, &errs); err == nil {
		t.Fatal("a search with no words did not fail")
	}
	if err := searchCommand(cfg, []string{"--print", "--since", "13 Oct", "x"}, &out, &errs); err == nil {
		t.Fatal("--since took a date it cannot read")
	}
}

func TestRepoFilterMatchesAPathOrARepositoryName(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "sub")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	byPath := repoFolderFilter(dir)
	if !byPath(inside) || !byPath(dir) || byPath(t.TempDir()) {
		t.Fatal("the path filter accepted the wrong folders")
	}
	byName := repoFolderFilter(filepath.Base(inside))
	if !byName(inside) || byName(dir) {
		t.Fatal("a folder outside git is not matched by its own name")
	}
}

func TestSearchSessionsOverMCP(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeConversation(t, root, "cl2", "ses-b", time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC), "where did we decide the merge lock", "in the store reader")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_sessions","arguments":{"query":"merge lock"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search_sessions","arguments":{"query":"merge lock","profile":"cl"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_sessions","arguments":{}}}`,
	}, "\n")
	var out strings.Builder
	if err := mcpServeCommand(Config{Profiles: indexedProfiles()}, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d responses:\n%s", len(lines), out.String())
	}
	for index, want := range []string{`"search_sessions"`, `kizuna cl2 resume ses-b`, `\"hits\": []`, `"isError":true`} {
		if !strings.Contains(lines[index], want) {
			t.Fatalf("response %d = %s, want it to contain %s", index+1, lines[index], want)
		}
	}
}

func TestCtrlFSwitchesEvenWhileTheTitlesFilterIsBeingTyped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := searchDesignModel(t)
	m.search.on, m.recentSearching, m.recentFilter = false, true, "kiz"
	m, _ = pressKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlF})
	if !m.search.on || m.recentSearching {
		t.Fatalf("ctrl+f while typing a title filter: search %v, typing %v", m.search.on, m.recentSearching)
	}
}
