# Searching what was said, across every account

Find a past conversation by something said in it, in any profile and any
provider, and resume it or hand it off from the hit. Proposed and built
2026-10-13. Back to the [README](../README.md). It builds on
[session-store.md](session-store.md) and [sessions.md](sessions.md).

- In the TUI: `ctrl+f` on the board, or in the `R` / `H` picker, or *search
  what was said* in the `space` palette.
- From a shell: `kizuna search <query>`.
- For agents: the `search_sessions` tool of `kizuna mcp serve`.

## Why

The picker's `/` matches a row's title, folder, account and id. That finds a
conversation you can name. It does not find "the one where we decided how the
OpenCode merge lock works" or "where did that `ENOSPC` come from". That is
usually what you are looking for a week later, and usually in another account,
because the work moved between accounts when one ran out.

Grepping the state directories by hand does not answer it either:

- **It only reaches half the history.** Claude Code and Codex keep JSONL
  transcripts that `rg` can read: about 2.7 GB of Claude transcripts across the
  four Claude profiles on the workstation. OpenCode keeps its conversations in
  `opencode.db` (about 1.5 GB across four profiles), and Antigravity keeps them
  in per-conversation protobuf stores. `rg` gets nothing useful out of either.
- **A hit is a file path in a wall of JSON.** Most matches are in tool calls and
  tool output, not in what anyone said. The path also does not say which
  conversation it was, what it was called, or how to get back into it.
- **It cannot act.** Even after finding the file, you still have to work out the
  profile, the session id and the folder, then `cd` there and resume by hand.

kizuna already had every piece except the search itself:

- an index of every profile's conversations (`sessions.db`);
- readers that reduce each provider's format to the turns that were spoken;
- a picker that resumes a row in its own folder, or hands it to another account.

## Goals

1. Search the text of past conversations across every profile and provider at
   once, or narrowed to one profile or repository.
2. A hit names the conversation the way the picker does (title, account, folder,
   last activity), and shows the matching turn in context.
3. **From a hit: resume it, or hand it off.** The keys are the picker's, and so
   are the rules: resume reopens under the account that recorded it, in the
   folder it ran in, and a handoff builds the brief from that account.
4. The same search from the command line, and for agents over MCP.
5. Search what was *said*, never tool output. A hit has to be something a person
   can read.

Not goals:

- searching tool output or diffs;
- search across machines;
- semantic or embedding search (full-text search first; see
  [Later](#later)).

## How it works

### 1. Every conversation's turns go into the index

The `turn` table holds what the handoff brief and the preview pane already
reduce a conversation to: spoken text only, with injected preambles stripped.
Before this, turns were cached only when something read a conversation whole (a
handoff, an OpenCode preview), so 30 of 965 conversations had them.
`backfillSessionTurns` (`cmd/kizuna/search.go`) now reads the rest:

- **Refresh stays as it is.** Listing rows still costs one `stat` per source.
  The backfill never runs inside the refresh the TUI waits on.
- **Newest first, by fingerprint.** It reads every conversation whose
  `turn_source.fingerprint` differs from its session row's, newest first. A
  conversation whose fingerprint has not moved is skipped. A transcript that
  grew is read again whole.
- **One parser per provider, still.** It goes through the existing readers
  (`readAllMessages`, `readOpenCodeMessages`, `readAntigravityMessages`), by the
  source path the index recorded. It does not go through `readSessionMessages`,
  which looks a Claude transcript up among all of them: that is fine for one
  keypress but quadratic for a backfill.
- **In batches of 24, read in parallel.** Each batch is read by up to six
  goroutines outside the index lock, then written in one transaction under it.
  A picker reading the index waits for one batch at most.
- **A source that cannot be read is named, never taken as empty.** The report
  carries each profile with unread conversations. The TUI and the CLI print it
  as `✗ opencode-go: its store could not be read, 41 unread`.
- **The OpenCode reader now waits on a lock.** Several goroutines open the same
  store at once, beside an OpenCode that may be writing it. Without a busy
  timeout, the second one got `SQLITE_BUSY`. The first run on the workstation
  lost six conversations that way.

Headless runs are indexed too, and hidden from results unless asked for, the
way `.` hides them in the picker.

### 2. Full-text search with FTS5

`modernc.org/sqlite` is built with `SQLITE_ENABLE_FTS5`, so there is no new
dependency:

```sql
CREATE VIRTUAL TABLE turn_fts USING fts5(
  text, content = 'turn', content_rowid = 'n',
  tokenize = 'unicode61 remove_diacritics 2'
);
-- kept in step with turn by AFTER INSERT / DELETE / UPDATE triggers
```

- **An external-content table.** The text is stored once, in `turn`, and the FTS
  index holds only the postings. Every rewrite or delete of a conversation's
  turns goes through the triggers, including the delete that follows a
  transcript being removed.
- **`turn` gained an `INTEGER PRIMARY KEY` (`n`) for `content_rowid`.** An
  implicit rowid can be renumbered by `VACUUM`, which would quietly point every
  hit at the wrong turn. The index's `PRAGMA user_version` is now `1`. An older
  index drops its `turn` and `turn_source` tables on first open; they are a
  cache, and the backfill reads them again. An older kizuna binary still works
  against the new schema, because its inserts name their columns.
- **Order.** Hits come newest first, by the conversation's last activity. `o`
  (or `--best`, or `order: "best_match"` over MCP) ranks them instead by their
  best turn's `bm25`, then by last activity. The 200-conversation cap is taken
  after ordering, so it keeps the newest 200 or the best 200. The snippet is
  `snippet(turn_fts, 0, …, '…', 16)` of the best turn. Its matched tokens are
  drawn amber on a dark amber cell (`colorMatch`) in the TUI, and between
  square brackets in `--print` and `--json`.
- **Query syntax is FTS5's, passed through.** Plain words are ANDed, `"a
  phrase"` is a phrase, `prefix*` is a prefix, and `OR` and `NOT` work. A query
  FTS5 refuses to parse is searched as the phrase it spells, so pasting `ENOSPC:
  no space left` just works. The READ AS line then says `searched as the phrase
  "…"`.
- **`unicode61` treats an underscore as a separator**, so `SQLITE_BUSY` is two
  tokens and either word finds it.
- **Japanese has no spaces**, and `unicode61` makes a run of kana and kanji one
  token, so a word inside a Japanese sentence is not found on its own. English
  inside Japanese text is found (the handoff's `backfill 中に SQLITE_BUSY`
  sample matches).

The search groups the matching turns by conversation. Each hit carries:

- how many of its turns matched, and which ones (in conversation order);
- its best turn and that turn's snippet;
- its total turn count.

It reads the index only and never refreshes, so it is cheap enough to run on
every pause in typing. The coverage it reports (conversations whose turns are
read, out of every one indexed) is how much it could see.

### 3. The search mode in the TUI

The design is the handoff in
[handoffs/Session search.dc.html](handoffs/Session%20search.dc.html), answering
the brief [briefs/done/SESSION-SEARCH.md](briefs/done/SESSION-SEARCH.md). The
code is `cmd/kizuna/searchbox.go`.

**One box, two searches.** `R` stays the resume picker. `ctrl+f` flips it
between the `/` filter it always had (titles, folders, accounts, ids) and a
ranked search of what was said. The two modes are told apart three ways at once:

- the mode pair (`titles` / `what was said`) on the right of the heading, drawn
  as the scope chip is;
- the field: a small `/` line for titles, a full-width tinted field for what was
  said;
- an empty state that names what it searched (`No title, folder or id matches …`
  against `Nothing said matches …`).

The plain picker's `/` line moved under the heading for this, as the handoff
picker already drew it. Both modes take the same rows, so switching never
resizes the box.

**Entry.**

- `ctrl+f` on the board, or *search what was said* in the palette's
  `CONVERSATION` group, opens the box in search mode on every account. The
  palette shows `965 indexed`, or `612 of 965 indexed` while the backfill runs.
- `ctrl+f` inside `R` or `H` switches that picker. The query survives a trip to
  titles and back.
- In the `H` picker the stepper keeps the heading, and the scope and the mode
  pair share the line under it.

**Focus.** The box opens with the keys in the field, so typing searches. `↓` or
`↵` hands the keys to the results, and `/` takes them back, the way the board's
own `/` works. The letter keys (`H` `y` `n` `a` `.` `o`) act only once the
results have the keys; until then they are typed.

**Typing.**

- The query runs 120 ms after typing pauses.
- The word being typed is matched as a prefix (`bu` → `bu*`). It stays a prefix
  when the keys move to the results: re-running it exactly turned a half-typed
  word's ten hits into none.
- The results on screen stay in place but dim, lose their marks and their
  cursor, and nothing can be acted on until the new ones land. The count line
  says `searching sqlite AND bu* …`, and the pane says `searching…`.
- A result for an older query is dropped.

**The list.** Each hit takes two lines:

1. when, the account (in all-accounts mode), the title with a handed-off row's
   `→ account` marker, and `MATCHES` in the column where the titles mode has
   `FOLDER`;
2. the best snippet in quotation marks, after who said it (`you`, or the
   provider in its colour), so it reads as something said and not as a log
   line.

Under the list come:

- the count and the order in force, worded like the headless note (`newest
  first, o best match first`);
- the coverage: `searched all 965`, or a cyan progress bar while the backfill
  runs, with `so far` on every count;
- a `✗` line in warning yellow for each source that could not be read;
- how many headless hits are hidden.

**The pane** opens on the row's best match, numbered in conversation order
(`MATCH 2 OF 5`). A thin track shows where the matches sit along the
conversation (`turn 14 of 212`), with the shown one in accent; `n` and `N` step
through them. Under that:

- the turn before, at most two rows;
- the matching turn, at most six rows, matched words marked. A long turn whose
  first match would fall below those rows opens a row above the match, marked
  with `…`;
- the turn after, at most two rows;
- `HOW IT ENDED`: the last two turns, one line each.

Japanese is cut and wrapped by display width, and a line never begins with
`、。？！」』）・`.

**Empty results.**

- While the backfill runs, the list says `Nothing said matches … yet`, how many
  are still to read, and the progress bar: "not yet", never "never said".
- With the index complete, it says what was searched (`searched all 212 of
  claude-personal's conversations`) and offers `a` for every account and
  `ctrl+f` for titles.
- Either way the pane shows HOW THE QUERY WAS READ: the words as they were
  combined, and how many conversations hold each one. A word in none of them
  usually explains the empty list.

**Keys on the results.**

| Key | Does |
| --- | --- |
| `↵` | Resumes the hit under the account that recorded it, in its folder, exactly as the picker does. A folder that has moved is reported and the box stays open. In the `H` picker, `↵` chooses the hit as the conversation leaving. |
| `H` | Hands the hit off, skipping the wizard's first step. |
| `y` | Copies `kizuna <profile> resume <id>` to the clipboard (OSC 52). |
| `n` / `N` | Steps through the hit's matches. |
| `a` / `.` / `o` | Toggles the scope, headless hits, and the order. |
| `ctrl+f` | Switches back to titles. |
| `/` | Hands the keys back to the field. |
| `esc` | Closes the box. |

Provider, repository and `--from user` stay command-line flags: a box with
more toggles stops being a picker.

**The narrow fold.** Below 83 columns inside the box, the pane folds away as it
does in the plain picker. A hit keeps its snippet line, because with no pane
that line is the only evidence of the match. Each toggle shows only its current
chip, the footer drops keys from its end, and `n N` is not offered.

**The backfill in the TUI** starts when the search mode opens, unless one is
already running. Each batch's report updates the coverage and re-asks the same
query in the same generation, so the list fills in without dimming.

### 4. From the command line

```sh
kizuna search merge lock                          # the TUI on that query, keys on the results
kizuna search --print merge lock                  # one block per hit, no TUI
kizuna search --print --profile max2 --repo kumiko --since 2026-09-01 ENOSPC
kizuna search --print --headless wave-loop        # include headless runs
kizuna search --print --from user ingress         # only what you said
kizuna search --print --best merge lock           # best match first instead of newest
kizuna search --json merge lock                   # for scripts and agents
```

- **Before searching**, it brings the index up to date. It says how far it has
  got on stderr when stderr is a terminal, and names any source it could not
  read. The first run reads every conversation; later runs read only what
  changed.
- **`--print`** writes one block per hit: `profile  provider  when  folder
  title  (N matches)`, then `said-by: snippet`, then the resume command. It ends
  with the count and the READ AS line. An empty result prints how many
  conversations hold each word.
- **`--json`** writes one object:
  `{query, read_as, searched, total, failed, hits: [{profile, provider, id,
  title, folder, updated, matches, turns, said_by, snippet, resume,
  headless}]}`. The snippet marks matches with `[brackets]`.
- **On a terminal**, with neither flag, it opens the TUI search mode on the
  query with the keys on the results, so `↵` and `H` work from the command line
  too.
- **`--repo`** matches a folder by repository name (`git rev-parse`, once per
  folder), or by a path inside it, the way `kizuna peers [repo]` does.

### 5. Over MCP

`kizuna mcp serve` has `search_sessions` beside `list_peers`, `send_message`
and `read_inbox`. It takes `query`, and optionally `profile`, `repo`, `limit`
(default 20), `from_user`, `headless` and `order` (`newest`, the default, or
`best_match`), and it answers with the `--json`
object. An agent can then answer "where did we decide X" from another account's
history, or find the conversation a card came out of.

- **It spends at most 20 s bringing the index up to date** before it answers
  from what is there. The first read of the workstation's history takes about
  30 s, longer than an agent should wait on one tool call. `searched` /
  `total` say how much it saw, and the next call reads on.
- **It is read-only** and returns what was said, nothing more. It never resumes
  or hands off; those stay with the person at the TUI.
- **Results can carry anything said in any account's conversation**, including
  pasted secrets. The snippet is what the index holds, and the index is already
  readable by the same user, so this widens who *asks*, not who *can read*. The
  tool is there wherever the messaging server is registered, which `kizuna
  integrate messaging <profile>` does.

## Measured on the workstation

2026-10-13, nine profiles, 966 conversations:

| | |
| --- | --- |
| Turns read | 31,540 turns, 23 MB of text |
| `sessions.db` | 3.6 MB before, 42 MB after |
| First backfill, one goroutine, index on `/mnt/hdd2` | 103 s |
| First backfill, parallel reads, index on `/mnt/hdd2` | 78 s, of which 55 s waited on the spinning disk |
| First backfill, parallel reads, index on `/` (where it lives) | **31 s** |
| Of that, reading | Claude 343 conversations in 10 s; OpenCode 605 in 23 s; Antigravity 18 in 0.03 s |
| A search with nothing changed | 40 ms, the whole command |
| A search while agents are writing | 1–2 s: a live conversation's transcript moves its fingerprint and is read again whole |

The OpenCode cost is decoding parts that are mostly tool output, at about 38 ms
a session, and the store is already indexed on `part.session_id`.

## Where it departs from the handoff

- **The count lines under the list are indented two cells.** The handoff draws
  them at the list's edge in 2a–2d and 2g, but two cells in 2h, which it calls
  today's picker; today's picker indents its count two cells too. The two cells
  line them up with the row text, and the titles and search modes agree.
  `TestSearchBoxMatchesTheDesignFrames` pins this; every other cell of the
  eight frames matches the handoff's own rendering.
- **"N headless hidden" counts the headless hits, not the index.** The handoff's
  sample says 14 of the 965 conversations are headless. A search can only say
  how many of its matches it is hiding, so the TUI always searches headless runs
  and hides them, and the count is of hidden hits.
- **The copied status is the status line's one colour.** The handoff draws
  `copied` muted and the command in text colour. Every box's status line is
  drawn in its kind's colour, and this one is no exception.
- **`↵` keeps the prefix match.** The handoff only says the word being typed is
  matched as a prefix. Running the query exactly when the keys moved emptied
  half-typed results, so the reading stays as it was shown.
- **`ctrl+f` in the picker's fields switches modes** rather than moving the
  caret forward, as `Ctrl-F` does in every other field. `→` still moves it.
- **Newest first is the default order** (2026-10-15), where the handoff drew
  best match first. In use a search is nearly always for something recent, and
  `bm25` across conversations of very different lengths put old, long ones on
  top. The design test still pins the handoff's frames: 2a is drawn with best
  match toggled on, 2b with newest first.
- **The pane opens a long matching turn at its match.** The handoff's sample
  turns fit in six rows. Real ones do not, and their first match was cut off.

## What it does not do yet

- **Deleted transcripts drop out of search.** The question still open in
  [session-store.md](session-store.md#open-question) gets sharper: a hit on a
  conversation whose transcript is gone could be read but not resumed. Keeping
  the turns as a read-only row is one answer. Search follows today's behaviour:
  the turns go with the row.
- **Japanese words inside Japanese text are not found on their own** (see
  above). A second, `trigram`, index is the remedy if it matters in practice.
- **A conversation still being written is read again whole** every time it
  moves. Reading only what was appended would make a search during a wave
  cheaper; it has not been worth it yet.
- **No real-store test.** The proposal asked for one beside
  `TestRealDBResumeImportAndMerge`. The workstation run above was the check;
  there is no test that re-runs it.
- **Codex rollouts are untested on real data.** The workstation has none; the
  backfill reads them through the same reader as the handoff.
- **Live-only OpenCode sessions are not searched.** A session that exists only
  in a running instance's store is not indexed until that instance merges, as
  before.

## Decisions

- **Newest first by default.** See the departures above. `--newest` is still
  accepted and is now the default; `--best` asks for rank order.

- **Both sides are searched.** Your turns and the agent's are searched by
  default, so "where did it tell me X" works; `--from user` (and `from_user`
  over MCP) narrows to yours.
- **The backfill runs whenever search opens**, not at TUI start. After the first
  run it costs a refresh and nothing more, and a TUI that is never asked to
  search never reads 2.7 GB.

## Later

- A `trigram` index for Japanese, if the default tokenizer misses in practice.
- Semantic search over the same turns, once full-text search shows where it
  falls short.
