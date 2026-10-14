# Design brief: searching what was said, from the resume picker

Repository: https://github.com/masshirodev/kizuna (public). Read these in it:

- `doc/session-search.md`: the proposal this brief comes from (the index, the
  query syntax, the CLI and MCP halves).
- `doc/sessions.md`, "The picker is two panes": the screen this reshapes.
- `doc/tui.md`: the canvas, the box vocabulary, the palette, and the earlier
  handoffs' decisions.
- `cmd/kizuna/wizard.go` (`recentPicker`, `pickerBody`, `pickerList`,
  `recentRows`) and `cmd/kizuna/preview.go` (`previewLines`,
  `conversationLines`): the picker as drawn today.
- `doc/briefs/done/` and `doc/handoffs/done/`: earlier briefs and the handoffs
  that answered them.

Written 2026-10-13, from: *"worth it to add to kizuna a command/menu that
searches in past profiles chats? like grep"* and then *"option to continue that
session (or handoff) from the search menu would be nice"*.

This brief is for Claude Design. It is not a plan. It adds a search mode to the
resume picker (`R`): you type words, and get back the conversations, across
every account, in which someone said them. From a hit you resume the
conversation or hand it off. What the mode looks like, how it is told apart
from the plain picker, and how a hit shows its match are layout questions.
Nothing below is built.

## What exists today, measured

The resume picker is a box over the board, 140 wide at most (134 inside, after
the border and padding). Its parts, top to bottom:

1. **Heading.** `resume`, then the scope toggle (` this account ` / `all
   accounts`, the current one a chip, `a` flips it), and on the right the
   search corner (`/ search title, folder or id`, the query with a caret while
   typing, or the filter left behind with `esc / clears`).
2. **Two panes** with a 3-column divider. The list takes 48% of the 131
   columns, between 46 and 62 wide, so 62 at full size. The preview gets the
   rest, 69. Below 83 columns (46 + 3 + 34) the preview folds away and the
   list is drawn alone.
   - **The list**: a faint uppercase column header (`WHEN  ACCOUNT
     CONVERSATION  FOLDER`, with `ACCOUNT` only in all-accounts mode), then one
     row per conversation: `when` (7), account (14, in the provider's colour),
     title (what is left), folder (18, dimmed). A handed-off row carries
     `→ max` after its title. The cursor row has the `▌` bar and the selected
     tint. Under the rows sits a count: `48 conversations · newest activity
     first · 12 headless hidden, . shows`.
   - **The preview**: `PREVIEW`, then the title, then `when · folder`, then
     `account · id <full id>`, a blank line, then **the end** of the
     conversation as blocks: who spoke (`you` in the key colour, the provider's
     name in its colour), then what they said, wrapped, at most 4 rows a turn,
     with a `…` above the first turn when earlier ones are off screen.
3. **Footer keys**: `esc close   ↑↓ choose   ↵ resume in ~/projects/kumiko
   H hand this off instead`.

`/` today filters rows by title, folder, account or id. It never looks inside a
conversation.

## What the new mode has to show

- **The query**, with FTS5 syntax available but never required: plain words
  are ANDed, `"a phrase"`, `prefix*`, `OR`, `NOT`. A query FTS5 cannot parse is
  quietly searched as a phrase.
- **One row per conversation that matched**, ranked by how well it matched,
  then by last activity. A row knows: when, account, provider, title, folder,
  **how many of its turns matched**, its best snippet (about 12 words around
  the match, the matched words marked), and whether that turn was said by you
  or by the agent.
- **The match in context** for the conversation under the cursor: the
  matching turn plus the turn before and after it, matched words highlighted,
  and a way through its other matches (`n` / `N`, `match 2 of 5`).
- **Progress while the index fills.** The first time, the backfill reads every
  conversation in the background, newest first (965 on the workstation). Until
  it finishes, results are partial and the screen has to say so
  (`searched 612 of 965 conversations`), so an empty list is not read as "never
  said".
- **The actions from a hit**: `↵` resumes it under the account that recorded
  it, in its folder (the footer names the folder, as today). `H` hands it off,
  skipping the wizard's first step. `y` copies `kizuna <profile> resume <id>`.

## Tensions the design has to resolve

1. **One picker, two kinds of search.** The picker's `/` filters rows by what a
   row is. The new search matches what was said, and it returns different rows
   in a different order (by relevance, not recency). If both live in one box,
   the two must not be mistaken for each other: a user who types a word and
   sees nothing has to know which of the two said "nothing".
2. **The pane's job changes.** Today the pane shows how a conversation
   *ended*, because that is what tells two sessions apart. In search mode it
   should show *where the match is*, which may be at the very start of a long
   conversation. Should it show only the match, the match and the ending, or
   switch between them?
3. **The snippet's home.** At 62 columns the list has no room for a snippet on
   the row. The pane has room, but the pane folds away below 83 columns. And
   ranking ten conversations by one snippet each is easier when you can see
   them all at once, not one at a time in the pane.
4. **Relevance against recency.** The plain picker is newest first, and the
   `WHEN` column carries that order. Ranked results scramble the dates. Does
   the list say it is ranked, offer a toggle to newest first, or both?

## What the design needs to decide

- **How the mode is entered and shown.** Proposed: a palette entry ("search
  conversations", opening in all-accounts mode) and `ctrl+f` inside the `R` /
  `H` picker. Is it the same box with a third heading chip (`titles` / `what
  was said`), a different box title (`search` instead of `resume`), or a
  separate box? Does the query field always hold focus (so typing searches),
  or does `/` still open it?
- **The row.** Which columns stay: `when`, account, title, folder, match count?
  Does the row grow a second line for the snippet (halving how many fit), does
  the snippet replace the folder, or does it live only in the pane?
- **The pane.** The header stays (title, when and folder, account and id). The
  body: how many turns around the match, how the match is marked (reverse
  video, a colour, underline, brackets in the text), where `match 2 of 5`
  sits, and whether the conversation's ending is still shown below.
- **States.** Typing with no query yet, searching (the query moved on, results
  pending), results, no results with the index complete, no results with the
  backfill still running, and a backfill that failed for one source. Where the
  backfill count lives (under the list, like the headless count, or in the
  heading).
- **The other toggles.** `a` (this account / all) and `.` (headless) already
  exist and carry over. Are provider, repository, or "only what I said"
  (`--from user`) worth a key, or do they stay CLI flags?
- **The footer.** Candidates: `esc`, `↑↓`, `↵ resume in <folder>`, `H hand
  off`, `y copy command`, `n/N next match`, `ctrl+f` back to titles. That is
  more than the picker's four. Which stay in the footer and which go to the
  `space` palette?
- **The narrow fold.** Below 83 columns the preview goes. What does a hit keep:
  a snippet line under each row, or nothing but the title?

## Constraints the design must hold

- A full-screen Bubble Tea TUI with **no mouse**. Every action is a key.
- The box is at most **140 wide**, centred over the board, which fades behind
  it. Every cell is drawn on the canvas (`#0B0B0E`, text `#E5E7EB`), and every
  tone is chosen against it. The vocabulary is the existing one: faint
  uppercase section labels, the `▌` cursor and the tinted selected row, the
  heading chip for the current scope, spaced key hints, provider colours on
  account names.
- **Both panes are drawn at the frame's full height**, whatever is under the
  cursor, so moving the cursor never resizes the box. That stays.
- **Nothing is drawn as known before it is read.** The pane already says
  `reading the transcript…` until the read lands, and the backfill state must
  follow the same rule.
- **What is searched is what was said**, never tool calls or tool output. The
  snippet is text a person or the agent wrote, and the design should not
  suggest otherwise (for example by showing a match as if it were a log line).
- **Text may be Japanese.** Snippets must be cut by display width, not by
  character count; the code's `truncate` already does this.
- A handed-off conversation keeps its `→ account` marker in the row.
- Resume and handoff rules do not change: a hit is reopened under the account
  that recorded it, in the folder it ran in, and a moved or deleted folder is
  reported, never swapped for another.
- Carry **sample data** in the handoff (a query, eight or so hits across at
  least three accounts and two providers, one with several matches, one
  Japanese snippet), so a conformance test can pin the frame cell for cell,
  as `TestBoardMatchesTheDesignFrame` does for the board.

## Adjacent, not part of this

- **`kizuna search --print / --json`** and the **`search_sessions` MCP tool**
  are in the proposal and need no design.
- **The handoff wizard's later steps** are unchanged; `H` from a hit lands on
  "going to" exactly as `H` from the resume picker does today.
- **Semantic search** is out. This is word matching over an FTS5 index.
