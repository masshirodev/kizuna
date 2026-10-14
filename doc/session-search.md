# Searching what was said, across every account

A proposal (2026-10-13). Find a past conversation by something said in it, in
any profile and any provider, and from the hit resume it or hand it off. Back to
the [README](../README.md); builds on [session-store.md](session-store.md) and
[sessions.md](sessions.md).

## Why

The picker's `/` matches a row's title, folder, account and id. That finds a
conversation you can name. It does not find "the one where we decided how the
OpenCode merge lock works" or "where did that `ENOSPC` come from", which is
usually what you are looking for a week later, and usually in another account,
because the work moved between accounts when one ran out.

Grepping the state directories by hand does not answer it either:

- **It only reaches half the history.** Claude Code and Codex keep JSONL
  transcripts that `rg` can read, about 2.7 GB of Claude transcripts across the
  four Claude profiles on the workstation. OpenCode keeps its conversations in
  `opencode.db` (about 1.5 GB across four profiles) and Antigravity in per-conversation
  protobuf stores. `rg` gets nothing useful out of either.
- **A hit is a file path in a wall of JSON.** Most matches are in tool calls and
  tool output, not in what anyone said, and the path does not say which
  conversation it was, what it was called, or how to get back into it.
- **It cannot act.** Even after finding the file you still have to work out the
  profile, the session id and the folder, then `cd` there and resume by hand.

kizuna already has every piece but the search itself: an index of every
profile's conversations (`sessions.db`), readers that reduce each provider's
format to spoken turns, and a picker that resumes a row in its own folder or
hands it to another account.

## Goals

1. Search the text of past conversations across every profile and provider at
   once, or narrowed to one profile, provider, or repository.
2. A hit names the conversation the way the picker does (title, account,
   folder, last activity) and shows the matching turn in context.
3. **From a hit: resume it, or hand it off**, with the same keys as the picker
   and the same rules (resume reopens under the account that recorded it, in
   the folder it ran in; a handoff builds the brief from that account).
4. The same search from the command line, and to agents over MCP.
5. Search what was *said*, never tool output. A hit has to be something a
   person can read.

Not goals: searching tool output or diffs, search across machines, and semantic
or embedding search (a full-text index first; see [Later](#later)).

## Design

### 1. Every conversation's turns go into the index

Today turns are cached on demand: a handoff reads a conversation whole and
caches it, and a preview uses that cache when it is fresh. On the workstation
that is 30 of 965 indexed sessions, 3,880 turns, 2.2 MB of text. Search needs
all of them.

P2's as-built note gives the reason it was not done: "Materializing every turn
of every transcript would have put 2 GB of Claude transcripts into the first
refresh." That is still true, so the backfill must not run inside the refresh
the TUI waits on:

- **Refresh stays as it is.** Listing rows still costs a `stat` per source.
- **A backfill fills `turn` in the background**, newest conversations first,
  skipping any whose `turn_source.fingerprint` already matches the session's.
  It reuses the existing readers (`readClaudeTranscript`, `readCodexRollout`,
  `readOpenCodeMessages`, the Antigravity walker) through `cacheSessionTurns`,
  so there is still one parser per provider.
- **It runs off the keypress** in the TUI, and to completion in
  `kizuna search` (with a `reading N of M conversations…` line on stderr, so
  the first search is slower and every later one is not).
- **After the first backfill it is incremental.** A transcript that grew moves
  its fingerprint and is re-read; nothing else is.
- **Holding the index lock** while it writes, in batches, so a picker reading
  the index waits for one batch at most, not for the whole backfill
  (`sessionIndexMu` already serialises this process; another process's writes
  are already ordered by SQLite).

Estimate, to be measured: 2.2 MB for 30 sessions suggests tens of MB of text
for 965, against 3.6 MB of `sessions.db` today. Headless runs are indexed too,
and hidden from results unless asked for, the way `.` hides them in the picker.

### 2. Full-text search with FTS5

`modernc.org/sqlite` is built with `SQLITE_ENABLE_FTS5`, so no new dependency:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS turn_fts USING fts5(
  text,
  content='turn',
  content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);
-- kept in step with turn by AFTER INSERT / DELETE / UPDATE triggers
```

- An external-content table, so the text is stored once (in `turn`) and the FTS
  index only holds the postings.
- `turn` has a composite primary key, so its implicit `rowid` is what
  `content_rowid` points at. Rewriting a conversation's turns already deletes
  and reinserts them, which the triggers follow.
- Ranking is `bm25(turn_fts)`, then the session's last activity. Snippets come
  from `snippet(turn_fts, 0, '[', ']', '…', 12)`, rendered as highlights.
- **Query syntax is FTS5's**, passed through: words are ANDed, `"a phrase"`
  matches a phrase, `prefix*` a prefix, `OR` and `NOT` work. A query that FTS5
  refuses to parse is retried as a quoted phrase rather than shown as an error,
  so pasting `ENOSPC: no space left` just works.
- **Japanese has no spaces**, and `unicode61` tokenizes a run of kana and kanji
  as one token, so a word inside a Japanese sentence would not match. Use the
  `trigram` tokenizer for a second index if that turns out to matter; start
  without it.

Results are grouped by conversation: one row per session, carrying its best
turn's snippet and a count of the other matching turns.

### 3. The search mode in the TUI

Search is a mode of the existing two-pane picker, not a new screen, so resume
and handoff come for free and look the same:

- Opened from the `space` palette ("search conversations") and from `ctrl+f`
  inside the `R` / `H` picker, which switches the picker's `/` from matching
  rows to matching what was said. The palette entry starts in all-accounts
  mode; `a` narrows it to the selected account, as in the picker.
- **The list** is the hits, one row per conversation, in the picker's row
  format plus the hit count (`3 matches`) and the account in all-accounts mode.
- **The pane** shows the matching turn in context (the turn before and after
  it, with the match highlighted) instead of the conversation's tail. `n` / `N`
  step through that conversation's other matches. The header is the picker's:
  title, account, full conversation id, last activity.
- **`enter` resumes** the hit under the account that recorded it, in its
  folder, exactly as the picker does, including reporting a folder that has
  moved or been deleted rather than resuming somewhere else.
- **`H` hands it off**, skipping the wizard's first step, the way `H` inside
  the resume picker does: the brief is built from the recording account and
  offered to the others.
- **`y` copies** the resume command (`kizuna <profile> resume <id>`), for
  resuming in another terminal or on another pane.
- The search runs as you type, off the keypress, debounced, with the latest
  query winning; a result that lands after the query moved on is dropped. While
  the backfill is still running, the count under the list says so
  (`searched 612 of 965 conversations`), so an empty result is not mistaken for
  a final one.

Per the workstation's rule for UI that reshapes a screen, **a design brief
([briefs/done/SESSION-SEARCH.md](briefs/done/SESSION-SEARCH.md)) comes before P2's code**: the pane's in-context layout and how the
mode is told apart from the plain picker are what it needs to settle.

### 4. From the command line

```sh
kizuna search "merge lock"                        # every account, interactive picker in search mode
kizuna search "merge lock" --print                # one line per hit, no TUI
kizuna search "ENOSPC" --profile max2 --repo kumiko --since 2026-09-01
kizuna search "wave-loop" --headless              # include headless runs
kizuna search "merge lock" --json                 # for scripts and agents
```

A line of `--print` output is `profile  provider  last-activity  folder  title`
and then the snippet indented under it. `--json` emits each hit as
`{profile, provider, id, title, folder, updated, matches, snippet, resume}`,
where `resume` is the command that reopens it. On a terminal without `--print`
or `--json`, `search` opens the TUI search mode on that query, so `enter` and
`H` work from the command line too.

`--repo` matches the folder's repository by name or path, the same way
`kizuna peers [repo]` does.

### 5. Over MCP

`kizuna mcp serve` gains `search_sessions` beside `list_peers`, `send_message`
and `read_inbox`: arguments `query`, optional `profile`, `repo`, `limit`; the
result is the `--json` hits. An agent can then answer "where did we decide X" from
another account's history, or find the conversation a card came out of.

It is read-only and returns what was said, nothing more. It never resumes or
hands off; those stay with the person at the TUI. **Results can carry anything
said in any account's conversation**, including pasted secrets: the snippet is
what the index holds, and the index is already readable by the same user, so
this widens who *asks* rather than who *can read*. Still, the MCP tool is off
until `kizuna integrate messaging` (or a flag of its own) turns it on.

## Phases

| Phase | What | Done when |
| --- | --- | --- |
| P0 | Background backfill of `turn` for every session; measured cost on the workstation | every indexed session has fresh turns; first-backfill time and index size recorded below |
| P1 | `turn_fts` with triggers; `kizuna search --print / --json` | a phrase from an OpenCode, a Claude and an Antigravity conversation each finds it |
| P2 | Design brief, then the TUI search mode: resume, hand off, copy | `enter` and `H` from a hit behave exactly as from the picker |
| P3 | `search_sessions` over MCP | an agent in one profile finds a conversation from another |

## Tests

- Backfill: a stale fingerprint is re-read, a fresh one is skipped, a deleted
  transcript's turns go with its row (the cascade P2 already relies on).
- FTS: insert, rewrite and delete of a conversation's turns keep `turn_fts` in
  step; a query FTS5 cannot parse falls back to a phrase.
- Grouping: three matching turns in one conversation are one hit with
  `3 matches`, ranked by the best.
- Resume from a hit uses the recording profile and folder (reuse the resume
  picker's tests against a search-mode model).
- Handoff from a hit skips the first step and offers every other account.
- Headless hits are hidden unless asked for, in the TUI and with `--headless`.
- A real-store test, skipped where the store is missing, like
  `TestRealDBResumeImportAndMerge`: a known phrase from the operator's own
  OpenCode archive is found.

## Open questions

- **Deleted transcripts.** The question already open in
  [session-store.md](session-store.md#open-question) gets sharper: a hit on a
  conversation whose transcript is gone can be read but not resumed. Keeping the
  turns as a searchable, read-only row is one answer; dropping them with the
  transcript (today's behaviour) is the other. Until it is decided, search
  follows today's behaviour.
- **Assistant turns, or only the user's?** Searching both finds "where did it
  tell me X"; searching only the user's is smaller and less noisy. Default to
  both, with `--from user` to narrow.

## Later

- A `trigram` index for Japanese, if the default tokenizer misses in practice.
- Semantic search over the same turns, once full-text search shows where it
  falls short.
