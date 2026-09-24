# Resuming, hijacking, and handing off

Getting back into a conversation — a stopped one, a running one, or one that
has to move to another account. Back to the [README](../README.md).

## Resuming

`R` offers the conversations `RECENT SESSIONS` has already read off disk, and
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

The read happens off the keypress, so moving the cursor never waits on the disk;
the pane says `reading the transcript…` until it lands. A row already read is
shown from a cache that lives as long as the picker, so moving up and down a
list re-reads nothing. A read that lands after the cursor has moved on is filed
against the row it belongs to rather than shown beside another one.

Below the width where both halves can be read the preview folds away and the
picker is a plain list, the same way the cockpit folds a column rather than
squeezing it. A list longer than the pane scrolls to keep the row the keys act
on in view.

The pane is filled from the same transcripts the handoff brief is built from, so
it shows nothing for a provider whose conversations are not read back —
OpenCode and Antigravity say so instead of sitting on `reading…`.

This is a wider offer than the provider's own resume flow, which only ever sees
the folder it was started in. The panel has read every folder the account has
worked in, so a conversation from another project is one keypress away instead
of a `cd` away.

One account cannot resume another's conversation: a session id only exists
inside the isolated state directory that recorded it, so the same id under a
different profile finds nothing. Switching the picker to all profiles does not
cross that line — it lists other accounts' sessions and reopens each one under
the account that recorded it, which is the one the row was read from.

## What is read, per provider

**What `RECENT SESSIONS` reads is per provider.** Codex and Claude Code keep a
transcript file per conversation, which is what gets parsed for a title.
OpenCode keeps its own SQLite database (`opencode.db`) with title, folder, and
timestamp as plain columns — no parsing needed, and resuming picks the exact
conversation by id (`opencode --session <id>`), the same as Codex and Claude.

**A row is titled with the conversation's own name where there is one.** Claude
Code names a conversation itself a few turns in and writes that name into the
transcript (`ai-title`, and `agent-name` for an agent session); that is the name
its own UI shows, so it is the name the row carries. Titling the row with the
opening sentence instead left the launcher and the CLI disagreeing about what
the same session was called. The opening sentence is still the fallback, for a
session too short to have been named and for Codex, which records no name at
all.
Antigravity is not read yet: its conversation store is a per-conversation
SQLite database whose readable metadata carries ids but not a title, and the
title lives in a protobuf blob with no published schema — resuming a specific
Antigravity conversation already works from a *running* instance (see `h`
below), but there is no picker for a stopped one.

With nothing recorded — an account that has not run yet, or Antigravity, whose
transcripts this launcher does not read — `R` falls back to the provider's own
resume flow in the current launch folder:

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

With nothing recorded — an account that has not run yet, or Antigravity,
whose transcripts are not read — `resume` falls back to the provider's own
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
| Claude Code | `claude agents --json`, matched on the recorded PID |
| Antigravity | not available; the instance is listed without a title |
| OpenCode | not available; the instance is listed without a title |

Hijacking an Antigravity profile is refused for the same reason a
second launch is: its credential store is exclusive while the first process is
running. Hijacking a live OpenCode instance is refused as well — reopening its
conversation elsewhere would put two writers on one session — while resuming a
merged OpenCode session works like any other.

## Handing a session to another account

`H` is for the case where one CLI's limit is spent and the work is not finished.
It reads the outgoing conversation, reduces it to a brief, and starts another
account on that brief in the same folder.

Which session is leaving is chosen from the same two-pane picker `R` uses (see
[The picker is two panes](#the-picker-is-two-panes)), so the conversation about
to be reduced to a brief is readable before it is — and searchable, and
switchable to every account. A handoff started from the all-profiles mode is
built from the account that recorded the chosen row, and offered to the others.

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

The full transcript is named at the end as a path, not pasted in. That is the
difference from handing over the profile folder and saying "continue": the next
agent *can* read it, but does not have to. On this machine a 1140 KB transcript
(~292k tokens) reduced to a 5 KB brief (~1.3k tokens).

There is no model anywhere in that path, and that is forced rather than chosen:
the premise is that you are out of quota, so the outgoing CLI cannot summarise
itself, and asking the incoming one to summarise means reading the transcript —
the cost being avoided. Extraction is mechanical. Press `e` at the confirmation
to edit the brief before it goes; you know what mattered.

The confirmation itself shows **how the conversation ended** — the last turns as
they were actually said, under `HOW IT ENDED`, with the same speaker labels the
resume picker uses. That is deliberately not the head of the brief that was just
written, which opens with the same four lines every time and answers a different
question than this screen asks. The question here is whether this is the work you
meant to move, and the last thing said answers it at a glance. A conversation
longer than the box says so with a `…` above what is shown, rather than starting
mid-sentence as though that were the beginning.

Destinations are ranked by the quota window that runs out soonest, since a
weekly allowance with room is no help at the moment the five-hour one is spent.
An account whose quota is unknown sorts below a measured one.

A handoff is recorded in `~/.config/ai/lineage.json`, and `RECENT SESSIONS`
marks the source row with `→ <account>`. The chain reads forwards only: a
handoff is a baton pass, not a fork, so there is never a newer branch on the
other side to reconcile.

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

`H` can hand off *from* Claude Code and Codex, which are the providers whose
transcripts are read. It can hand off *to* Claude Code and Codex, which are the
providers whose opening-prompt syntax is known. Another provider chosen as a
destination is refused with the brief's path, rather than launched with the
brief silently dropped.
