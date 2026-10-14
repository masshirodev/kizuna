package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The search handoff's sample data (doc/handoffs/Session search.dc.html, its
// SAMPLE DATA table and the CTX1/CTX3 contexts in its script): the query
// "sqlite busy", nine hits across six accounts and five providers, at Tue 13 Oct
// 2026 14:05, with 965 conversations indexed. The frames are drawn from it, so
// the box can be compared with the mock line for line.

type designHit struct {
	when    time.Time
	profile string
	title   string
	folder  string
	id      string
	matches int
	to      string
	you     bool
	snippet string
}

func searchDesignHits(home string) []designHit {
	day := func(month time.Month, d, hh, mm int) time.Time {
		return time.Date(2026, month, d, hh, mm, 0, 0, time.Local)
	}
	kizuna := filepath.Join(home, "projects/kizuna")
	return []designHit{
		{day(10, 12, 20, 0), "claude-personal", "Investigate SQLITE_BUSY on resume", kizuna, "3c9e0d12-7b44-4f0e-9a51-2d6c0b1e8f37", 5, "codex-work", true, "…so resume fails with [SQLITE]_[BUSY] only while opencode is still running in the other pane?"},
		{day(10, 13, 14, 2), "codex-work", "Investigate SQLITE_BUSY on resume", kizuna, "019a2f4e-8c1d-7e55-b0a3-5f6e2d9c4a10", 3, "", false, "…set [busy]_timeout so a second [sqlite] reader waits for the lock…"},
		{day(10, 13, 11, 37), "claude-max", "セッション検索のインデックス設計", kizuna, "7a0c5e21-93bd-4c6a-8f12-e4d07b3a9c55", 4, "", true, "backfill 中に [SQLITE]_[BUSY] が出たら、少し待ってから再試行する？"},
		{day(10, 9, 16, 0), "opencode-go", "OpenCode store reader", kizuna, "ses_4f2a91c0e8ffYq3TzR7wKd2m", 2, "", false, "…a read-only [sqlite] open still gets [busy] while opencode checkpoints the WAL…"},
		{day(10, 2, 16, 0), "codex-personal", "Billing webhooks retry queue", filepath.Join(home, "work/billing"), "0199c7d2-4be0-7a13-9e6f-21c8b5d0f7a4", 1, "", false, "…writes go [busy] because the worker and the API share one [sqlite] file…"},
		{day(9, 28, 16, 0), "antigravity-personal", "Wave runner: parallel workers", filepath.Join(home, "projects/waves"), "b18e6f03-2d7a-4e91-a5c4-9f0d3e7b2a68", 1, "", true, "workers die with [busy] errors half the time, is it the [sqlite] file?"},
		{day(10, 13, 11, 37), "claude-personal", "Handoff brief: trim injected blocks", kizuna, "9f13aa27-01c2-4d8e-b6f5-3a7c2e9d0b14", 1, "", false, "…the brief reader retries on [busy], like the [sqlite] index does…"},
		{day(9, 21, 16, 0), "deepseek", "Notes sync between laptops", filepath.Join(home, "notes"), "d4c2b9e7-6a1f-4083-bc5e-7f2a0d9e3c61", 1, "", true, "is syncthing why the [sqlite] file says [busy] sometimes?"},
		{day(9, 15, 16, 0), "claude-max", "", filepath.Join(home, "scratch"), "e5a7f1c3-08bd-4f26-9d4a-6b3e2c1f8a07", 1, "", false, "…[SQLITE]_[BUSY] is result code 5, and retrying it blindly hides the cause…"},
	}
}

func bracketsToMarks(text string) string {
	return strings.NewReplacer("[", matchOpen, "]", matchClose).Replace(text)
}

// searchDesignModel is the resume box in search mode over the sample, with the
// keys on the results and the cursor on the first hit, as frame 2a draws it.
func searchDesignModel(t *testing.T) tuiModel {
	t.Helper()
	home, _ := os.UserHomeDir()
	now := time.Date(2026, 10, 13, 14, 5, 0, 0, time.Local)
	profiles := sortedProfiles([]Profile{
		{Name: "claude-max", Provider: "claude", Command: "claude"},
		{Name: "codex-personal", Provider: "codex", Command: "codex"},
		{Name: "codex-work", Provider: "codex", Command: "codex"},
		{Name: "claude-personal", Provider: "claude", Command: "claude"},
		{Name: "antigravity-personal", Provider: "antigravity", Command: "agy"},
		{Name: "deepseek", Provider: "deepseek", Command: "deepseek"},
		{Name: "opencode-go", Provider: "opencode", Command: "opencode"},
	})
	m := tuiModel{profiles: profiles, width: 150, height: 42, loaded: true, now: now, workingDir: filepath.Join(home, "projects/kizuna"),
		mode: tuiRecent, pickerAll: true, lineage: map[string]lineageLink{}}
	m.followSelection("claude-personal")
	var hits []searchHit
	for index, sample := range searchDesignHits(home) {
		provider := profileNamed(profiles, sample.profile).Provider
		seqs := make([]int, sample.matches)
		for i := range seqs {
			seqs[i] = i
		}
		hit := searchHit{provider: provider, matches: sample.matches, seqs: seqs, bestFromUser: sample.you,
			snippet: bracketsToMarks(sample.snippet), rank: float64(index)}
		hit.record = recordedSession{session: instanceSession{id: sample.id, title: sample.title}, folder: sample.folder,
			lastActive: sample.when, profile: sample.profile}
		if sample.to != "" {
			m.lineage[sample.id] = lineageLink{TargetProfile: sample.to}
		}
		hits = append(hits, hit)
	}
	// Hit 1's matches sit at turns 9 14 61 140 188 of 212, opened on 14; hit 3's
	// at 8 21 22 40 of 44, opened on 8 (turns count from 1 on screen).
	hits[0].seqs, hits[0].best = []int{8, 13, 60, 139, 187}, 13
	hits[2].seqs, hits[2].best = []int{7, 20, 21, 39}, 7
	// The 14 headless conversations the sample hides.
	for index := range 14 {
		hits = append(hits, searchHit{provider: "claude", matches: 1, seqs: []int{0}, rank: 100,
			record: recordedSession{session: instanceSession{id: "headless-" + string(rune('a'+index))}, profile: "claude-max", headless: true}})
	}
	m.search = searchState{on: true, query: "sqlite busy", tail: 0, focus: searchOnResults, seq: 1, landed: 1,
		result:   searchResult{query: "sqlite busy", reading: readQuery("sqlite busy", false), hits: hits},
		coverage: indexCoverage{searched: 965, total: 965, known: true},
		contexts: map[string]hitContext{}}
	m.search.at = bestAt(hits[0])
	m.search.contexts[hitKey(hits[0], 13)] = hitContext{profile: "claude-personal", session: hits[0].record.session.id, seq: 13, total: 212,
		earlier: true, later: true,
		turns: []contextTurn{
			{seq: 12, text: "The resume path opens the OpenCode store with mode=ro, but OpenCode is mid-write in the other pane, and a read-only open still has to wait for its write lock to clear."},
			{seq: 13, fromUser: true, match: true, text: bracketsToMarks("so resume fails with [SQLITE]_[BUSY] only while opencode is still running in the other pane? can we wait for the lock instead of failing straight away")},
			{seq: 14, text: "Yes. A timeout on the connection makes the reader wait for the lock rather than return at once. Five seconds covers a checkpoint; past that it is fair to report it."},
		},
		ending: []contextTurn{
			{seq: 210, fromUser: true, text: "good, ship it, and give the handoff reader the same timeout"},
			{seq: 211, text: "Done. Both readers wait up to 5 s before reporting the store as locked; go test ./... passes."},
		}}
	m.search.contexts[hitKey(hits[2], 7)] = hitContext{profile: "claude-max", session: hits[2].record.session.id, seq: 7, total: 44,
		earlier: true, later: true,
		turns: []contextTurn{
			{seq: 6, text: "バックフィルは新しい会話から順に読み込みます。検索の対象は、その時点までに読み込んだ分だけです。"},
			{seq: 7, fromUser: true, match: true, text: bracketsToMarks("backfill 中に [SQLITE]_[BUSY] が出たら、少し待ってから再試行する？それともその会話は飛ばす？")},
			{seq: 8, text: "待ってから再試行します。飛ばしてしまうと「まだ読んでいない」と「何も言っていない」の区別がつかなくなるので。"},
		},
		ending: []contextTurn{
			{seq: 42, fromUser: true, text: "それでいこう。進捗の表示も入れて"},
			{seq: 43, text: "入れました。「965 件中 612 件を検索済み」と出ます。"},
		}}
	return m
}

// searchDesignFrames are the handoff's frames as the model draws them, keyed
// as the handoff keys them.
func searchDesignFrames(t *testing.T) map[string]tuiModel {
	t.Helper()
	frames := map[string]tuiModel{}
	base := searchDesignModel(t)
	frames["f2a"] = base

	b := searchDesignModel(t)
	b.search.newest = true
	sortHits(b.search.result.hits, true)
	b.search.cursor = 1
	b.search.at = 0
	b.setStatus(statusOK, "copied  kizuna claude-max resume 7a0c5e21-93bd-4c6a-8f12-e4d07b3a9c55")
	frames["f2b"] = b

	c := searchDesignModel(t)
	c.search.query, c.search.tail, c.search.focus, c.search.seq, c.search.prefix = "sqlite bu", 0, searchOnField, 2, true
	frames["f2c"] = c

	d := searchDesignModel(t)
	var partial []searchHit
	for index, hit := range d.search.result.hits {
		if index == 3 || index == 5 || index == 7 || index == 8 {
			continue
		}
		partial = append(partial, hit)
	}
	d.search.result.hits = partial
	d.search.cursor = 1
	d.search.coverage = indexCoverage{searched: 612, total: 965, known: true, failed: []backfillFailure{{profile: "opencode-go", unread: 41, store: true}}}
	frames["f2d"] = d

	e := searchDesignModel(t)
	e.search.query = "kubernetes ingress"
	e.search.result = searchResult{query: e.search.query, reading: readQuery(e.search.query, false)}
	e.search.counts = []termCount{{"kubernetes", 3}, {"ingress", 0}, {"both", 0}}
	e.search.coverage = indexCoverage{searched: 612, total: 965, known: true}
	frames["f2e"] = e

	f := searchDesignModel(t)
	f.pickerAll = false
	f.search.query = "sqlite busyy"
	f.search.result = searchResult{query: f.search.query, reading: readQuery(f.search.query, false)}
	f.search.counts = []termCount{{"sqlite", 7}, {"busyy", 0}, {"both", 0}}
	f.search.coverage = indexCoverage{searched: 212, total: 212, known: true}
	frames["f2f"] = f

	g := searchDesignModel(t)
	g.width, g.height = 88, 42
	frames["f2g"] = g

	h := searchDesignModel(t)
	h.search.on = false
	h.recentSearching, h.recentFilter, h.recentTail = true, "sqlite busy", 0
	h.recentAll = nil
	for index := range 14 {
		h.recentAll = append(h.recentAll, recordedSession{session: instanceSession{id: "run-" + string(rune('a'+index)), title: "agent/w1"}, profile: "claude-max", headless: true})
	}
	frames["f2h"] = h
	return frames
}

// boxContent is the box's inside, cell for cell: every row between its top and
// bottom border, without the border and the padding, as plain text with each
// wide character followed by an empty cell.
func boxContent(view string) []string {
	var out []string
	inside := false
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		cells := cellsOf(line)
		left := -1
		for index, cell := range cells {
			if cell == "╭" || cell == "│" || cell == "╰" {
				left = index
				break
			}
		}
		if left < 0 {
			continue
		}
		switch cells[left] {
		case "╭":
			inside = true
			continue
		case "╰":
			return out
		}
		if !inside {
			continue
		}
		right := -1
		for index := len(cells) - 1; index > left; index-- {
			if cells[index] == "│" {
				right = index
				break
			}
		}
		if right < 0 {
			continue
		}
		out = append(out, strings.TrimRight(strings.Join(cells[left+3:right-2], ""), " "))
	}
	return out
}

// cellsOf splits a line into terminal cells; a wide character takes its cell
// and leaves the next one empty.
func cellsOf(line string) []string {
	var cells []string
	for _, r := range line {
		cells = append(cells, string(r))
		if ansi.StringWidth(string(r)) == 2 {
			cells = append(cells, "")
		}
	}
	return cells
}

// searchDesignGolden is every sample frame's box content, checked against the
// handoff's own rendering cell for cell: its script was run under node and its
// boxes compared with these. The one difference left is deliberate and written
// down in doc/session-search.md ("Where it departs from the handoff"): the
// count lines under the list are indented two cells, as the titles list's
// always were, where the handoff draws them at the list's edge.
const searchDesignGolden = "testdata/search-design-frames.json"

// The search box at the design's frame, with the design's data, is the mock.
// Every row is pinned rather than a few figures, because hand-set widths (the
// 7-cell MATCHES column, the 9-cell snippet indent, the 22-cell progress bar,
// two lines per hit) are what a tidy-up would round away. Set
// KIZUNA_UPDATE_SEARCH_GOLDEN=1 to rewrite the file after a deliberate change,
// and check the result against the handoff again.
func TestSearchBoxMatchesTheDesignFrames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	got := map[string][]string{}
	for name, m := range searchDesignFrames(t) {
		got[name] = boxContent(m.View())
	}
	if os.Getenv("KIZUNA_UPDATE_SEARCH_GOLDEN") != "" {
		data, _ := json.MarshalIndent(got, "", " ")
		if err := os.WriteFile(searchDesignGolden, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(searchDesignGolden)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string][]string
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for name, lines := range want {
		frame := got[name]
		if len(frame) != len(lines) {
			t.Errorf("%s: %d rows, want %d", name, len(frame), len(lines))
			continue
		}
		for row, line := range lines {
			if strings.ReplaceAll(frame[row], home, "~") != strings.ReplaceAll(line, home, "~") {
				t.Errorf("%s row %d\n got: %q\nwant: %q", name, row, frame[row], line)
			}
		}
	}
}
