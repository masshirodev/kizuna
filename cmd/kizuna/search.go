package main

import (
	"database/sql"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Searching what was said (doc/session-search.md). Every conversation's spoken
// turns are read into the session index's turn table by a backfill, and an FTS5
// index over that table answers a query with the conversations that said it,
// ranked. The text is what the preview and the handoff brief already reduce a
// conversation to, so tool calls and tool output are never searched.

// turnSearchSchema is the full-text index over turn. It is an external-content
// table: the text is stored once, in turn, and the triggers keep the postings in
// step with every insert, rewrite and delete the cache makes.
//
// unicode61 treats an underscore as a separator, so SQLITE_BUSY is two tokens and
// a search for either word finds it. A run of kana and kanji is one token, so a
// word inside a Japanese sentence is not found on its own; a trigram index is
// the remedy if that matters in practice.
const turnSearchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS turn_fts USING fts5(
	text,
	content = 'turn',
	content_rowid = 'n',
	tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS turn_fts_insert AFTER INSERT ON turn BEGIN
	INSERT INTO turn_fts (rowid, text) VALUES (new.n, new.text);
END;
CREATE TRIGGER IF NOT EXISTS turn_fts_delete AFTER DELETE ON turn BEGIN
	INSERT INTO turn_fts (turn_fts, rowid, text) VALUES ('delete', old.n, old.text);
END;
CREATE TRIGGER IF NOT EXISTS turn_fts_update AFTER UPDATE ON turn BEGIN
	INSERT INTO turn_fts (turn_fts, rowid, text) VALUES ('delete', old.n, old.text);
	INSERT INTO turn_fts (rowid, text) VALUES (new.n, new.text);
END;
`

// Snippets and highlights come back from SQLite with the matched tokens between
// these two bytes, which no conversation contains.
const (
	matchOpen  = "\x01"
	matchClose = "\x02"
)

// searchableProvider reports whether the index holds a provider's
// conversations at all.
func searchableProvider(provider string) bool {
	switch provider {
	case "claude", "codex", "opencode", "antigravity":
		return true
	}
	return false
}

// ---- the backfill -----------------------------------------------------------

// backfillBatch is how many conversations are read before their turns are
// written in one transaction. A picker reading the index waits for one batch at
// most, never for the whole backfill.
const backfillBatch = 24

// indexCoverage is how much of the index search can see: conversations whose
// turns are read, out of every conversation indexed, and the sources that could
// not be read this time.
type indexCoverage struct {
	searched int
	total    int
	failed   []backfillFailure
	// known is false until the counts were read at all.
	known bool
}

func (c indexCoverage) complete() bool {
	return c.known && c.searched >= c.total
}

// backfillFailure is one profile whose conversations could not all be read.
// store says the provider keeps them in one store (OpenCode, Antigravity), so
// the store itself is what failed rather than a file per conversation.
type backfillFailure struct {
	profile string
	unread  int
	store   bool
}

func (f backfillFailure) describe() string {
	if f.store {
		return fmt.Sprintf("%s: its store could not be read, %d unread", f.profile, f.unread)
	}
	return fmt.Sprintf("%s: %s could not be read", f.profile, plural(f.unread, "conversation"))
}

type pendingTurns struct {
	profile     Profile
	id          string
	source      string
	fingerprint string
}

// backfillSessionTurns reads the turns of every indexed conversation whose
// turns are missing or older than its source, newest first, for the given
// profiles. Each profile's rows are refreshed first, so a conversation started
// since the last refresh is read too. report, when given, is called after each
// batch with the coverage so far.
//
// The read happens outside the index lock and the write inside it, a batch at a
// time; a source that cannot be read is skipped and named, never taken as a
// conversation with nothing in it.
func backfillSessionTurns(profiles []Profile, report func(indexCoverage)) indexCoverage {
	return backfillSessionTurnsUntil(profiles, report, time.Time{})
}

// backfillSessionTurnsUntil is backfillSessionTurns that stops between batches
// once the deadline passes (none when it is zero), for a caller that has to
// answer in time and will say how much it searched.
func backfillSessionTurnsUntil(profiles []Profile, report func(indexCoverage), deadline time.Time) indexCoverage {
	var searchable []Profile
	for _, profile := range profiles {
		if searchableProvider(profile.Provider) {
			searchable = append(searchable, profile)
		}
	}
	failures := map[string]*backfillFailure{}
	fail := func(profile Profile) {
		failure := failures[profile.Name]
		if failure == nil {
			failure = &backfillFailure{profile: profile.Name, store: profile.Provider == "opencode" || profile.Provider == "antigravity"}
			failures[profile.Name] = failure
		}
		failure.unread++
	}

	pending, coverage, ok := pendingBackfill(searchable)
	if !ok {
		return indexCoverage{}
	}
	snapshot := func() indexCoverage {
		out := coverage
		names := make([]string, 0, len(failures))
		for name := range failures {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out.failed = append(out.failed, *failures[name])
		}
		return out
	}
	if report != nil {
		report(snapshot())
	}
	for start := 0; start < len(pending); start += backfillBatch {
		if !deadline.IsZero() && time.Now().After(deadline) {
			break
		}
		batch := pending[start:min(start+backfillBatch, len(pending))]
		read := make([][]handoffMessage, len(batch))
		failed := make([]bool, len(batch))
		readConcurrently(len(batch), func(index int) {
			messages, err := readPendingTurns(batch[index])
			if err != nil {
				failed[index] = true
				return
			}
			if messages == nil {
				messages = []handoffMessage{}
			}
			read[index] = messages
		})
		for index, item := range batch {
			if failed[index] {
				fail(item.profile)
			}
		}
		written := writeBackfillBatch(batch, read)
		coverage.searched += written
		if report != nil {
			report(snapshot())
		}
	}
	return snapshot()
}

// readConcurrently runs read for every index of a batch on a few goroutines.
// Parsing a transcript is the backfill's whole cost (the first one on the
// workstation read 2.7 GB of them), and it is CPU-bound, so it spreads well;
// it is capped so a backfill beside a build does not take the whole machine.
func readConcurrently(n int, read func(int)) {
	workers := min(max(runtime.NumCPU()/2, 1), 6, n)
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range next {
				read(index)
			}
		}()
	}
	for index := range n {
		next <- index
	}
	close(next)
	wg.Wait()
}

// pendingBackfill refreshes the profiles' rows and lists what the backfill
// still has to read, newest first, with the coverage before it starts.
func pendingBackfill(profiles []Profile) ([]pendingTurns, indexCoverage, bool) {
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return nil, indexCoverage{}, false
	}
	defer db.Close()
	byName := map[string]Profile{}
	names := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		// A profile whose rows cannot be refreshed keeps the rows it had; the
		// backfill still reads those, and a source that has gone fails then.
		_ = refreshSessionIndex(db, profile)
		byName[profile.Name] = profile
		names = append(names, profile.Name)
	}
	if len(names) == 0 {
		return nil, indexCoverage{known: true}, true
	}
	in := placeholders(len(names))
	var coverage indexCoverage
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(t.fingerprint = s.fingerprint), 0) FROM session s
		LEFT JOIN turn_source t ON t.profile = s.profile AND t.id = s.id
		WHERE s.profile IN (`+in+`)`, names...).Scan(&coverage.total, &coverage.searched); err != nil {
		return nil, indexCoverage{}, false
	}
	coverage.known = true
	rows, err := db.Query(`SELECT s.profile, s.id, s.source, s.fingerprint FROM session s
		LEFT JOIN turn_source t ON t.profile = s.profile AND t.id = s.id
		WHERE s.profile IN (`+in+`) AND (t.fingerprint IS NULL OR t.fingerprint != s.fingerprint)
		ORDER BY s.updated DESC`, names...)
	if err != nil {
		return nil, indexCoverage{}, false
	}
	defer rows.Close()
	var pending []pendingTurns
	for rows.Next() {
		var item pendingTurns
		var name string
		if rows.Scan(&name, &item.id, &item.source, &item.fingerprint) != nil {
			continue
		}
		item.profile = byName[name]
		pending = append(pending, item)
	}
	return pending, coverage, rows.Err() == nil
}

// readPendingTurns reads one conversation whole from the source the index
// recorded for it. It goes by that path rather than by readSessionMessages,
// which looks a Claude transcript up among all of them: right for one keypress,
// quadratic for a backfill.
func readPendingTurns(item pendingTurns) ([]handoffMessage, error) {
	switch item.profile.Provider {
	case "claude", "codex":
		return readAllMessages(item.source, item.profile.Provider)
	case "opencode":
		return readOpenCodeMessages(item.source, item.id)
	case "antigravity":
		return readAntigravityMessages(item.profile, item.id)
	}
	return nil, fmt.Errorf("provider %q is not indexed", item.profile.Provider)
}

// writeBackfillBatch stores the turns read for one batch, each against the
// fingerprint its source had when it was listed. A conversation whose row has
// gone since (its transcript was deleted) is skipped. It returns how many were
// written.
func writeBackfillBatch(batch []pendingTurns, read [][]handoffMessage) int {
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return 0
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0
	}
	defer tx.Rollback() //nolint:errcheck
	written := 0
	for index, item := range batch {
		messages := read[index]
		if messages == nil {
			continue
		}
		var exists int
		if tx.QueryRow(`SELECT 1 FROM session WHERE profile = ? AND id = ?`, item.profile.Name, item.id).Scan(&exists) != nil {
			continue
		}
		if err := storeTurns(tx, item.profile.Name, item.id, item.fingerprint, messages); err != nil {
			return 0
		}
		written++
	}
	if tx.Commit() != nil {
		return 0
	}
	return written
}

// storeTurns replaces a conversation's cached turns.
func storeTurns(tx *sql.Tx, profile, id, fingerprint string, messages []handoffMessage) error {
	if _, err := tx.Exec(`DELETE FROM turn WHERE profile = ? AND id = ?`, profile, id); err != nil {
		return err
	}
	for seq, message := range messages {
		fromUser := 0
		if message.fromUser {
			fromUser = 1
		}
		if _, err := tx.Exec(`INSERT INTO turn (profile, id, seq, from_user, text) VALUES (?, ?, ?, ?, ?)`,
			profile, id, seq, fromUser, message.text); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT INTO turn_source (profile, id, fingerprint) VALUES (?, ?, ?)
		ON CONFLICT (profile, id) DO UPDATE SET fingerprint = excluded.fingerprint`, profile, id, fingerprint)
	return err
}

// readIndexCoverage is the coverage as the index stands, without refreshing or
// reading anything: what the palette's note and the CLI's progress line say
// before a backfill reports.
func readIndexCoverage(profiles []Profile) indexCoverage {
	names := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		if searchableProvider(profile.Provider) {
			names = append(names, profile.Name)
		}
	}
	if len(names) == 0 {
		return indexCoverage{known: true}
	}
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return indexCoverage{}
	}
	defer db.Close()
	var coverage indexCoverage
	if db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(t.fingerprint = s.fingerprint), 0) FROM session s
		LEFT JOIN turn_source t ON t.profile = s.profile AND t.id = s.id
		WHERE s.profile IN (`+placeholders(len(names))+`)`, names...).Scan(&coverage.total, &coverage.searched) != nil {
		return indexCoverage{}
	}
	coverage.known = true
	return coverage
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// ---- reading a query --------------------------------------------------------

// searchQuery is one search: the words as typed, and what narrows them.
type searchQuery struct {
	text string
	// prefix matches the last word as a prefix (bu → bu*). The TUI sets it
	// while the field has the keys, so results arrive as a word is typed.
	prefix   bool
	profiles []Profile
	headless bool
	fromUser bool
	since    time.Time
	// folder, when set, keeps only conversations whose folder it accepts
	// (--repo).
	folder func(string) bool
	// bestMatch orders by rank instead of by last activity, newest first,
	// which is the default: a search is usually for something recent.
	bestMatch bool
	limit     int
}

// queryReading is how a query was read: what was sent to FTS5, the plain words
// it was made of when it was only plain words, and whether it had to be
// searched as a phrase because FTS5 could not parse it as typed.
type queryReading struct {
	fts    string
	terms  []string
	phrase bool
}

// describe is the READ AS line: the words ANDed, the query as typed when it
// used FTS5's own syntax, or the phrase it fell back to.
func (r queryReading) describe() string {
	switch {
	case r.phrase:
		return "searched as the phrase " + r.fts
	case len(r.terms) > 0:
		return strings.Join(r.terms, " AND ")
	}
	return r.fts
}

// readQuery turns what was typed into an FTS5 query. Plain words pass through
// as they are, which FTS5 ANDs; quotes, *, OR and NOT are FTS5's own syntax and
// pass through too. With prefix, a last word still being typed is matched as a
// prefix.
func readQuery(text string, prefix bool) queryReading {
	trimmed := strings.TrimSpace(text)
	reading := queryReading{fts: trimmed}
	if plainWords(trimmed) {
		reading.terms = strings.Fields(trimmed)
	}
	if prefix && trimmed != "" && !strings.HasSuffix(text, " ") && endsInBareword(trimmed) {
		reading.fts = trimmed + "*"
		if len(reading.terms) > 0 {
			reading.terms[len(reading.terms)-1] += "*"
		}
	}
	return reading
}

// asPhrase is the fallback for a query FTS5 refuses to parse: the whole text,
// quoted, so pasting `ENOSPC: no space left` searches for exactly that.
func (r queryReading) asPhrase(text string) queryReading {
	quoted := `"` + strings.ReplaceAll(strings.TrimSpace(text), `"`, `""`) + `"`
	return queryReading{fts: quoted, phrase: true}
}

// plainWords reports whether a query is only FTS5 barewords that are not
// operators: the case the READ AS line can spell out word by word.
func plainWords(text string) bool {
	words := strings.Fields(text)
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		if word == "AND" || word == "OR" || word == "NOT" || word == "NEAR" || !isBareword(word) {
			return false
		}
	}
	return true
}

// isBareword is FTS5's bareword: ASCII letters and digits, the underscore, and
// anything outside ASCII.
func isBareword(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r > unicode.MaxASCII || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		return false
	}
	return true
}

func endsInBareword(text string) bool {
	if strings.Count(text, `"`)%2 == 1 {
		return false
	}
	words := strings.Fields(text)
	last := words[len(words)-1]
	return isBareword(last) && last != "AND" && last != "OR" && last != "NOT" && last != "NEAR"
}

// ---- searching --------------------------------------------------------------

// searchHit is one conversation that matched: its row, how many of its turns
// matched and which, and its best turn's snippet.
type searchHit struct {
	record   recordedSession
	provider string
	matches  int
	// seqs are the matching turns in conversation order; best is the one the
	// snippet came from, the best ranked.
	seqs         []int
	best         int
	bestRow      int64
	bestFromUser bool
	snippet      string
	rank         float64
	turns        int
}

// searchResult is what a search answers: the hits, how the query was read, and
// how much of the index was searchable when it ran.
type searchResult struct {
	query    string
	reading  queryReading
	hits     []searchHit
	coverage indexCoverage
	err      error
}

// searchLimit caps the conversations a search returns. Ranking ten snippets by
// eye is the use; two hundred is already more than anyone reads.
const searchLimit = 200

// searchSessions answers a query from the index. It never refreshes or reads a
// source, so it is fast enough to run on every keystroke; what it can see is
// whatever the backfill has read so far, and the coverage says how much that is.
func searchSessions(query searchQuery) searchResult {
	result := searchResult{query: query.text, reading: readQuery(query.text, query.prefix)}
	if result.reading.fts == "" {
		return result
	}
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		result.err = err
		return result
	}
	defer db.Close()
	names := searchProfileNames(query.profiles)
	result.coverage = coverageIn(db, names)
	hits, err := matchTurns(db, query, result.reading.fts, names)
	if err != nil {
		// Anything FTS5 cannot parse is searched as the phrase it spells.
		result.reading = result.reading.asPhrase(query.text)
		if query.prefix && !strings.HasSuffix(query.text, " ") {
			result.reading.fts += "*"
		}
		if hits, err = matchTurns(db, query, result.reading.fts, names); err != nil {
			result.err = err
			return result
		}
	}
	for index := range hits {
		_ = db.QueryRow(`SELECT snippet(turn_fts, 0, ?, ?, '…', 16) FROM turn_fts WHERE turn_fts MATCH ? AND rowid = ?`,
			matchOpen, matchClose, result.reading.fts, hits[index].bestRow).Scan(&hits[index].snippet)
		_ = db.QueryRow(`SELECT COUNT(*) FROM turn WHERE profile = ? AND id = ?`,
			hits[index].record.profile, hits[index].record.session.id).Scan(&hits[index].turns)
	}
	result.hits = hits
	return result
}

func searchProfileNames(profiles []Profile) []any {
	names := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		if searchableProvider(profile.Provider) {
			names = append(names, profile.Name)
		}
	}
	return names
}

func coverageIn(db *sql.DB, names []any) indexCoverage {
	if len(names) == 0 {
		return indexCoverage{known: true}
	}
	var coverage indexCoverage
	if db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(t.fingerprint = s.fingerprint), 0) FROM session s
		LEFT JOIN turn_source t ON t.profile = s.profile AND t.id = s.id
		WHERE s.profile IN (`+placeholders(len(names))+`)`, names...).Scan(&coverage.total, &coverage.searched) != nil {
		return indexCoverage{}
	}
	coverage.known = true
	return coverage
}

// matchTurns runs the MATCH and groups the matching turns by conversation,
// ranked, limited. A stale conversation's turns are searched as they were last
// read: what it said then it still said.
func matchTurns(db *sql.DB, query searchQuery, fts string, names []any) ([]searchHit, error) {
	if len(names) == 0 {
		return nil, nil
	}
	where := []string{`turn_fts MATCH ?`, `t.profile IN (` + placeholders(len(names)) + `)`}
	args := append([]any{fts}, names...)
	if !query.headless {
		where = append(where, `s.headless = 0`)
	}
	if query.fromUser {
		where = append(where, `t.from_user = 1`)
	}
	if !query.since.IsZero() {
		where = append(where, `s.updated >= ?`)
		args = append(args, query.since.UnixNano())
	}
	rows, err := db.Query(`SELECT t.n, t.profile, t.id, t.seq, t.from_user, bm25(turn_fts),
			s.provider, s.title, s.folder, s.created, s.updated, s.headless
		FROM turn_fts JOIN turn t ON t.n = turn_fts.rowid
		JOIN session s ON s.profile = t.profile AND s.id = t.id
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]*searchHit{}
	var order []string
	for rows.Next() {
		var (
			row              int64
			profile, id      string
			seq, fromUser    int
			rank             float64
			provider, title  string
			folder           string
			created, updated int64
			headless         int
		)
		if err := rows.Scan(&row, &profile, &id, &seq, &fromUser, &rank, &provider, &title, &folder, &created, &updated, &headless); err != nil {
			return nil, err
		}
		if query.folder != nil && !query.folder(folder) {
			continue
		}
		key := profile + "\x00" + id
		hit := byKey[key]
		if hit == nil {
			hit = &searchHit{provider: provider, rank: rank, best: seq, bestRow: row, bestFromUser: fromUser == 1}
			hit.record = recordedSession{
				session:    instanceSession{id: id, title: title},
				folder:     folder,
				when:       indexTime(created),
				lastActive: indexTime(updated),
				profile:    profile,
				headless:   headless == 1,
			}
			byKey[key] = hit
			order = append(order, key)
		} else if rank < hit.rank {
			hit.rank, hit.best, hit.bestRow, hit.bestFromUser = rank, seq, row, fromUser == 1
		}
		hit.matches++
		hit.seqs = append(hit.seqs, seq)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hits := make([]searchHit, 0, len(order))
	for _, key := range order {
		hit := byKey[key]
		sort.Ints(hit.seqs)
		hits = append(hits, *hit)
	}
	sortHits(hits, query.bestMatch)
	limit := query.limit
	if limit <= 0 {
		limit = searchLimit
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// sortHits orders hits newest first by last activity; or, by best match, by
// their best turn's rank (bm25 is lower for a better match), newest first among
// equals.
func sortHits(hits []searchHit, bestMatch bool) {
	sort.SliceStable(hits, func(i, j int) bool {
		if bestMatch && hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		return hits[i].record.activity().After(hits[j].record.activity())
	})
}

// ---- an empty result --------------------------------------------------------

// termCount is how many conversations hold one word of the query.
type termCount struct {
	term          string
	conversations int
}

// countTerms is HOW THE QUERY WAS READ, for a query that found nothing: how many
// conversations hold each word, and how many hold them all. When one word is in
// none of them, that usually explains the empty list.
func countTerms(query searchQuery, reading queryReading) []termCount {
	if len(reading.terms) == 0 {
		return nil
	}
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		return nil
	}
	defer db.Close()
	names := searchProfileNames(query.profiles)
	if len(names) == 0 {
		return nil
	}
	count := func(fts string) int {
		args := append([]any{fts}, names...)
		headless := ""
		if !query.headless {
			headless = ` AND s.headless = 0`
		}
		var n int
		if db.QueryRow(`SELECT COUNT(DISTINCT t.profile || char(0) || t.id) FROM turn_fts
			JOIN turn t ON t.n = turn_fts.rowid JOIN session s ON s.profile = t.profile AND s.id = t.id
			WHERE turn_fts MATCH ? AND t.profile IN (`+placeholders(len(names))+`)`+headless, args...).Scan(&n) != nil {
			return 0
		}
		return n
	}
	counts := make([]termCount, 0, len(reading.terms)+1)
	for _, term := range reading.terms {
		counts = append(counts, termCount{term: term, conversations: count(term)})
	}
	if len(reading.terms) > 1 {
		label := "both"
		if len(reading.terms) > 2 {
			label = "all"
		}
		counts = append(counts, termCount{term: label, conversations: count(strings.Join(reading.terms, " "))})
	}
	return counts
}

// ---- a hit in context -------------------------------------------------------

// contextTurn is one turn of the pane's context: who said it, where it sits,
// and its text, with the matched words marked when it is the matching turn.
type contextTurn struct {
	seq      int
	fromUser bool
	text     string
	match    bool
}

// hitContext is what the pane shows for one match: the matching turn with the
// turn before and after it, where it sits among the conversation's turns, and
// how the conversation ended.
type hitContext struct {
	profile string
	session string
	seq     int
	total   int
	turns   []contextTurn
	earlier bool
	later   bool
	ending  []contextTurn
	problem string
}

// readHitContext reads the context around one matching turn from the index.
func readHitContext(profile, id string, seq int, fts string) hitContext {
	context := hitContext{profile: profile, session: id, seq: seq}
	sessionIndexMu.Lock()
	defer sessionIndexMu.Unlock()
	db, err := openSessionIndex()
	if err != nil {
		context.problem = err.Error()
		return context
	}
	defer db.Close()
	if db.QueryRow(`SELECT COUNT(*) FROM turn WHERE profile = ? AND id = ?`, profile, id).Scan(&context.total) != nil || context.total == 0 {
		context.problem = "this conversation's turns are not in the index any more"
		return context
	}
	rows, err := db.Query(`SELECT n, seq, from_user, text FROM turn WHERE profile = ? AND id = ? AND seq BETWEEN ? AND ? ORDER BY seq`,
		profile, id, seq-1, seq+1)
	if err != nil {
		context.problem = err.Error()
		return context
	}
	var matchRow int64 = -1
	for rows.Next() {
		var turn contextTurn
		var row int64
		var fromUser int
		if rows.Scan(&row, &turn.seq, &fromUser, &turn.text) != nil {
			continue
		}
		turn.fromUser = fromUser == 1
		if turn.seq == seq {
			turn.match, matchRow = true, row
		}
		context.turns = append(context.turns, turn)
	}
	rows.Close()
	if matchRow >= 0 && fts != "" {
		var marked string
		if db.QueryRow(`SELECT highlight(turn_fts, 0, ?, ?) FROM turn_fts WHERE turn_fts MATCH ? AND rowid = ?`,
			matchOpen, matchClose, fts, matchRow).Scan(&marked) == nil {
			for index := range context.turns {
				if context.turns[index].match {
					context.turns[index].text = marked
				}
			}
		}
	}
	context.earlier = seq-1 > 0
	context.later = seq+1 < context.total-1
	ending, err := db.Query(`SELECT seq, from_user, text FROM turn WHERE profile = ? AND id = ? ORDER BY seq DESC LIMIT 2`, profile, id)
	if err == nil {
		for ending.Next() {
			var turn contextTurn
			var fromUser int
			if ending.Scan(&turn.seq, &fromUser, &turn.text) == nil {
				turn.fromUser = fromUser == 1
				context.ending = append([]contextTurn{turn}, context.ending...)
			}
		}
		ending.Close()
	}
	return context
}

// ---- marked text ------------------------------------------------------------

// markedSpan is a run of text that either is or is not a matched token.
type markedSpan struct {
	text  string
	match bool
}

// parseMarked splits a snippet or highlight into its matched and plain runs.
func parseMarked(text string) []markedSpan {
	var spans []markedSpan
	match := false
	for text != "" {
		marker := matchOpen
		if match {
			marker = matchClose
		}
		cut := strings.Index(text, marker)
		if cut < 0 {
			spans = append(spans, markedSpan{text: text, match: match})
			break
		}
		if cut > 0 {
			spans = append(spans, markedSpan{text: text[:cut], match: match})
		}
		text = text[cut+len(marker):]
		match = !match
	}
	return spans
}

// unmarked is the text with the match markers taken out.
func unmarked(text string) string {
	return strings.NewReplacer(matchOpen, "", matchClose, "").Replace(text)
}
