package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeTurn is one transcript line saying text, from the user or the agent.
func claudeTurn(fromUser bool, text string, at time.Time) string {
	role := "assistant"
	if fromUser {
		role = "user"
	}
	return fmt.Sprintf(`{"type":%q,"cwd":"/work","entrypoint":"cli","timestamp":%q,"message":{"content":[{"type":"text","text":%q}]}}`,
		role, at.UTC().Format("2006-01-02T15:04:05.000Z"), text)
}

// writeConversation writes a Claude transcript of alternating turns, the first
// from the user, last active at the given time.
func writeConversation(t *testing.T, root, profile, session string, at time.Time, turns ...string) string {
	t.Helper()
	lines := make([]string, len(turns))
	for index, text := range turns {
		lines[index] = claudeTurn(index%2 == 0, text, at)
	}
	writeTranscriptLines(t, root, profile, "-work", session, lines...)
	return claudeTranscriptPath(root, profile, "-work", session)
}

func indexedProfiles() []Profile {
	return []Profile{{Name: "cl", Provider: "claude", Command: "claude"}, {Name: "cl2", Provider: "claude", Command: "claude"}}
}

func search(t *testing.T, text string, mutate ...func(*searchQuery)) searchResult {
	t.Helper()
	query := searchQuery{text: text, profiles: indexedProfiles()}
	for _, change := range mutate {
		change(&query)
	}
	result := searchSessions(query)
	if result.err != nil {
		t.Fatalf("search %q: %v", text, result.err)
	}
	return result
}

func hitIDs(result searchResult) []string {
	ids := make([]string, len(result.hits))
	for index, hit := range result.hits {
		ids[index] = hit.record.session.id
	}
	return ids
}

func TestAnOlderIndexDropsItsTurnCacheAndGainsTheSearchIndex(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path, err := sessionIndexPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE turn (profile TEXT NOT NULL, id TEXT NOT NULL, seq INTEGER NOT NULL, from_user INTEGER NOT NULL, text TEXT NOT NULL, PRIMARY KEY (profile, id, seq))`,
		`CREATE TABLE turn_source (profile TEXT NOT NULL, id TEXT NOT NULL, fingerprint TEXT NOT NULL, PRIMARY KEY (profile, id))`,
		`INSERT INTO turn VALUES ('cl', 's', 0, 1, 'cached before the search index')`,
		`INSERT INTO turn_source VALUES ('cl', 's', '1:2')`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	db, err := openSessionIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, turns, sources int
	db.QueryRow(`PRAGMA user_version`).Scan(&version)
	db.QueryRow(`SELECT COUNT(*) FROM turn`).Scan(&turns)
	db.QueryRow(`SELECT COUNT(*) FROM turn_source`).Scan(&sources)
	if version != sessionIndexVersion || turns != 0 || sources != 0 {
		t.Fatalf("after migration: version %d, %d turns, %d sources; want %d, 0, 0", version, turns, sources, sessionIndexVersion)
	}
	if _, err := db.Exec(`INSERT INTO turn (profile, id, seq, from_user, text) VALUES ('cl', 's', 0, 1, 'now indexed')`); err != nil {
		t.Fatal(err)
	}
	var found int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_fts WHERE turn_fts MATCH 'indexed'`).Scan(&found); err != nil || found != 1 {
		t.Fatalf("turn_fts found %d, %v; want the inserted turn", found, err)
	}
}

func TestBackfillReadsEveryConversationOnceAndAStaleOneAgain(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	stale := writeConversation(t, root, "cl", "ses-a", at, "where does SQLITE_BUSY come from", "a second reader")
	writeConversation(t, root, "cl", "ses-b", at.Add(time.Hour), "unrelated", "nothing here")
	writeConversation(t, root, "cl2", "ses-c", at.Add(2*time.Hour), "the sqlite file is busy", "set busy_timeout")

	var reports []indexCoverage
	coverage := backfillSessionTurns(indexedProfiles(), func(c indexCoverage) { reports = append(reports, c) })
	if !coverage.complete() || coverage.total != 3 || coverage.searched != 3 || len(coverage.failed) != 0 {
		t.Fatalf("coverage = %+v, want 3 of 3", coverage)
	}
	if first := reports[0]; first.searched != 0 || first.total != 3 {
		t.Fatalf("first report = %+v, want 0 of 3 before reading", first)
	}
	if got := readIndexCoverage(indexedProfiles()); got.searched != 3 || got.total != 3 {
		t.Fatalf("readIndexCoverage = %+v", got)
	}

	// A second backfill finds nothing to read.
	reports = nil
	backfillSessionTurns(indexedProfiles(), func(c indexCoverage) { reports = append(reports, c) })
	if len(reports) != 1 || reports[0].searched != 3 {
		t.Fatalf("second backfill reported %+v, want one report of 3 of 3 and no batches", reports)
	}

	// A transcript that grows is stale until it is read again.
	writeTranscriptFile(t, stale, claudeTurn(true, "where does SQLITE_BUSY come from", at),
		claudeTurn(false, "a second reader", at), claudeTurn(true, "and kubernetes ingress?", at.Add(3*time.Hour)))
	if got := search(t, "kubernetes"); len(got.hits) != 0 {
		t.Fatalf("found %v before the backfill read the new turn", hitIDs(got))
	}
	backfillSessionTurns(indexedProfiles(), nil)
	if got := search(t, "kubernetes"); len(got.hits) != 1 || got.hits[0].record.session.id != "ses-a" {
		t.Fatalf("after the backfill: %v, want ses-a", hitIDs(got))
	}
}

func TestBackfillNamesASourceItCouldNotRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	path := writeConversation(t, root, "cl", "ses-a", at, "hello", "hi")
	profiles := indexedProfiles()[:1]
	// Index the row, then make the transcript unreadable while keeping it listed.
	pending, _, ok := pendingBackfill(profiles)
	if !ok || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, ok)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running as a user that reads mode-0 files")
	}
	coverage := backfillSessionTurns(profiles, nil)
	if coverage.complete() || len(coverage.failed) != 1 || coverage.failed[0].unread != 1 {
		t.Fatalf("coverage = %+v, want one unread conversation named", coverage)
	}
	if got := coverage.failed[0].describe(); got != "cl: 1 conversation could not be read" {
		t.Fatalf("describe = %q", got)
	}
}

func TestSearchGroupsMatchingTurnsByConversationAndRanksThem(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	writeConversation(t, root, "cl", "ses-many", at,
		"resume fails with SQLITE_BUSY", "a reader waits on the writer",
		"still SQLITE_BUSY after the timeout", "busy again", "the sqlite store is busy")
	writeConversation(t, root, "cl2", "ses-once", at.Add(time.Hour),
		"why is the sqlite file busy", "syncthing")
	writeConversation(t, root, "cl", "ses-none", at.Add(2*time.Hour), "unrelated", "nothing")
	backfillSessionTurns(indexedProfiles(), nil)

	result := search(t, "sqlite busy")
	if len(result.hits) != 2 {
		t.Fatalf("hits = %v, want two conversations", hitIDs(result))
	}
	many := result.hits[0]
	if many.record.session.id != "ses-once" {
		many = result.hits[1]
	}
	if many.record.session.id == "ses-once" {
		many = result.hits[0]
	}
	if many.matches != 3 || len(many.seqs) != 3 || many.seqs[0] != 0 || many.seqs[2] != 4 {
		t.Fatalf("ses-many matched %d turns at %v, want 3 at [0 2 4]", many.matches, many.seqs)
	}
	if many.turns != 5 || many.record.profile != "cl" || many.provider != "claude" {
		t.Fatalf("ses-many = %+v", many)
	}
	if !strings.Contains(many.snippet, matchOpen) {
		t.Fatalf("snippet %q marks nothing", many.snippet)
	}
	// The underscore separates tokens, so SQLITE_BUSY matches both words.
	if got := search(t, "busy"); len(got.hits) != 2 {
		t.Fatalf("busy found %v", hitIDs(got))
	}

	newest := search(t, "sqlite busy", func(q *searchQuery) { q.newest = true })
	if hitIDs(newest)[0] != "ses-once" {
		t.Fatalf("newest first = %v, want ses-once first", hitIDs(newest))
	}
	if got := search(t, "sqlite busy", func(q *searchQuery) { q.profiles = indexedProfiles()[1:] }); len(got.hits) != 1 || got.hits[0].record.profile != "cl2" {
		t.Fatalf("scoped to cl2 = %v", hitIDs(got))
	}
	if result.coverage.searched != 3 || result.coverage.total != 3 {
		t.Fatalf("coverage = %+v", result.coverage)
	}
}

func TestSearchHidesHeadlessAndCanKeepOnlyWhatYouSaid(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	writeConversation(t, root, "cl", "ses-talk", at, "hello", "the answer mentions lattice")
	writeTranscriptLines(t, root, "cl", "-work", "ses-run",
		`{"type":"user","cwd":"/work","entrypoint":"sdk-cli","timestamp":"2026-10-13T11:00:00.000Z","message":{"content":[{"type":"text","text":"lattice wave worker"}]}}`)
	backfillSessionTurns(indexedProfiles(), nil)

	if got := search(t, "lattice"); len(got.hits) != 1 || got.hits[0].record.session.id != "ses-talk" {
		t.Fatalf("default = %v, want the conversation only", hitIDs(got))
	}
	if got := search(t, "lattice", func(q *searchQuery) { q.headless = true }); len(got.hits) != 2 {
		t.Fatalf("with headless = %v", hitIDs(got))
	}
	if got := search(t, "lattice", func(q *searchQuery) { q.fromUser = true }); len(got.hits) != 0 {
		t.Fatalf("from user = %v, want nothing: only the agent said it", hitIDs(got))
	}
}

func TestAQueryFTSCannotParseIsSearchedAsAPhrase(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	writeConversation(t, root, "cl", "ses-a", at, "the build died: ENOSPC: no space left on device", "clear the cache")
	writeConversation(t, root, "cl", "ses-b", at, "no space for it", "left ENOSPC alone")
	backfillSessionTurns(indexedProfiles(), nil)

	result := search(t, "ENOSPC: no space left")
	if !result.reading.phrase || len(result.hits) != 1 || result.hits[0].record.session.id != "ses-a" {
		t.Fatalf("reading %+v, hits %v; want a phrase finding ses-a", result.reading, hitIDs(result))
	}
	if got := result.reading.describe(); got != `searched as the phrase "ENOSPC: no space left"` {
		t.Fatalf("describe = %q", got)
	}
}

func TestTheWordBeingTypedIsMatchedAsAPrefix(t *testing.T) {
	cases := []struct {
		text   string
		prefix bool
		fts    string
		terms  []string
	}{
		{"sqlite bu", true, "sqlite bu*", []string{"sqlite", "bu*"}},
		{"sqlite bu", false, "sqlite bu", []string{"sqlite", "bu"}},
		{"sqlite bu ", true, "sqlite bu", []string{"sqlite", "bu"}},
		{`"a phrase`, true, `"a phrase`, nil},
		{"one OR", true, "one OR", nil},
		{"one OR two", false, "one OR two", nil},
		{"ENOSPC:", true, "ENOSPC:", nil},
		{"セッション", true, "セッション*", []string{"セッション*"}},
	}
	for _, c := range cases {
		got := readQuery(c.text, c.prefix)
		if got.fts != c.fts || strings.Join(got.terms, ",") != strings.Join(c.terms, ",") {
			t.Errorf("readQuery(%q, %v) = %q %v, want %q %v", c.text, c.prefix, got.fts, got.terms, c.fts, c.terms)
		}
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeConversation(t, root, "cl", "ses-a", time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC), "sqlite busy", "ok")
	backfillSessionTurns(indexedProfiles(), nil)
	if got := search(t, "sqlite bu", func(q *searchQuery) { q.prefix = true }); len(got.hits) != 1 {
		t.Fatalf("prefix search = %v", hitIDs(got))
	}
	if got := search(t, "sqlite bu"); len(got.hits) != 0 {
		t.Fatalf("exact search = %v, want nothing", hitIDs(got))
	}
}

func TestAnEmptyResultCountsEachWord(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	at := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	writeConversation(t, root, "cl", "ses-a", at, "sqlite is fine", "ok")
	writeConversation(t, root, "cl2", "ses-b", at, "sqlite again", "ok")
	backfillSessionTurns(indexedProfiles(), nil)
	query := searchQuery{text: "sqlite busyy", profiles: indexedProfiles()}
	result := searchSessions(query)
	if len(result.hits) != 0 {
		t.Fatalf("hits = %v", hitIDs(result))
	}
	counts := countTerms(query, result.reading)
	want := []termCount{{"sqlite", 2}, {"busyy", 0}, {"both", 0}}
	if fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
	if got := result.reading.describe(); got != "sqlite AND busyy" {
		t.Fatalf("describe = %q", got)
	}
}

func TestAHitsContextIsTheTurnsAroundItAndHowItEnded(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeConversation(t, root, "cl", "ses-a", time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC),
		"open", "the store is locked", "so resume fails with SQLITE_BUSY?", "yes, add a timeout", "ship it", "done")
	backfillSessionTurns(indexedProfiles(), nil)
	result := search(t, "sqlite busy")
	if len(result.hits) != 1 || result.hits[0].best != 2 {
		t.Fatalf("hits = %+v", result.hits)
	}
	context := readHitContext("cl", "ses-a", 2, result.reading.fts)
	if context.problem != "" || context.total != 6 || len(context.turns) != 3 {
		t.Fatalf("context = %+v", context)
	}
	if !context.turns[1].match || context.turns[1].fromUser != true || context.turns[0].match {
		t.Fatalf("turns = %+v, want the match in the middle", context.turns)
	}
	spans := parseMarked(context.turns[1].text)
	var marked []string
	for _, span := range spans {
		if span.match {
			marked = append(marked, span.text)
		}
	}
	if strings.Join(marked, ",") != "SQLITE,BUSY" {
		t.Fatalf("marked %v in %q, want SQLITE and BUSY with the underscore plain", marked, context.turns[1].text)
	}
	if !context.earlier || !context.later {
		t.Fatalf("earlier %v later %v, want both", context.earlier, context.later)
	}
	if len(context.ending) != 2 || context.ending[0].text != "ship it" || context.ending[1].text != "done" {
		t.Fatalf("ending = %+v", context.ending)
	}
	if first := readHitContext("cl", "ses-a", 0, ""); first.earlier || len(first.turns) != 2 {
		t.Fatalf("context at the first turn = %+v", first)
	}
}

func TestDeletingATranscriptTakesItsTurnsOutOfTheSearch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := writeConversation(t, root, "cl", "ses-a", time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC), "kubernetes", "ok")
	backfillSessionTurns(indexedProfiles(), nil)
	if got := search(t, "kubernetes"); len(got.hits) != 1 {
		t.Fatalf("before = %v", hitIDs(got))
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	backfillSessionTurns(indexedProfiles(), nil)
	if got := search(t, "kubernetes"); len(got.hits) != 0 {
		t.Fatalf("after deleting = %v", hitIDs(got))
	}
	db, err := openSessionIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var postings int
	db.QueryRow(`SELECT COUNT(*) FROM turn_fts WHERE turn_fts MATCH 'kubernetes'`).Scan(&postings)
	if postings != 0 {
		t.Fatalf("turn_fts still holds %d postings for a deleted conversation", postings)
	}
}

func TestParseMarkedSplitsMatchedRuns(t *testing.T) {
	got := parseMarked("…so " + matchOpen + "SQLITE" + matchClose + "_" + matchOpen + "BUSY" + matchClose + " only")
	want := []markedSpan{{"…so ", false}, {"SQLITE", true}, {"_", false}, {"BUSY", true}, {" only", false}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parseMarked = %v, want %v", got, want)
	}
	if unmarked("a"+matchOpen+"b"+matchClose) != "ab" {
		t.Fatal("unmarked kept a marker")
	}
}
