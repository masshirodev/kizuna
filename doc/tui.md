# The TUI

Running `ai` with no arguments opens the cockpit. Back to the [README](../README.md).

The layout is the **gauge board** — direction 2a of the `ai-session Redesign`
Claude Design handoff, carried through every screen in its third turn. Every box
uses one vocabulary: an accent border, an uppercase accent title with the thing
it acts on beside it, uppercase faint section labels, `▌` and a tinted row for
the cursor, thin `━` gauges, and key hints spaced rather than dotted, with the
way out always at the right.

## The board

One frame: a title bar, one table, a status line, and a key bar.

**Every cell is drawn on the design's canvas** — `#0B0B0E` with `#E5E7EB` text
in the dark theme — rather than on whatever the terminal's own background is.
Every other tone was chosen against that canvas, and left to the terminal a
lighter or tinted theme washes the faint ones out. Each styled span closes its
colours when it ends, so the canvas is re-opened after every one of them; a span
with its own background (the selection tint, a chip, the badge) still wins
inside itself.

**The board is at most 146 × 40, the frame the design is drawn at, and is
centred in a larger terminal.** Past that width it would only spread the same
columns further apart (the gauges stop growing at 32 cells), and past that
height the status line and the keys would be stranded far below the table.
Boxes are not capped by it: they size against the whole terminal, since a picker
or a conversation does turn more room into more to read.

- **The title bar** names the account with the most headroom and the one with
  the least — `most headroom claude-max 78% · lowest claude-personal 7%` —
  with the launch folder and any pending update (`↑ 3 behind main  U`) on the
  right. The folder and the update are dropped rather than clipped when the
  terminal is too narrow for them.
- **The table** is every account, one row each: name, provider, a gauge and a
  figure for what is left of the 5-hour and 7-day windows with when each rolls
  over, whether it can log in, and how many instances it is running (`▶ 2`).
- **The selected account expands in place** rather than opening a detail
  column: under its row, how it launches (command, where the CLI is, model,
  status line, note), what it is running, a 24-hour activity sparkline in its
  provider's colour, and its last four conversations with `R`/`H`/`h` beside
  them. Too narrow for the two side by side, the recent list goes under what is
  running.
- **Every other account's live instances sit nested under its row**
  (`└ ▶ Migrate billing webhooks   PID 39021 · ~/work/billing · 3h05m`), so
  what is running is read beside the account running it.
- **The status line** is what the old log panel shrank to: the last thing that
  happened, and at what time. A box's own errors are shown in the box instead.
- **The key bar** carries five keys — `↵ run`, `R resume`, `H hand off`,
  `p args`, `/ find` — and `space all actions`, `? keys` on the right.

### Sorted by headroom

**The board is ranked, not alphabetical.** Accounts whose CLI keeps a quota
cache this launcher reads (Codex and Claude Code) come first, ordered by
whichever of their two windows runs out first, most headroom at the top; an
account whose quota is unknown sorts to the bottom of that group. The others —
Antigravity, OpenCode, DeepSeek — are listed apart under `NO LOCAL QUOTA CACHE`
with `· not reported` in place of gauges, because they have no number to rank
by and sorting them last would read as if they were empty.

**The cursor belongs to the account, not to the row.** A quota refresh can
reorder every row; the selection follows the account it was on. `r` keeps the
old figures on screen until the new ones land, rather than blanking them and
reshuffling the board twice.

### Narrow and short terminals

The gauges take what the fixed columns leave, up to 32 cells, and are dropped —
figures kept — once they would be shorter than 6. Past that the reset times go
first, then the live count (the expanded row repeats it), then auth, then the
name column narrows. A board taller than the frame loses the expansion's second
half (running, activity, recent) before it loses rows, and then scrolls to keep
the cursor in view.

Everything else — the profile editor, the folder and argument prompts, the
delete confirmation, the clone and share boxes, the pickers, the handoff wizard
and the palette — opens as a box over the board. The board behind a box fades to
one flat tone, with a fainter tint left on the rows that were selected: every
key is the box's until it closes, so the board is context rather than something
to read.

## What a row says

Auth is the column before `LIVE`:

- `● ok` — the provider's credential file exists in the isolated state
  directory, so `ai run` can use it.
- `● key` — a `deepseek` profile with `DEEPSEEK_API_KEY` exported, or an
  Antigravity profile with `modelProvider: "gemini"` and `GEMINI_API_KEY`.
  These authenticate from the environment rather than a stored OAuth file.
- `○ login` — no credentials yet; press `l` to log in.
- `· ?` — the provider has no known credential location, so the launcher does
  not guess.

The check only tests whether the file exists, and reads the API key variable
only to see whether it is empty; `ai-session` still never opens or prints
credentials. A profile whose token has expired therefore keeps showing `● ok`
until the official CLI asks you to log in again.

The expanded row's launch line shows where the profile's command resolves on
`PATH`, or `not installed — press i`. See
[Install the provider CLIs](running.md#install-the-provider-clis).

It names the model the profile will start with, read from that CLI's own
settings inside the isolated directory, and leaves it out when the provider has
no discoverable answer yet:

| Provider | Source |
| -------- | ------ |
| Codex | `codex/config.toml`, top-level `model` |
| Claude Code | `claude/settings.json`, `model` |
| OpenCode | `state/opencode/model.json`, most recent entry; falls back to `model` in `config/opencode/opencode.json[c]` |

OpenCode's state file is preferred because it holds the model last chosen in the
TUI, which is what OpenCode restores on the next start; the config default only
applies before anything has been picked.

The gauges show the remaining five-hour and weekly quotas reported by each
account's own local CLI cache, along with when each window rolls over. Codex is
read from the newest rate-limit events in that profile's session logs; Claude
Code is read from its cached usage utilization. `…` means the caches have not
been read yet; `—` means a window is expired or unavailable. This is
intentionally profile-local and does not use OpenUsage, so multiple accounts for
the same provider stay separate. Credential files are never opened.

The TUI starts in the directory where you ran `ai`. Press `c` to set another
launch folder without leaving the TUI; relative paths resolve from the current
launch folder and `~` is supported. The chosen folder applies to subsequent
CLI launches in that TUI session and does not change the parent shell's
directory.

## Activity and recent sessions

The sparkline counts sessions touched per hour over the last day, from the
timestamps on the profile's own transcripts. It is deliberately not labelled as
quota: no provider records what a limit cost at a given hour, so the histogram
measures the one thing that is actually on disk. `RECENT` reads the
same transcripts for what each conversation was about, skipping the preamble
both CLIs write before the first thing you actually typed. Two shapes are
handled, because the CLIs use both: a block sent *ahead* of the prompt is
skipped whole (environment dumps, harness reminders, Codex's `AGENTS.md`
instruction dump), while a block wrapped *around* the prompt is unwrapped
instead — Codex's IDE integration leads with your open tabs and labels the part
you typed, so skipping the message would lose the prompt with it.

**The list is ordered by the last message, not by when each session began.** A
conversation opened three days ago and answered a minute ago belongs at the top,
and dating the row by its start buried it under everything started since. For
Claude and Codex the file is appended to on every turn, so its modification time
is the last thing said; OpenCode keeps the same fact in its `time_updated`
column. A row therefore shows when the session was last active, and a store
without that column (an older OpenCode) still lists, dated by when it began.

**Claude's subagent transcripts are not listed.** They live under a `subagents/`
folder beside their parent conversation and are written every time a subagent
runs, so a session that delegated leaves a dozen recent-looking files behind.
They are not conversations that can be resumed, and left in they were most of
the list after any session that used them.

## Keys

Use the arrow keys or `j`/`k` to select a profile. On the key bar:

- Enter runs the selected profile.
- `R` resumes one of its recorded conversations, in the folder it ran in.
- `H` hands a conversation to another account, for when one has run out of
  quota — see [Handing a session to another account](sessions.md#handing-a-session-to-another-account).
- `p` runs it with extra arguments typed at the prompt, or with a recent or
  pinned set picked from under it.
- `/` filters the board by name or provider. Enter keeps the filter and hands
  the keys back, so a search is a way to reach one account among many rather
  than a mode to dismiss before acting; Escape clears it.
- `space` or `?` opens the palette.
- `q` or Escape quits.

### The palette

`space` (or `?`) opens every other action as a filterable palette, grouped by
what it acts on — `CONVERSATION`, `PROFILE`, `PROVIDER CLI`, `AI-SESSION` —
with live context beside each: where `↵` would run, how many conversations are
recorded, how many instances are running, the 5h figure `H` would hand off
from, whether the profile is logged in, where its CLI is, whether auto-swap is
on, and how far behind `main` this build is. Typing narrows the list by what the
actions do; typing an action's own key puts it under the cursor, so `c` then
Enter does what `c` does on the board. `↑`/`↓` move and Enter runs the
highlighted action. The palette folds to one column rather than clipping when
the terminal is too narrow for two.

The keys it lists, all of which also work straight from the board:

| Group | Key | Action |
| ----- | --- | ------ |
| Conversation | `↵` `p` `R` `h` `H` | run here · run with arguments · resume · open a live instance (the original keeps running) · hand off |
| Profile | `a` `e` `C` `x` `/` | add · edit · clone its setup (no credentials) · delete after confirmation · find |
| Provider CLI | `l` `i` `u` `K` `m` `s` | log in · install the CLI (after showing the command) · update it · stop an instance (Enter stops one, `a`/`y` all) · install MCP servers · install skills |
| ai-session | `c` `A` `r` `U` `q` | change launch folder · auto-swap on handoff · refresh quotas and updates · update ai-session (after showing the checkout and steps) · quit |

## The boxes

**The resume picker (`R`)** is two panes: conversations on the left — when,
what, where, plus the account once it lists every one — and the conversation
under the cursor on the right, read from its end. The heading carries the scope
as a toggle (`this account` / `all accounts`, `a` flips it) and the search
(`/`, by title, folder, account, or conversation id). The footer names the
folder Enter will reopen the row in, and `H` hands that row off instead. See
[The picker is two panes](sessions.md#the-picker-is-two-panes).

**The handoff (`H`)** is a four-step wizard — see
[Handing a session to another account](sessions.md#handing-a-session-to-another-account).

**The profile editor (`a`, `e`)** is one field per row, the active one tinted
and carrying the cursor, and the provider as a row of chips moved with `←`/`→`.
A command still set to the old provider's default follows the chip; one typed by
hand is left alone. Under the fields, `LAUNCHES AS` shows the real command line,
the isolated directory the provider is pointed at (`CLAUDE_CONFIG_DIR`,
`CODEX_HOME`, the XDG triple, …), and how the running account will name itself.
Tab moves between fields and Enter saves from any of them.

**The MCP / skill box (`m`, `s`)** is one box: the profiles that could lend on
the left with how many each has, the chosen one's servers or skills on the right,
ticked with space. `←`/`→` move between the panes, moving through the lenders
reads each as the cursor lands on it, and `tab` switches between MCP servers
and skills. A row the destination already has shows as `installed` with no box
beside it; it can still be ticked on purpose, which is how a definition is
replaced, but `a` (tick all new) passes over it. Profiles with nothing to lend
are named under `nothing to lend`. For MCP servers copied between two different
CLIs, `TRANSLATED ON THE WAY` names the file the servers will be written to and,
for each ticked server, what the copy rewrites — `${VAR}` respelled as
`{env:VAR}` for OpenCode, or for Codex a bearer header moved to
`bearer_token_env_var`, a header read from the environment moved to
`env_http_headers`, a variable passed through listed in `env_vars`. The notes are
computed from the same splits the writers use, so the box cannot describe a
rewrite the copy then does not make.

## Recent and pinned arguments

The `p` prompt puts the field on top and, under it, the whole command it will
produce — the profile's stored defaults first, then what was typed, because that
is the order they are passed in. Under that it lists what it has been given
before: `PINNED` sets first, marked `◆`, in the order they were pinned, then the last five sets run under
`RECENT`, newest first. Only a launch that actually started is recorded, and
only when it carried arguments — a plain run is what Enter is for.

The list is shared by every profile and every provider, which is the point:
the `--model` you gave one Claude account is one keypress away on the next.
It is also the risk, since a flag one CLI understands is one another rejects,
so each row names the profile it last ran on (in its provider's colour) and
when, and a set last run on a different CLI from the selected profile's is
marked `other CLI`. Running a set that
is already listed moves it to the front rather than listing it twice, and
updates which account it names.

- `↓` and `↑` move through the rows. A picked row is copied into the field, so
  Enter runs it as it is and typing edits it first. Editing detaches it from the
  row it came from. Going back up past the first row restores what you had
  typed before.
- `ctrl-p` pins the highlighted row, or unpins it if it is already pinned. With
  no row highlighted it pins whatever is typed in the field, without running
  it; such a set reads `never run` until it has been.

A pin stays until it is unpinned, and running it updates it in place rather
than listing it again under `RECENT`, so it does not use up one of the five.
Unpinning puts the set back at the front of `RECENT`, where the next five runs
age it out like any other.

The history lives in `~/.config/ai/arguments.json`, created `0600` because a
set can carry a prompt as well as flags. Every change rereads the file first,
so two open TUIs do not undo each other's pins, and a file that cannot be
parsed is reported and left untouched rather than replaced with an empty
history.

## Box sizing

**A box is sized to the terminal, not to what it happens to be showing.** Every
box grows in width with the window up to a ceiling of its own, border included — the resume
picker at 140, the handoff wizard and the share box at 124, the palette at 120,
the arguments prompt at 104, the editor at 92, and the boxes that ask one
question at 88 — and gives back six columns to the board behind it. The width a
wide terminal adds to a picker goes to the preview rather than the list: a
session row is a time, a title and a folder, and beyond the width those need,
more columns only pad the gaps between them, while the preview turns every
extra column into a sentence that fits on one line.

Height is the frame's, and it does not move. The pickers, the handoff steps and
the share box are drawn at the full height the frame allows and padded out to
it, however much the row under the cursor has to say. Sizing them to their
content instead meant a different box for every row: stepping from a long
conversation to a two-message one collapsed the list beside it as well, and the
row being aimed at moved while it was being aimed at. A status line inside one
of these boxes comes out of that height rather than being added to it, so a
message about the last keypress does not push the box two rows taller either.
The boxes that ask one question are still as tall as the question.

`TestBoardMatchesTheDesignFrame` pins the board against the handoff: it draws
the board with the mock's own sample data and checks the rows cell for cell, so
a later pass that rounds a hand-set width (the 22- and 13-column account
columns, the 32-cell gauges, the spaced key hints) fails rather than quietly
redrawing the screen.

## Where it departs from the handoff

The handoff is a mock drawn against sample data; where it and the repository
disagree, the repository won, and these are the places:

- **The palette filters first.** The mock says "type to filter, or just press
  the key", but nearly every letter is a key, so a palette that ran a key on
  its first keystroke could never be filtered. Typing always filters; typing an
  action's exact key puts that action under the cursor, and Enter runs it.
- **Already-installed MCP servers and skills can still be ticked.** The mock
  says they "can't be ticked". The share box has always let one be ticked on
  purpose, because that is the only way to replace a definition from the TUI;
  it shows no box on those rows and `a` still passes over them.
- **The going-to step's `WHAT MOVES` carries no size.** The mock prints "a 5 KB
  brief (~1.3k tokens)" there, but the brief is only written on the next step,
  so no size is known yet. The brief step shows the real size and a rough token
  count (bytes ÷ 4) beside the file's path.
- **The brief step names the conversation.** The mock's route line has the two
  accounts and the folder only. With auto-swap on the brief step is the only
  one shown, so it also carries the title — the question left is whether this
  is the work that was meant to move.
- **Resume rows leave out the conversation id.** The mock's rows are when, what,
  where; the id is in the preview in full and the search still matches it.
- **Recent-argument rows are dated like every other list** (`14:02`, `yest.`),
  not "2h ago" as the mock draws one of them.
- **`LAUNCHES AS` lists every isolation variable.** The mock shows only
  `CLAUDE_CONFIG_DIR`; an OpenCode profile is isolated by three XDG variables
  and an Antigravity one by three of its own, and all of them are shown.
- **The board has no `FOLDER` / `SWAP` footer.** The folder moved to the title
  bar, and auto-swap is shown where it can be changed — the handoff wizard's
  footer and the palette.

## What it does not do yet

- **No light-theme review.** The three new tones (`colorTrack`, `colorField`,
  `colorGhost`) have light values, but only the dark theme was checked against
  the mock.
- **The palette does not remember a filter** between openings, and has no
  fuzzy matching — a filter is a substring of what an action does.
- **The share box reads a lender's config on every cursor move** through the
  lenders. It is one small file per move, but it is disk on a keypress.
