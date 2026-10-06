# Resuming, hijacking, and handing off

Getting back into a conversation — a stopped one, a running one, or one that
has to move to another account. Back to the [README](../README.md).

## Resuming

`R` offers the conversations the selected account's `RECENT` list has already
read off disk, and
reopens the chosen one by id in the folder it ran in. Both halves matter: the id
names the conversation, and every provider looks for that id under the folder it
belongs to, so resuming from anywhere else reaches a different conversation or
none at all. A recorded folder that has since been moved or deleted is reported
rather than quietly swapped for the current launch folder.

## The picker is two panes

`R` and `H` open the same list, and both put the conversation under the cursor
in a pane beside it — the list on the left, how the session ended on the right,
the way a file picker shows the file it is hovering. A title says what a
conversation was called; it does not say whether it is the one being looked for,
and two sessions in the same folder on the same afternoon are told apart by what
was said in them.

**The pane shows the end of the conversation, not its beginning.** The opening of
a session is often a pasted brief or a batch prompt that names the work only
indirectly, while what was last being done is what tells one session from
another now. Reading the tail has to be bounded the other way, though: a
six-megabyte transcript cannot be read from the top on every cursor move, so the
file is read backwards a window at a time until enough of the last things said
have been collected, and the window widens only when the tail holds too little.
A conversation whose earlier half is off screen says so with a `…` above the
first turn shown, rather than starting mid-sentence as though that were the
start.

**No single turn may spend the whole pane on itself.** The read also caps each
turn at a few rows, because one pasted stack trace or brief is a turn too —
uncapped it filled every row, so the pane showed one message instead of a
conversation. A turn the pane cuts ends in an ellipsis.

Under the title the pane names the account and the **conversation id** in full —
the id `ai <profile> resume <id>` takes, which was previously nowhere to read —
and dates the session by its last activity, the same fact the list is ordered by.

`/` searches the list by title, folder, account, or id, and `a` switches between
the selected account's sessions and every account's. The search matches what a
row does not show as well as what it does, so a session can be found by a word
from its title, the folder it ran in, or the id of a conversation whose row has
scrolled away. Enter keeps the filter and hands the keys back, matching the
profile list's own search; Escape clears it. In the all-profiles mode each row
also names the account that recorded it, and it is reopened under that account —
a session id only exists inside the isolated state directory that recorded it.

**Headless runs are hidden until `.` shows them.** A conversation nobody typed
into (`opencode run`, `claude -p`, `codex exec`) is still listed, but only once
`.` is pressed in a picker; the count under the list says how many are hidden,
and the expanded account's `RECENT` rows follow the same switch. A wave night
leaves dozens of them, and they used to push every conversation a person had
off the list. Each kind is limited separately, so a list of runs can never
crowd the conversations out even while they are hidden. How each provider marks
one:

| Provider | Headless when |
| -------- | ------------- |
| OpenCode | the session's permission rules deny the `question` tool, which `opencode run` writes because nobody is there to answer (checked against 61 interactive and 62 headless sessions) |
| Claude Code | the transcript's `entrypoint` is not `cli` (`claude -p` writes `sdk-cli`) |
| Codex | the rollout's originator names `exec`; not verified on a machine with Codex rollouts |

The read happens off the keypress, so moving the cursor never waits on the disk;
the pane says `reading the transcript…` until it lands. A row already read is
shown from a cache that lives as long as the picker, so moving up and down a
list re-reads nothing. A read that lands after the cursor has moved on is filed
against the row it belongs to rather than shown beside another one.

Below the width where both halves can be read the preview folds away and the
picker is a plain list, the same way the cockpit folds a column rather than
squeezing it. A list longer than the pane scrolls to keep the row the keys act
on in view.

The pane is filled from the same conversations the handoff brief is built from.
OpenCode's pane is read from its store and Antigravity's from its
per-conversation store, not a transcript file, and both are filled the same way.

This is a wider offer than the provider's own resume flow, which only ever sees
the folder it was started in. The panel has read every folder the account has
worked in, so a conversation from another project is one keypress away instead
of a `cd` away.

One account cannot resume another's conversation: a session id only exists
inside the isolated state directory that recorded it, so the same id under a
different profile finds nothing. Switching the picker to all profiles does not
cross that line — it lists other accounts' sessions and reopens each one under
the account that recorded it, which is the one the row was read from.

## The session index

Every list above reads `~/.config/ai/sessions.db`, one table of every profile's
conversations. A refresh stats each source and re-reads only the ones whose
fingerprint moved (a transcript's size and modification time, an OpenCode
session's `time_updated`), so the first build on the workstation took 335 ms
for 448 conversations across eight profiles and every refresh after it 20 ms.
A conversation read whole (a handoff, an OpenCode preview) is cached there too,
against the same fingerprint, and the next preview or handoff of it is one
query. The index is a cache, never the record: deleting it costs one slower
refresh, and when it cannot be opened the lists fall back to reading the
sources directly. Sessions that live only in a running OpenCode instance are
not indexed; they are read from that instance's store until it merges. See
[session-store.md](session-store.md).

## What is read, per provider

**What `RECENT SESSIONS` reads is per provider.** Codex and Claude Code keep a
transcript file per conversation, which is what gets parsed for a title.
OpenCode keeps its own SQLite database (`opencode.db`) with title, folder, and
timestamp as plain columns — no parsing needed, and resuming picks the exact
conversation by id (`opencode --session <id>`), the same as Codex and Claude.
The conversation itself is read out of that store too, for the handoff brief and
the preview pane: `message` carries the role and `part` the prose, and only the
`text` parts survive the read (tool calls, reasoning and step markers are not
something anyone said). See [Handing a session to another account](#handing-a-session-to-another-account).

**A row is titled with the conversation's own name where there is one.** Claude
Code names a conversation itself a few turns in and writes that name into the
transcript (`ai-title`, and `agent-name` for an agent session); that is the name
its own UI shows, so it is the name the row carries. Titling the row with the
opening sentence instead left the launcher and the CLI disagreeing about what
the same session was called. The opening sentence is still the fallback, for a
session too short to have been named and for Codex, which records no name at
all.
Antigravity is listed from its `conversation_summaries.db`, which carries the
title, the workspace and the last-modified time as plain columns; its turns are
read out of each conversation's own store (see
[Reading Antigravity](#reading-antigravity)). A stopped conversation is
reopened by id in its workspace with `agy --conversation <id>`, like any other
row.

With nothing recorded — an account that has not run yet — `R` falls back to the
provider's own resume flow in the current launch folder:

| Provider | Command |
| -------- | ------- |
| Codex | `codex resume` (session picker) |
| Claude Code | `claude --resume` (session picker) |
| Antigravity | `agy --continue` |
| OpenCode | `opencode --continue` |

Antigravity and OpenCode continue the last session for the current workspace
instead of opening a picker.

## From the command line

The same picker is one command away without opening the cockpit first:

```sh
ai codex-work resume            # the recent-sessions modal for that profile
ai codex-work resume ses_abc    # one conversation, reopened in its folder
ai run codex-work resume        # the run spelling works too
```

With nothing recorded — an account that has not run yet — `resume` falls back to the provider's own
flow above, which is the same offer `R` makes from inside the TUI. A
`resume` followed by anything else is not the wrapper at all: a prompt that
happens to start with the word keeps passing through to the provider
untouched.

## Hijacking a running instance

`h` chooses from processes rather than from transcripts. It reads the running
instances the launcher already tracks, resolves the session each one has open,
and reopens exactly that session in the folder the instance was launched from —
which is what makes it the key for a conversation that is still going, where `R`
is the key for one that has stopped. Session titles come from the provider
itself and are best effort:

| Provider | Source |
| -------- | ------ |
| Codex | the newest session log recorded for that folder |
| Claude Code | `claude agents --json`, matched on the recorded PID, then titled from that session's transcript |
| Antigravity | not available; the instance is listed without a title |
| OpenCode | not available; the instance is listed without a title |

For Claude Code, `claude agents` gives the session id and a slug such as
`ranma-ff`, but no title. The slug comes from the folder name, so two sessions
in one repo look almost the same. The picker takes the session id, opens that
conversation's transcript, and shows the title Claude Code gave it (its
`ai-title` record), falling back to the opening message. The slug is shown dim
after the title, because it is how the status line and `claude agents` name the
session. When the row is too narrow for both, the slug is dropped. When no
transcript is found, the slug is the title.

Hijacking an Antigravity profile is refused for the same reason a
second launch is: its credential store is exclusive while the first process is
running. Hijacking a live OpenCode instance is refused as well — reopening its
conversation elsewhere would put two writers on one session — while resuming a
merged OpenCode session works like any other.

## Handing a session to another account

`H` is for the case where one CLI's limit is spent and the work is not finished.
It reads the outgoing conversation, reduces it to a brief, and starts another
account on that brief in the same folder.

It is a four-step wizard, with a stepper across the top of the box saying where
you are — `leaving`, `going to`, `brief`, `open`. `←` goes back a step, and
`esc` leaves.

1. **Leaving** — which session is moving, chosen from the same two-pane picker
   `R` uses (see [The picker is two panes](#the-picker-is-two-panes)), so the
   conversation about to be reduced to a brief is readable before it is — and
   searchable, and switchable to every account. `H` pressed inside the resume
   picker hands off the row under the cursor and skips this step: the row you
   would have resumed is the row you are handing over. A handoff started from
   the all-accounts mode is built from the account that recorded the chosen
   row, and offered to the others.
2. **Going to** — the leaving account and how its conversation ended on the
   left; on the right the destinations, ranked, each with a gauge for the window
   that runs out first and a note saying which window that is (`limited by 7d`,
   or `5h nearly out` when it is nearly spent). An account whose CLI cannot be
   opened on a prompt is still a destination, marked `by hand` in its row; it
   takes the brief by clipboard instead (see below). A `WHAT MOVES` line says
   what goes and what stays.
3. **Brief** — the route with both accounts' figures, the conversation's title,
   and the brief as it was written beside how the conversation ended: what was
   asked (the first three, then a count), where it was left, and the repository
   (branch, files changed, `+`/`−` lines, the first few changed paths). The
   file's size and a rough token count sit beside its path.
4. **Open** — `↵` on the brief step hands the brief over: it launches the
   destination on it, or, when that CLI cannot be opened on a prompt, copies
   the brief's text to the terminal's clipboard and opens that account in the
   folder, so there is somewhere to paste it into. `v` reads the full brief in
   `$PAGER` (default `less`) first, which is also the way to select and copy it
   by hand.

Nothing is copied into either CLI's state directory. The conversation stays
where it was recorded; what moves is a markdown file under
`~/.config/ai/handoffs/`.

The brief has three parts:

1. **What was asked** — every request you typed, in order, with the CLIs' own
   injected blocks stripped and the half-written copy of an interrupted message
   dropped.
2. **Where the previous agent left off** — its last few *substantial* messages.
   Substance is measured by length, which is crude but free: a model's final
   messages are usually its shortest, so taking the tail by position picks the
   line between two tool calls rather than a conclusion.
3. **The repository, as of the handoff** — branch, uncommitted changes, diff
   against `HEAD`, recent commits. This is read from git at handoff time rather
   than reconstructed from the transcript, because what a transcript records
   depends on how the previous agent happened to hold its tools. On one session
   here, recovering the touched files from tool arguments found two of the six
   that were actually edited, because the work went through shell heredocs and
   no tool argument ever named a path. Git does not have that problem.

The full conversation is named at the end, not pasted in. That is the
difference from handing over the profile folder and saying "continue": the next
agent *can* read it, but does not have to. On this machine a 1140 KB transcript
(~292k tokens) reduced to a 5 KB brief (~1.3k tokens). For Claude and Codex it
is named as a path to grep; for OpenCode, which has no transcript file, it is
named as the store and session id with a read-only `sqlite3` command that prints
its rows.

There is no model anywhere in that path, and that is forced rather than chosen:
the premise is that you are out of quota, so the outgoing CLI cannot summarise
itself, and asking the incoming one to summarise means reading the transcript —
the cost being avoided. Extraction is mechanical. Press `e` on the brief step
to edit the brief before it goes; you know what mattered.

The brief step shows **how the conversation ended** beside the brief — the last
turns as they were actually said, under `HOW IT ENDED`, with the same speaker
labels the resume picker uses. The brief's own opening is not shown there: it is
the same four lines every time and answers a different question than this step
asks. The question here is whether this is the work you
meant to move, and the last thing said answers it at a glance. A conversation
longer than the box says so with a `…` above what is shown, rather than starting
mid-sentence as though that were the beginning.

Destinations are ranked by the quota window that runs out soonest, since a
weekly allowance with room is no help at the moment the five-hour one is spent.
An account whose quota is unknown sorts below a measured one.

A handoff is recorded in `~/.config/ai/lineage.json`, and the recent lists —
under the expanded account and in the pickers — mark the source row with
`→ <account>`. The chain reads forwards only: a
handoff is a baton pass, not a fork, so there is never a newer branch on the
other side to reconcile.

The conversation a handoff became is recorded too, as `target_session_id`, so
the expanded account marks it `← <account>`. It cannot be known at launch, so
the board fills it in later, from the conversation whose first prompt is the
handoff's — the path of the brief, or a pasted brief's heading. See
[Where a conversation came from](board-expansion.md#where-a-conversation-came-from).

## Auto-swap

`A` toggles auto-swap, which is **off by default** and persisted in
`profiles.json` as `settings.auto_swap`.

With it on, `H` skips the destination question and sends the work to whichever
account has the most quota left. It does **not** skip two other things:

- **Which session is leaving is always asked.** That is the one thing the tool
  cannot infer safely.
- **The brief is still shown before anything launches.** Writing a file and
  starting a process are different promises, and with auto-swap on this is the
  frame where you find out where the work went.

Auto-swap does not watch a running session and switch mid-flight. While a CLI
owns the terminal the launcher is not running, so it has nothing to watch with.

`H` can hand off *from* Claude Code, Codex, OpenCode and Antigravity: the first
two are read from a transcript file, OpenCode from its SQLite store, and
Antigravity from its per-conversation store. It can hand off *to* any
other account: Claude Code and Codex are opened on the brief directly, because
their opening-prompt syntax is known, while every other provider — OpenCode,
Antigravity — takes it by hand: the brief's text goes on the terminal's
clipboard and that account is opened in the folder, so the paste has somewhere
to land. The brief step's key line says which of the two it will do.

### Reading OpenCode

The OpenCode reader is the same reduction the other two get, from a different
shape of store. `message` carries the role and `part` the prose; only `text`
parts survive, and the store is filtered by `session_id` so a large database is
queried by session rather than read whole. The rows are parsed in Go rather than
with SQL's JSON functions, so the reader does not depend on how the SQLite build
was compiled.

A resumed OpenCode session is carried into its instance first: every instance
starts on an empty store, and `opencode --session` only finds sessions in the
store it opens. The launcher exports the session from the profile's store and
imports it into the instance, from the session's own folder, before opencode
starts (see [session-store.md](session-store.md)).

With concurrent instances, a session may live in the profile's merged store, in a
live instance's private copy, or both. The reader picks the copy whose session row
was updated last, so a conversation still being written is read where it is
rather than from the copy it has not merged into.

**Where this departs from the brief.** The brief allowed a pointer-only fallback
tier for a session that could not be read; the full reader landed instead, so only
one kind of brief is produced. The pointer text survives in the brief's own "if
you need more" section as the `sqlite3` command. The preview pane reads the
session's text parts and takes the tail from the end rather than scanning
backwards through a file, which is bounded by the session and needs no windowing.

**What it does not do yet.** The brief still reads OpenCode's store directly
rather than through `opencode export <id>`. Export once answered `Session not
found` here for a session present in the profile store; run with the profile's
own environment it works, and it is what a resume uses to carry a session into
an instance (see [session-store.md](session-store.md)), but the reader predates
that and has no reason to change. A session that lives only in a stale,
unmerged instance directory is still not read — the same copies the recent list
refuses to offer, for the same reason.

### Reading Antigravity

Antigravity keeps one SQLite store per conversation
(`~/.gemini/antigravity-cli/conversations/<id>.db` under the profile's private
home) and a `conversation_summaries.db` beside them. The summaries hold what a
list needs as plain columns, so listing decodes nothing. A conversation with no
workspace was a prompt a program handed the CLI (on the workstation, six calls
from a reading tool), so it is headless. Subagent conversations
(`nesting_depth` above 0) are not listed.

The turns are protobuf in the `steps` table, with no published schema. The field
paths were read off the workstation's own conversations and hold across all of
them:

| Step type | Field | What it is | Read |
| --------- | ----- | ---------- | ---- |
| 14 | `19.2` | what the user typed | yes |
| 15 | `20.1` | the model's reply (`20.8` repeats it) | yes |
| 15 | `20.3` | its thinking | no |
| 15 | `20.7.3` | a tool call's arguments | no |
| first step's metadata | `1.1` | when the conversation began (seconds) | yes, once |

The start time is read when a conversation is indexed, not on a refresh, and the
turns are cached in the index once something reads them. A path that stops
matching after an Antigravity update reads as nothing said, never as the wrong
thing said. The brief points at the conversation's store and says it is
protobuf, rather than suggesting to grep it.

The clipboard is the terminal's own, reached with an OSC 52 escape rather than
by shelling out to `xclip`, `wl-copy` or `pbcopy`: which of those exists is a
fact about the machine, while the terminal is what the wizard is already talking
to, and a paste into the incoming CLI is a terminal gesture. The escape travels
back over SSH, and is wrapped for tmux when `$TMUX` is set. Because a terminal
may ignore an OSC 52 without saying so, the status line names the brief's path
beside the copy, and the file is written either way — it is the fallback, not a
second copy.
