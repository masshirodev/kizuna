package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The resume picker's search of what was said (doc/session-search.md, and the
// handoff in doc/handoffs/Session search.dc.html). One box, two searches:
// ctrl+f flips the picker between the / filter it has always had (titles,
// folders, accounts, ids) and a ranked search of what was said in every
// conversation. The two are told apart three ways at once: the mode pair in the
// heading, the field (a small / line for titles, a full-width tinted field for
// what was said), and an empty state that names what it searched.

type searchFocus int

const (
	// searchOnField is the field holding the keys: typing searches.
	searchOnField searchFocus = iota
	// searchOnResults hands the keys to the list: letters are actions.
	searchOnResults
)

// searchState is the search mode while a picker is open. coverage and
// backfilling outlive the picker: the backfill runs to its end whether or not
// anybody is still looking.
type searchState struct {
	on    bool
	query string
	tail  int
	focus searchFocus
	// newest orders the hits newest first (o) instead of best match first.
	newest bool
	// seq counts the queries asked; landed is the one the result on screen
	// answered. While they differ the result is stale: dimmed, unmarked, and
	// without a cursor, so nothing old reads as the answer.
	seq    int
	landed int
	prefix bool
	result searchResult
	counts []termCount
	cursor int
	// at is which of the hit's matches the pane shows, as an index into its
	// seqs (conversation order).
	at       int
	contexts map[string]hitContext

	coverage    indexCoverage
	backfilling bool
}

func (s searchState) stale() bool {
	return strings.TrimSpace(s.query) != "" && s.landed != s.seq
}

// searchDebounce is how long typing has to pause before the query runs. A
// search is a few milliseconds, but one per keystroke still redraws the list
// under every letter of a word.
const searchDebounce = 120 * time.Millisecond

type searchTickMsg struct{ seq int }

type searchResultMsg struct {
	seq    int
	result searchResult
	counts []termCount
}

type hitContextMsg struct {
	key     string
	context hitContext
}

// backfillMsg is one report from the backfill. updates is where the next one
// will come from; done says there will be none.
type backfillMsg struct {
	coverage indexCoverage
	done     bool
	updates  <-chan backfillMsg
}

// ---- opening and closing ----------------------------------------------------

// openSearchFromBoard opens the resume box in search mode over every account,
// which is what the palette's "search what was said" does.
func (m *tuiModel) openSearchFromBoard() tea.Cmd {
	m.mode = tuiRecent
	m.pickerAll = true
	m.record = 0
	m.previews = nil
	m.recentFilter, m.recentSearching = "", false
	m.clearStatus()
	m.search = searchState{coverage: m.search.coverage, backfilling: m.search.backfilling}
	return m.enterSearch()
}

// enterSearch switches the open picker to searching what was said. The query
// survives a trip to the titles filter and back.
func (m *tuiModel) enterSearch() tea.Cmd {
	m.search.on = true
	m.search.focus = searchOnField
	m.recentSearching = false
	m.clearStatus()
	cmds := []tea.Cmd{}
	if !m.search.backfilling {
		m.search.backfilling = true
		cmds = append(cmds, startBackfillCmd(m.profiles))
	}
	if !m.search.coverage.known {
		cmds = append(cmds, readCoverageCmd(m.profiles))
	}
	if strings.TrimSpace(m.search.query) != "" {
		cmds = append(cmds, m.requery(true))
	}
	return tea.Batch(cmds...)
}

// leaveSearch goes back to the titles filter.
func (m *tuiModel) leaveSearch() tea.Cmd {
	m.search.on = false
	m.clearStatus()
	if m.pickerAll && !m.recentAllLoaded {
		m.setStatus(statusNone, "reading every profile…")
		return loadRecentAllCmd(m.profiles)
	}
	m.clampRecord()
	return m.selectPreview()
}

type coverageReadMsg struct{ coverage indexCoverage }

func readCoverageCmd(profiles []Profile) tea.Cmd {
	profiles = append([]Profile(nil), profiles...)
	return func() tea.Msg { return coverageReadMsg{coverage: readIndexCoverage(profiles)} }
}

// startBackfillCmd reads every conversation into the index in the background
// and reports after each batch. The reports arrive one message at a time, each
// carrying the channel the next one comes from.
func startBackfillCmd(profiles []Profile) tea.Cmd {
	profiles = append([]Profile(nil), profiles...)
	updates := make(chan backfillMsg, 1)
	go func() {
		final := backfillSessionTurns(profiles, func(coverage indexCoverage) {
			// Only the latest report matters; one the TUI has not taken yet
			// is replaced rather than queued.
			select {
			case <-updates:
			default:
			}
			updates <- backfillMsg{coverage: coverage, updates: updates}
		})
		select {
		case <-updates:
		default:
		}
		updates <- backfillMsg{coverage: final, done: true}
		close(updates)
	}()
	return waitBackfillCmd(updates)
}

func waitBackfillCmd(updates <-chan backfillMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-updates
		if !ok {
			return nil
		}
		return msg
	}
}

// ---- asking -----------------------------------------------------------------

// searchScope is the accounts a search covers: every one, or the selected one.
func (m tuiModel) searchScope() []Profile {
	if m.pickerAll {
		return m.profiles
	}
	if profile, ok := m.selectedProfile(); ok {
		return []Profile{profile}
	}
	return nil
}

func (m tuiModel) searchQueryFor(prefix bool) searchQuery {
	// Headless conversations are always asked for and hidden here, so the
	// count line can say how many it is hiding.
	return searchQuery{text: m.search.query, prefix: prefix, profiles: m.searchScope(), headless: true, newest: m.search.newest}
}

// requery starts a new query generation after the debounce. prefix matches the
// word being typed as a prefix.
func (m *tuiModel) requery(prefix bool) tea.Cmd {
	m.search.seq++
	m.search.prefix = prefix
	if strings.TrimSpace(m.search.query) == "" {
		m.search.result, m.search.counts, m.search.landed = searchResult{}, nil, m.search.seq
		return nil
	}
	seq := m.search.seq
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg { return searchTickMsg{seq: seq} })
}

// refreshSearch asks the same query again, in the same generation, so the
// backfill's progress fills the list in without dimming it.
func (m tuiModel) refreshSearch() tea.Cmd {
	if !m.search.on || strings.TrimSpace(m.search.query) == "" {
		return nil
	}
	return searchCmd(m.search.seq, m.searchQueryFor(m.search.prefix), m.showHeadless)
}

func searchCmd(seq int, query searchQuery, showHeadless bool) tea.Cmd {
	return func() tea.Msg {
		result := searchSessions(query)
		var counts []termCount
		if visibleHitCount(result.hits, showHeadless) == 0 && result.err == nil {
			counted := query
			counted.headless = showHeadless
			counts = countTerms(counted, result.reading)
		}
		return searchResultMsg{seq: seq, result: result, counts: counts}
	}
}

func visibleHitCount(hits []searchHit, showHeadless bool) int {
	count := 0
	for _, hit := range hits {
		if showHeadless || !hit.record.headless {
			count++
		}
	}
	return count
}

// visibleHits is the result after headless hits are hidden.
func (m tuiModel) visibleHits() []searchHit {
	if m.showHeadless {
		return m.search.result.hits
	}
	kept := make([]searchHit, 0, len(m.search.result.hits))
	for _, hit := range m.search.result.hits {
		if !hit.record.headless {
			kept = append(kept, hit)
		}
	}
	return kept
}

func (m tuiModel) hiddenHeadlessHits() int {
	if m.showHeadless {
		return 0
	}
	return len(m.search.result.hits) - len(m.visibleHits())
}

func (m tuiModel) selectedHit() (searchHit, bool) {
	hits := m.visibleHits()
	if m.search.stale() || m.search.cursor < 0 || m.search.cursor >= len(hits) {
		return searchHit{}, false
	}
	return hits[m.search.cursor], true
}

func hitKey(hit searchHit, seq int) string {
	return hit.record.profile + "\x00" + hit.record.session.id + "\x00" + strconv.Itoa(seq)
}

// bestAt is the position of the hit's best match among its matches, which is
// where the pane opens.
func bestAt(hit searchHit) int {
	for index, seq := range hit.seqs {
		if seq == hit.best {
			return index
		}
	}
	return 0
}

// selectContext points the pane at the selected hit's current match, reading
// it off the keypress unless it has been read already.
func (m *tuiModel) selectContext() tea.Cmd {
	hit, ok := m.selectedHit()
	if !ok || len(hit.seqs) == 0 {
		return nil
	}
	m.search.at = min(max(m.search.at, 0), len(hit.seqs)-1)
	seq := hit.seqs[m.search.at]
	key := hitKey(hit, seq)
	if _, read := m.search.contexts[key]; read {
		return nil
	}
	fts := m.search.result.reading.fts
	profile, id := hit.record.profile, hit.record.session.id
	return func() tea.Msg {
		return hitContextMsg{key: key, context: readHitContext(profile, id, seq, fts)}
	}
}

// ---- messages ---------------------------------------------------------------

// updateSearchMsg takes the search mode's own messages. handled is false for a
// message that is not one of them.
func (m tuiModel) updateSearchMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case searchTickMsg:
		if msg.seq != m.search.seq || !m.search.on {
			return m, nil, true
		}
		return m, searchCmd(msg.seq, m.searchQueryFor(m.search.prefix), m.showHeadless), true
	case searchResultMsg:
		if msg.seq != m.search.seq {
			return m, nil, true
		}
		previous, _ := m.selectedHit()
		sameQuery := m.search.landed == msg.seq
		m.search.result, m.search.counts, m.search.landed = msg.result, msg.counts, msg.seq
		m.search.cursor, m.search.at = 0, 0
		hits := m.visibleHits()
		// A refresh of the same query keeps the cursor on the conversation
		// it was on; a new query starts from its best hit.
		if sameQuery && previous.record.session.id != "" {
			for index, hit := range hits {
				if hit.record.profile == previous.record.profile && hit.record.session.id == previous.record.session.id {
					m.search.cursor = index
					break
				}
			}
		}
		if hit, ok := m.selectedHit(); ok {
			m.search.at = bestAt(hit)
		}
		return m, m.selectContext(), true
	case hitContextMsg:
		if m.search.contexts == nil {
			m.search.contexts = map[string]hitContext{}
		}
		m.search.contexts[msg.key] = msg.context
		return m, nil, true
	case coverageReadMsg:
		if !m.search.coverage.known || !m.search.backfilling {
			m.search.coverage = msg.coverage
		}
		return m, nil, true
	case backfillMsg:
		m.search.coverage = msg.coverage
		m.search.backfilling = !msg.done
		cmds := []tea.Cmd{m.refreshSearch()}
		if !msg.done {
			cmds = append(cmds, waitBackfillCmd(msg.updates))
		}
		return m, tea.Batch(cmds...), true
	}
	return m, nil, false
}

// ---- keys -------------------------------------------------------------------

// updateSearchKeys is a picker's keys while it searches what was said. Typing
// goes to the field until ↓ or ↵ hands the keys to the results, and / takes
// them back, the way the board's own / works; the letter keys act only once
// the results have the keys. inHandoff is the H picker, where ↵ chooses the
// conversation that is leaving instead of resuming it.
func (m tuiModel) updateSearchKeys(msg tea.KeyMsg, inHandoff bool) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "ctrl+c":
		return m.closeSearchBox()
	case "ctrl+f":
		return m, m.leaveSearch()
	}
	if m.search.focus == searchOnField {
		switch key {
		case "esc":
			return m.closeSearchBox()
		case "down", "enter":
			// The results stay as they are, the last word still a prefix:
			// running it again as typed turned a half-typed word's ten hits
			// into none the moment the keys moved.
			m.search.focus = searchOnResults
			return m, nil
		case "up":
			return m, nil
		}
		value, tail, edited := editLine(m.search.query, m.search.tail, msg)
		if !edited {
			return m, nil
		}
		m.search.query, m.search.tail = value, tail
		return m, m.requery(true)
	}

	hits := m.visibleHits()
	switch key {
	case "esc", "q":
		return m.closeSearchBox()
	case "/":
		m.search.focus = searchOnField
		m.clearStatus()
	case "up", "k":
		if m.search.cursor > 0 && !m.search.stale() {
			m.search.cursor--
			return m.atBestMatch()
		}
	case "down", "j":
		if m.search.cursor < len(hits)-1 && !m.search.stale() {
			m.search.cursor++
			return m.atBestMatch()
		}
	case "n", "N":
		hit, ok := m.selectedHit()
		if !ok || len(hit.seqs) < 2 {
			return m, nil
		}
		step := 1
		if key == "N" {
			step = -1
		}
		m.search.at = (m.search.at + step + len(hit.seqs)) % len(hit.seqs)
		return m, m.selectContext()
	case "a":
		m.pickerAll = !m.pickerAll
		m.clearStatus()
		return m, m.requery(m.search.prefix)
	case ".":
		m.showHeadless = !m.showHeadless
		m.search.cursor = 0
		if hit, ok := m.selectedHit(); ok {
			m.search.at = bestAt(hit)
		}
		return m, tea.Batch(m.requery(m.search.prefix), m.selectContext())
	case "o":
		m.search.newest = !m.search.newest
		return m, m.requery(m.search.prefix)
	case "y":
		hit, ok := m.selectedHit()
		if !ok {
			return m, nil
		}
		command := resumeCommandLine(hit.record.profile, hit.record.session.id)
		m.setStatus(statusOK, "copied  "+command)
		return m, copyTextCmd(command)
	case "H":
		if hit, ok := m.selectedHit(); ok && !inHandoff {
			return m.leaveWith(hit.record)
		}
	case "enter":
		hit, ok := m.selectedHit()
		if !ok {
			return m, nil
		}
		if inHandoff {
			return m.leaveWith(hit.record)
		}
		return m.resumeRecord(hit.record)
	}
	return m, nil
}

// atBestMatch opens the pane on the newly selected hit's best match.
func (m tuiModel) atBestMatch() (tea.Model, tea.Cmd) {
	if hit, ok := m.selectedHit(); ok {
		m.search.at = bestAt(hit)
	}
	return m, m.selectContext()
}

func (m tuiModel) closeSearchBox() (tea.Model, tea.Cmd) {
	m.mode = tuiList
	m.search.on = false
	m.handoff = handoffDraft{}
	m.clearStatus()
	return m, nil
}

// copyTextCmd hands text to the terminal's clipboard (clipboard.go).
func copyTextCmd(text string) tea.Cmd {
	return func() tea.Msg {
		_, _ = fmt.Print(osc52Clipboard(text))
		return nil
	}
}

// ---- drawing: the heading and the field -------------------------------------

// chipPair is a two-way switch drawn as the scope chip is: the current side a
// tinted chip, the other faint beside it, then its key. Narrow, only the
// current chip is drawn.
func chipPair(first, second string, onFirst bool, key string, narrow bool) string {
	on := func(text string) string { return selectedPen(true).render(boxTitleStyle, " "+text+" ") }
	keyHint := helpKeyStyle.Render("  " + key)
	if narrow {
		if onFirst {
			return on(first) + keyHint
		}
		return on(second) + keyHint
	}
	if onFirst {
		return on(first) + sectionLabelStyle.Render("  "+second) + keyHint
	}
	return sectionLabelStyle.Render(first+"  ") + on(second) + keyHint
}

func (m tuiModel) scopeChips(narrow bool) string {
	return chipPair("this account", "all accounts", !m.pickerAll, "a", narrow)
}

func (m tuiModel) modeChips(narrow bool) string {
	return chipPair("titles", "what was said", !m.search.on, "ctrl+f", narrow)
}

// pickerNarrow is the width below which the pane folds away.
func pickerNarrow(width int) bool {
	return width < pickerListMin+dividerWidth+previewPaneMin
}

// resumeHeading is the resume box's first line in either mode: the title and
// the scope on the left, the mode pair on the right where it never moves.
func (m tuiModel) resumeHeading(width int) string {
	narrow := pickerNarrow(width)
	return spread(boxTitle("resume")+"   "+m.scopeChips(narrow), m.modeChips(narrow), width)
}

// searchField is the query field: full width and tinted, only in this mode.
// It says what it searches, so a word that turns up nothing is never blamed on
// the titles filter.
func (m tuiModel) searchField(width int) string {
	ink := pen{background: colorField}
	right := ink.render(sectionLabelStyle, "searches what was said ")
	if m.search.focus != searchOnField {
		right = ink.render(sectionLabelStyle, "searches what was said   ") + ink.render(helpKeyStyle, "/") + ink.render(helpDescStyle, " edit ")
	}
	left := ink.render(helpKeyStyle, " › ")
	room := max(width-lipgloss.Width(left)-lipgloss.Width(right), 4)
	if m.search.focus == searchOnField {
		left += caretView(ink, fieldValueStyle, m.search.query, m.search.tail, room)
	} else {
		left += ink.render(fieldValueStyle, truncate(m.search.query, room))
	}
	return padStyled(ink, left, width-lipgloss.Width(right)) + right
}

// titlesCorner is the titles filter's own line under the heading.
func (m tuiModel) titlesCorner(width int) string {
	if m.recentSearching {
		return truncateStyled(helpKeyStyle.Render("/")+caretView(pen{}, fieldValueStyle, m.recentFilter, m.recentTail, len([]rune(m.recentFilter))+1)+
			sectionLabelStyle.Render("   searches titles, folders, accounts and ids"), width)
	}
	return truncateStyled(m.searchCorner(), width)
}

// ---- drawing: the box -------------------------------------------------------

// searchBox is the resume box in search mode: heading, field, a blank line, the
// body, then the footer. The plain picker gives one row to its / line, and this
// mode gives the field and a blank, so switching never resizes the box.
func (m tuiModel) searchBox(width, rows int) []string {
	lines := []string{m.resumeHeading(width), "", m.searchField(width), ""}
	lines = append(lines, m.searchBody(width, max(rows-2, minBlockRows))...)
	return append(lines, "", m.searchFooter(width, false))
}

// searchBody is the hits and, when there is room, the pane beside them.
func (m tuiModel) searchBody(width, rows int) []string {
	if pickerNarrow(width) {
		return padToRows(m.searchList(width, rows), rows, -1)
	}
	columns := width - dividerWidth
	list := min(max(columns*pickerListShare/100, pickerListMin), columns-previewPaneMin, pickerListMax)
	pane := columns - list
	return joinPanes(m.searchList(list, rows), m.searchPane(pane, rows), list, pane, rows)
}

func (m tuiModel) searchFooter(width int, inHandoff bool) string {
	esc := helpEntry{"esc", "close"}
	if inHandoff {
		esc = helpEntry{"esc", "cancel"}
	}
	if m.search.focus == searchOnField {
		syntax := sectionLabelStyle.Render(`"a phrase"   prefix*   OR   NOT     plain words all have to appear`)
		keys := renderKeys(helpEntry{"↓ ↵", "to the results"})
		right := renderKeys(esc)
		if lipgloss.Width(keys)+3+lipgloss.Width(syntax) <= width-lipgloss.Width(right)-2 {
			keys += keyGap + syntax
		}
		return spread(keys, right, width)
	}
	if len(m.visibleHits()) == 0 && strings.TrimSpace(m.search.query) != "" && !m.search.stale() {
		entries := []helpEntry{{"/", "edit query"}}
		if !m.pickerAll {
			entries = append(entries, helpEntry{"a", "every account"})
		}
		entries = append(entries, helpEntry{"ctrl+f", "search titles instead"})
		return boxFooter(width, esc, entries...)
	}
	choose := "resume in "
	if inHandoff {
		choose = "hand off from "
	}
	where := "it"
	if hit, ok := m.selectedHit(); ok {
		where = shortenHome(hit.record.folder)
	}
	entries := []helpEntry{{"↑↓", "choose"}, {"↵", strings.TrimSpace(choose + where)}}
	if !inHandoff {
		entries = append(entries, helpEntry{"H", "hand off"})
	}
	if !pickerNarrow(width) {
		entries = append(entries, helpEntry{"n N", "next match"})
	}
	entries = append(entries, helpEntry{"y", "copy command"})
	return boxFooter(width, esc, entries...)
}

// ---- drawing: the list ------------------------------------------------------

func (m tuiModel) searchListHeader(width int) string {
	header := "  " + pad("WHEN", pickerWhenWidth)
	if m.pickerAll {
		header += pad("ACCOUNT", pickerAccountWidth)
	}
	header = pad(header+"CONVERSATION", width-searchMatchesWidth) + "MATCHES"
	return sectionLabelStyle.Render(truncate(header, width))
}

// searchMatchesWidth is the MATCHES column, which takes FOLDER's place: the
// folder is still in the pane's header and in the footer's ↵.
const searchMatchesWidth = 7

// searchList is the column heading, two lines per hit windowed around the
// cursor, and the count lines under them.
func (m tuiModel) searchList(width, rows int) []string {
	lines := []string{m.searchListHeader(width), ""}
	if strings.TrimSpace(m.search.query) == "" {
		lines = append(lines, emptyStateStyle.Render("Type what was said; every word has to appear"))
		return append(lines, "", "")
	}
	hits := m.visibleHits()
	if len(hits) == 0 && !m.search.stale() && m.search.landed == m.search.seq {
		return append(lines, m.searchEmpty(width)...)
	}
	tail := m.searchTail(width)
	room := max(rows-3-len(tail), 2)
	room -= room % 2
	stale := m.search.stale()
	var body []string
	for index, hit := range hits {
		body = append(body, m.hitRows(hit, width, index == m.search.cursor && !stale, stale)...)
	}
	lines = append(lines, windowPairs(body, m.search.cursor, room)...)
	return append(append(lines, ""), tail...)
}

// windowPairs scrolls two-line rows the way windowRows scrolls one-line ones.
func windowPairs(lines []string, cursor, height int) []string {
	if height < 2 || len(lines) <= height {
		return lines
	}
	pairs := height / 2
	total := len(lines) / 2
	start := min(max(cursor-pairs/2, 0), total-pairs)
	return lines[start*2 : (start+pairs)*2]
}

// hitRows is one hit: the title with the match count, then the best snippet in
// quotes under who said it, so it reads as something said and not as a log
// line.
func (m tuiModel) hitRows(hit searchHit, width int, selected, stale bool) []string {
	ink := selectedPen(selected)
	tone := func(style lipgloss.Style) lipgloss.Style {
		if stale {
			return dimStyle
		}
		return style
	}
	bar := ink.render(lipgloss.NewStyle(), "  ")
	if selected {
		bar = ink.render(cursorBarStyle, "▌ ")
	}
	line := bar + ink.render(dimStyle, pad(formatWhen(m.clock(), hit.record.activity()), pickerWhenWidth))
	if m.pickerAll {
		line += ink.render(tone(providerStyle(hit.provider)), pad(truncate(hit.record.profile, pickerAccountWidth-1), pickerAccountWidth))
	}
	title, style := hit.record.session.title, fieldValueStyle
	switch {
	case title == "":
		title, style = "untitled session", unknownStyle
	case selected:
		style = nameActiveStyle
	}
	marker := ""
	if link, passed := m.lineage[hit.record.session.id]; passed && hit.record.session.id != "" {
		marker = " → " + link.TargetProfile
	}
	cell := max(width-lipgloss.Width(line)-searchMatchesWidth, 8)
	shown := truncate(title, max(cell-lipgloss.Width(marker)-2, 6))
	line += ink.render(tone(style), shown) + ink.render(tone(liveStyle), marker)
	first := padStyled(ink, line, width-searchMatchesWidth) +
		ink.render(tone(helpDescStyle), padLeft(strconv.Itoa(hit.matches), searchMatchesWidth))

	speaker := ink.render(tone(providerStyle(hit.provider)), hit.provider)
	if hit.bestFromUser {
		speaker = ink.render(tone(helpKeyStyle), "you")
		if stale {
			speaker = ink.render(dimStyle, "you")
		}
	}
	second := ink.render(lipgloss.NewStyle(), strings.Repeat(" ", 9)) + speaker + ink.render(lipgloss.NewStyle(), "  ")
	room := width - lipgloss.Width(second)
	cells := markedCells("“" + oneLine(hit.snippet) + "”")
	plain, match := tone(helpDescStyle), matchStyle
	if stale {
		match = dimStyle
	}
	second += renderCells(ink, cutCells(cells, room), plain, match)
	return []string{padStyled(ink, first, width), padStyled(ink, second, width)}
}

// searchTail is the count lines under the list: how many, in what order and
// how to change it, how much of the index was searched, a source that could
// not be read, and what headless hits are hidden.
func (m tuiModel) searchTail(width int) []string {
	indent := "  "
	coverage := m.search.coverage
	if m.search.stale() {
		reading := readQuery(m.search.query, m.search.prefix)
		words := fieldValueStyle.Render(reading.describe())
		if len(reading.terms) > 0 {
			words = ""
			for index, term := range reading.terms {
				if index > 0 {
					words += sectionLabelStyle.Render("  AND  ")
				}
				words += fieldValueStyle.Render(term)
			}
		}
		lines := []string{truncateStyled(indent+helpDescStyle.Render("searching  ")+words+helpDescStyle.Render("  …"), width)}
		if m.search.prefix && strings.HasSuffix(reading.fts, "*") {
			lines = append(lines, dimStyle.Render(truncate(indent+"the word being typed is matched as a prefix", width)))
		}
		return lines
	}
	hits := len(m.visibleHits())
	order := "best match first, o newest first"
	if m.search.newest {
		order = "newest first, o best match first"
	}
	count := plural(hits, "conversation")
	if !coverage.complete() {
		count += " so far"
	}
	lines := []string{dimStyle.Render(truncate(indent+count+" · "+order, width))}
	headless := ""
	switch hidden := m.hiddenHeadlessHits(); {
	case hidden > 0:
		headless = fmt.Sprintf("%d headless hidden, . shows", hidden)
	case m.showHeadless:
		headless = "headless shown, . hides"
	}
	if coverage.complete() {
		searched := fmt.Sprintf("searched all %d", coverage.total)
		if headless != "" {
			searched += " · " + headless
		}
		return append(lines, dimStyle.Render(truncate(indent+searched, width)))
	}
	if coverage.known {
		lines = append(lines, truncateStyled(indent+progressLine(coverage), width))
	}
	for _, failure := range coverage.failed {
		lines = append(lines, truncateStyled(indent+authMissingStyle.Render("✗ "+failure.describe()), width))
	}
	if headless != "" {
		lines = append(lines, dimStyle.Render(truncate(indent+headless, width)))
	}
	return lines
}

// progressBarWidth is the backfill's bar under the list.
const progressBarWidth = 22

// progressLine is the backfill's progress: a cyan bar with a figure, saying the
// oldest conversations are the ones still missing.
func progressLine(coverage indexCoverage) string {
	filled := 0
	if coverage.total > 0 {
		filled = min(coverage.searched*progressBarWidth/coverage.total, progressBarWidth)
	}
	info := lipgloss.NewStyle().Foreground(colorInfo)
	return info.Render(strings.Repeat("━", filled)) + trackStyle.Render(strings.Repeat("━", progressBarWidth-filled)) +
		info.Render(fmt.Sprintf("  searched %d of %d, newest first", coverage.searched, coverage.total))
}

// searchEmpty is the list when nothing matched. It says what was searched and
// how much of it, and while the index is still filling it says "not yet",
// never "never said".
func (m tuiModel) searchEmpty(width int) []string {
	coverage := m.search.coverage
	query := strings.TrimSpace(m.search.query)
	indent := "  "
	if !coverage.complete() && coverage.known {
		lines := []string{truncateStyled(indent+helpDescStyle.Render("Nothing said matches ")+fieldValueStyle.Render(query)+helpDescStyle.Render(" yet"), width), ""}
		lines = append(lines, dimStyle.Render(truncate(fmt.Sprintf("%sonly %d of %d conversations are searched so far;", indent, coverage.searched, coverage.total), width)))
		rest := fmt.Sprintf("the older %d are still being read, so this can still fill in", coverage.total-coverage.searched)
		for _, line := range wrapText(rest, min(max(width-2, 10), emptyNoteWidth)) {
			lines = append(lines, dimStyle.Render(indent+line))
		}
		lines = append(lines, "", truncateStyled(indent+progressLine(coverage), width))
		for _, failure := range coverage.failed {
			lines = append(lines, truncateStyled(indent+authMissingStyle.Render("✗ "+failure.describe()), width))
		}
		return lines
	}
	lines := []string{truncateStyled(indent+helpDescStyle.Render("Nothing said matches ")+fieldValueStyle.Render(query), width), ""}
	scope := fmt.Sprintf("searched all %d conversations", coverage.total)
	if !m.pickerAll {
		if profile, ok := m.selectedProfile(); ok {
			scope = fmt.Sprintf("searched all %d of %s's conversations", coverage.total, profile.Name)
		}
	}
	lines = append(lines, dimStyle.Render(truncate(indent+scope, width)), "")
	keys := []helpEntry{}
	if !m.pickerAll {
		keys = append(keys, helpEntry{"a", "every account"})
	}
	keys = append(keys, helpEntry{"ctrl+f", "titles, folders, ids"})
	return append(lines, truncateStyled(indent+renderKeys(keys...), width))
}

// emptyNoteWidth is the measure the "not yet" note is set to, narrower than the
// list so it reads as a note rather than a row.
const emptyNoteWidth = 48

// ---- drawing: the pane ------------------------------------------------------

func (m tuiModel) searchPane(width, rows int) []string {
	if strings.TrimSpace(m.search.query) == "" {
		return nil
	}
	if m.search.stale() {
		return []string{sectionLabelStyle.Render("PREVIEW"), "", unknownStyle.Render("searching…")}
	}
	hit, ok := m.selectedHit()
	if !ok {
		return m.queryPane(width)
	}
	lines := paneHeader(m, hit, width)
	if len(hit.seqs) == 0 {
		return lines
	}
	seq := hit.seqs[min(max(m.search.at, 0), len(hit.seqs)-1)]
	context, read := m.search.contexts[hitKey(hit, seq)]
	if !read {
		return append(lines, unknownStyle.Render("reading the transcript…"))
	}
	if context.problem != "" {
		return append(lines, unknownStyle.Render(truncate(context.problem, width)))
	}
	return padToRows(append(lines, m.matchLines(hit, context, width)...), rows, -1)
}

func paneHeader(m tuiModel, hit searchHit, width int) []string {
	title, style := hit.record.session.title, fieldValueStyle
	if title == "" {
		title, style = "untitled session", unknownStyle
	}
	return []string{
		sectionLabelStyle.Render("PREVIEW"),
		style.Render(truncate(title, width)),
		dimStyle.Render(truncate(previewWhere(m.clock(), hit.record), width)),
		dimStyle.Render(truncate(hit.record.profile+" · id "+hit.record.session.id, width)),
		"",
	}
}

// Turn rows in the pane: the matching turn gets up to six, its neighbours two.
const (
	matchTurnRows   = 6
	contextTurnRows = 2
)

// matchLines is the pane under its header: where the match sits along the
// conversation, the turns around it, and how the conversation ended.
func (m tuiModel) matchLines(hit searchHit, context hitContext, width int) []string {
	at := min(max(m.search.at, 0), len(hit.seqs)-1)
	left := sectionLabelStyle.Render("MATCH ") + fieldValueStyle.Bold(true).Render(fmt.Sprintf("%d OF %d", at+1, len(hit.seqs))) +
		"   " + helpKeyStyle.Render("n") + " " + helpKeyStyle.Render("N")
	right := dimStyle.Render(fmt.Sprintf("turn %d of %d", context.seq+1, context.total))
	track := matchTrack(hit.seqs, at, context.total, width-lipgloss.Width(left)-lipgloss.Width(right)-4)
	lines := []string{left + "  " + track + "  " + right, ""}
	if context.earlier {
		lines = append(lines, dimStyle.Render("…"))
	}
	for index, turn := range context.turns {
		if index > 0 {
			lines = append(lines, "")
		}
		speaker := providerStyle(hit.provider).Render(hit.provider)
		if turn.fromUser {
			speaker = helpKeyStyle.Render("you")
		}
		number := dimStyle
		if turn.match {
			number = helpDescStyle
		}
		lines = append(lines, spread(speaker, number.Render(fmt.Sprintf("turn %d", turn.seq+1)), width))
		body, cap := hintStyle, contextTurnRows
		switch {
		case turn.fromUser:
			body = fieldValueStyle
		case turn.match:
			body = helpDescStyle
		}
		if turn.match {
			cap = matchTurnRows
		}
		cells := markedCells(turn.text)
		if !turn.match {
			cells = markedCells(unmarked(turn.text))
		}
		rows := wrapCells(cells, width)
		if turn.match {
			rows = fromFirstMatch(rows, cap, width)
		}
		for _, row := range capRows(rows, cap, width) {
			lines = append(lines, renderCells(pen{}, row, body, matchStyle))
		}
	}
	if context.later {
		lines = append(lines, dimStyle.Render("…"))
	}
	if len(context.ending) > 0 {
		lines = append(lines, "", spread(sectionLabelStyle.Render("HOW IT ENDED"), dimStyle.Render(endedAt(m.clock(), hit.record.activity())), width))
		for _, turn := range context.ending {
			speaker, body := providerStyle(hit.provider).Render(pad(hit.provider, 9)), hintStyle
			if turn.fromUser {
				speaker, body = helpKeyStyle.Render(pad("you", 9)), fieldValueStyle
			}
			lines = append(lines, speaker+renderCells(pen{}, cutCells(markedCells(oneLine(unmarked(turn.text))), max(width-9, 4)), body, body))
		}
	}
	return lines
}

// endedAt is when the conversation was last spoken in, with the hour.
func endedAt(now, when time.Time) string {
	if when.IsZero() {
		return ""
	}
	day := formatWhen(now, when)
	clock := when.Local().Format("15:04")
	if day == clock {
		return clock
	}
	return day + " " + clock
}

// matchTrack draws where the matches sit along the conversation in the gauge's
// thin bar, the one shown in accent.
func matchTrack(seqs []int, at, total, width int) string {
	if width < 3 {
		return ""
	}
	cells := make([]string, width)
	for index := range cells {
		cells[index] = trackStyle.Render("━")
	}
	span := max(total-1, 1)
	for index, seq := range seqs {
		x := min(max((seq*(width-1)+span/2)/span, 0), width-1)
		style := helpDescStyle
		if index == at {
			style = helpKeyStyle
		}
		cells[x] = style.Render("┃")
	}
	return strings.Join(cells, "")
}

// queryPane is HOW THE QUERY WAS READ, shown when there is no hit to read out:
// the words as they were combined, and how many conversations hold each one.
func (m tuiModel) queryPane(width int) []string {
	reading := m.search.result.reading
	lines := []string{sectionLabelStyle.Render("HOW THE QUERY WAS READ"), ""}
	read := fieldValueStyle.Render(reading.describe())
	if len(reading.terms) > 0 {
		read = ""
		for index, term := range reading.terms {
			if index > 0 {
				read += sectionLabelStyle.Render("  AND  ")
			}
			read += fieldValueStyle.Render(term)
		}
	}
	lines = append(lines, truncateStyled(sectionLabelStyle.Render(pad("read as", 14))+read, width), "")
	complete := m.search.coverage.complete()
	for _, count := range m.search.counts {
		figure := plural(count.conversations, "conversation")
		if count.conversations == 0 {
			figure = "none"
		}
		if !complete {
			figure += " so far"
		}
		style := fieldValueStyle
		if count.conversations == 0 {
			style = authMissingStyle
		}
		lines = append(lines, helpDescStyle.Render(pad(count.term, 14))+style.Render(figure))
	}
	if len(m.search.counts) > 0 {
		lines = append(lines, "")
		if !complete {
			lines = append(lines, dimStyle.Render(truncate(fmt.Sprintf("counts cover the %d conversations read so far", m.search.coverage.searched), width)))
		} else {
			for _, count := range m.search.counts {
				if count.conversations == 0 && count.term != "both" && count.term != "all" {
					lines = append(lines, dimStyle.Render("a word no conversation holds is most often a typo"))
					break
				}
			}
		}
	}
	return lines
}

// ---- cells: marked text cut and wrapped by display width --------------------

// textCell is one rune with its display width and whether a search matched it.
type textCell struct {
	r     rune
	width int
	match bool
}

func markedCells(text string) []textCell {
	var cells []textCell
	for _, span := range parseMarked(text) {
		for _, r := range span.text {
			cells = append(cells, textCell{r: r, width: ansi.StringWidth(string(r)), match: span.match})
		}
	}
	return cells
}

func cellsWidth(cells []textCell) int {
	total := 0
	for _, cell := range cells {
		total += cell.width
	}
	return total
}

// cutCells fits cells to width, ending in an ellipsis when it had to cut.
func cutCells(cells []textCell, width int) []textCell {
	if width <= 0 {
		return nil
	}
	if cellsWidth(cells) <= width {
		return cells
	}
	used := 0
	for index, cell := range cells {
		if used+cell.width > width-1 {
			return append(append([]textCell(nil), cells[:index]...), textCell{r: '…', width: 1})
		}
		used += cell.width
	}
	return cells
}

// noLineStart is what a Japanese line must never begin with (kinsoku): a line
// break is moved back a character rather than strand one of these.
const noLineStart = "、。？！」』）・，．"

// wrapCells wraps cells to width. It breaks at a space where there is one, and
// between two wide characters otherwise, never before one of noLineStart.
func wrapCells(cells []textCell, width int) [][]textCell {
	if width < 2 {
		return [][]textCell{cells}
	}
	var lines [][]textCell
	var line []textCell
	used, breakAt := 0, -1
	trimEnd := func(l []textCell) []textCell {
		for len(l) > 0 && l[len(l)-1].r == ' ' {
			l = l[:len(l)-1]
		}
		return l
	}
	rescan := func() {
		breakAt, used = -1, 0
		for index, cell := range line {
			used += cell.width
			if cell.r == ' ' {
				breakAt = index + 1
			} else if cell.width == 2 && index > 0 && !strings.ContainsRune(noLineStart, cell.r) {
				breakAt = index
			}
		}
	}
	for _, cell := range cells {
		if cell.r == '\n' {
			lines = append(lines, trimEnd(line))
			line, used, breakAt = nil, 0, -1
			continue
		}
		if len(line) == 0 && cell.r == ' ' {
			continue
		}
		if used+cell.width > width {
			switch {
			case cell.r == ' ':
				lines = append(lines, trimEnd(line))
				line, used, breakAt = nil, 0, -1
				continue
			case breakAt > 0:
				rest := append([]textCell(nil), line[breakAt:]...)
				lines = append(lines, trimEnd(line[:breakAt]))
				for len(rest) > 0 && rest[0].r == ' ' {
					rest = rest[1:]
				}
				line = rest
			default:
				lines = append(lines, line)
				line = nil
			}
			rescan()
		}
		if cell.width == 2 && len(line) > 0 && !strings.ContainsRune(noLineStart, cell.r) {
			breakAt = len(line)
		}
		line = append(line, cell)
		used += cell.width
		if cell.r == ' ' {
			breakAt = len(line)
		}
	}
	if len(line) > 0 {
		lines = append(lines, trimEnd(line))
	}
	return lines
}

// fromFirstMatch starts a long matching turn a row above its first match when
// the match would otherwise fall below the rows the pane gives it, marking the
// cut with an ellipsis: the turn is shown because of the match, so the match
// is what must be on screen.
func fromFirstMatch(rows [][]textCell, cap, width int) [][]textCell {
	first := -1
	for index, row := range rows {
		for _, cell := range row {
			if cell.match {
				first = index
				break
			}
		}
		if first >= 0 {
			break
		}
	}
	if first < cap {
		return rows
	}
	rows = append([][]textCell(nil), rows[first-1:]...)
	lead := append([]textCell{{r: '…', width: 1}}, rows[0]...)
	for cellsWidth(lead) > width && len(lead) > 1 {
		lead = lead[:len(lead)-1]
	}
	rows[0] = lead
	return rows
}

// capRows keeps at most cap rows, the last ending in an ellipsis when rows
// were dropped.
func capRows(rows [][]textCell, cap, width int) [][]textCell {
	if len(rows) <= cap {
		return rows
	}
	rows = append([][]textCell(nil), rows[:cap]...)
	last := rows[cap-1]
	for cellsWidth(last) > width-1 && len(last) > 0 {
		last = last[:len(last)-1]
	}
	rows[cap-1] = append(append([]textCell(nil), last...), textCell{r: '…', width: 1})
	return rows
}

// renderCells styles runs of plain and matched cells.
func renderCells(ink pen, cells []textCell, plain, match lipgloss.Style) string {
	var out strings.Builder
	var run []rune
	matched := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		style := plain
		if matched {
			style = match
		}
		out.WriteString(ink.render(style, string(run)))
		run = run[:0]
	}
	for _, cell := range cells {
		if cell.match != matched {
			flush()
			matched = cell.match
		}
		run = append(run, cell.r)
	}
	flush()
	return out.String()
}

// oneLine folds a snippet's line breaks and runs of space into single spaces.
func oneLine(text string) string {
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' }), " ")
}

func padLeft(value string, width int) string {
	if gap := width - lipgloss.Width(value); gap > 0 {
		return strings.Repeat(" ", gap) + value
	}
	return value
}
