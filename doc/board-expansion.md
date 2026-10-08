# The board expansion

The account under the cursor expands in place, under its own row. This is the
screen read most, so it carries everything the board knows about the account,
at two levels. Back to [the TUI](tui.md).

The layout is the **Board expansion** Claude Design handoff
(`doc/handoffs/Board expansion.html`), drawn from the brief
[`doc/briefs/done/EXPANDED-ROW.md`](briefs/done/EXPANDED-ROW.md). The brief
started from the owner saying the old expansion hid too much: its launch line
was one string cut at the frame's edge, so the note was the first thing lost,
and its recent titles were cut at 38 columns while the pane still had room.

## Two levels

- **The glance** is what moving the cursor shows. It has to tell the account
  apart and say what it is doing, and change every keypress without becoming
  noise.
- **The sheet** opens on `tab` and folds back on `tab`. It is everything, laid
  out in columns. It stays open as the cursor moves, because it is a level the
  board is read at rather than a box about one account.

A third, the **compact** line (how the account launches, on one line), is what
a board too short for either still has room for.

## The glance

```
▌ claude-personal       claude       ━━ 7%   21:40    ━━━━━━━ 22%  thu      ● ok    ▶ 3
                                     resets 21:40 today  in 7h35m                   resets Thu 24 Sep 09:00  in 18h55m
    NOTE  personal pro plan · you@example.com                                       MODEL opus         CLI ~/.local/bin/claude
    RUNS  claude --dangerously-skip-permissions --model opus                        ENV   CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1
    WITH  status line ● ai's line   openusage · not read   tmux shim ● inert here   HAS   3 mcp  ·  5 skills  ·  app shiori (active)

    RUNNING HERE  3                                          RECENT   by last message  ·  3 headless hidden
    ▶ Refactor cockpit layout folding  1h12m                 14:02  Refactor cockpit layout folding                   ai-session
      brisk-amber-otter · PID 48213 · ~/projects/ai-session  11:37  Handoff brief: trim injected blocks               ai-session
    ▶ Draft release notes for 0.9  23m                       10:12  What's next  · after the folder tree lands        ai-session
      quiet-linen-heron · PID 51877 · ~/notes                09:48  What's next  · billing retry backoff              billing       ← codex-work
    ◇ Wave 2: lint the handoff package  4m                   yest.  Investigate SQLITE_BUSY on resume                 ai-session    → codex-work
      headless · tidy-cobalt-wren · PID 52110

    24h ▁▁▁▁▁▁▁▂▂▃▅▄▂▆█▅▃▂▄▆▃▂▁▂  66 sessions  ·  peak 14:00        R resume   H hand off   h open live   tab sheet
```

- **The reset line** sits under each gauge and spells out what the row
  abbreviates: `thu` becomes `Thu 24 Sep 09:00  in 18h55m`. A narrow gauge
  column keeps the time and drops the wait.
- **The launch** is three labelled lines in two columns. The command is `RUNS`,
  the environment `ENV`, so the long `NAME=value` prefix no longer pushes the
  note off the end. The note comes first, because it is how two accounts of the
  same provider are told apart.
- **`MODEL`** is the model the launch actually starts with. A `--model` /
  `-m` in the profile's default arguments wins over the CLI's settings, as it
  does on the CLI's own command line. The old line named the settings' model
  beside a command that passed another one. With neither, it says `default`.
- **`WITH`** is each integration that applies, in the words the integrations
  box (`I`) uses, from the same `integrationsFor` it calls. The row and the box
  cannot disagree. OpenUsage is always `· not read`, because ai never reads its
  hooks back; when the tool is not installed it says `· not installed`.
- **`HAS`** is the MCP servers and skills installed in the profile, and each
  app it is a member of, `(active)` when the app points at it.
- **`RUNNING HERE`** is each live instance: title and uptime, then the handle
  Claude runs it under, its PID and folder. A headless one (`ai run p -p …`, a
  wave worker) is marked `◇` and `headless`.
- **`RECENT`** is the last five conversations, by last message, with the
  headless ones hidden and counted in the header. Titles use the pane's width,
  and the folder (by its last name here) gives way first.

## The sheet

```
    LAUNCH   in the order a launch uses it                          INTEGRATIONS   as I checks them     IN THE PROFILE
    NOTE     personal pro plan · you@example.com                    status line ● ai's line             mcp servers 3  context7 · github · sqlite
    ENV      CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1                 openusage   · not read              skills      5
    COMMAND  claude --dangerously-skip-permissions --model opus     tmux shim   ● on · inert here       app         shiori · active member
    CLI      ~/.local/bin/claude                                                only from a ranma pane  auth        ● ok   file present
    MODEL    opus   from args
    STATE    ~/.config/ai/profiles/claude-personal                              ▶ locked while running
```

Under the three columns, the same two panes, longer: each instance gets a third
line with its folder and the conversation's short id, `RECENT` lists eight with
`showing 8 of 41`, and recent folders keep their path, collapsing the middle
(`~/…/ai-session`) when it does not fit. The sparkline gets an hour axis.

To make room, the board around the sheet folds: the other accounts' instances
are not nested under them, and the accounts with no quota cache fold onto one
line (`NO LOCAL QUOTA CACHE   antigravity-personal · deepseek · opencode-go  ↓`)
unless the cursor is among them.

- **`MODEL … from args`** or **`from settings`** says which of the two decided.
- **`STATE`** is the profile's own directory, where its config, sessions and
  memory live.
- **`INTEGRATIONS`** adds what qualifies a state: where the shim takes effect
  (`only from a ranma pane` / `applies here`), and `▶ locked while running`
  when OpenUsage cannot be installed because the profile is running.
- **`auth`** says what the state was read from, which is all ai ever learns
  about a credential: `file present`, or `from the environment` for a key.

## Where the facts come from

The launch fields come from the profile's config and are drawn as they are. The
rest costs a file read, so it is read with the cockpit's other panels, off the
UI thread, every refresh (`loadProfileFacts`, `cmd/ai/facts.go`), and the row
shows `…` until it lands:

| Fact | Read from |
| --- | --- |
| Integration states | `integrationsFor`, the integrations box's own checks |
| MCP servers, skills | `readMCPServers`, `readSkills`, the share box's readers |
| Apps | `profiles.json`, `apps` |
| `showing N of M`, `headless hidden` | a `COUNT` on the session index (`sessions.db`); left out when the index cannot be read |
| Second prompts | the conversation's turns, through the index's turn cache |
| `← account` | `lineage.json`, `target_session_id` |
| Slugs | the `h` / `k` pickers' `claude agents` call, kept by PID |

### Telling two conversations with one title apart

Two recent rows titled `What's next` say nothing about which is which, so a
title shared by another listed row carries the **second thing the user typed**
in that conversation, after a dim `·`. The title is made of the first prompt,
so the second is what tells them apart. The blocks a CLI sends ahead of a prompt
are skipped the same way the titles skip them. It is only read for rows that
are on screen and actually share a title, and once per conversation as it
stands: the read is remembered against the conversation's id and last-activity
time, so a refresh does not read it again.

### Where a conversation came from

`→ account` marks a conversation handed out, as it always has. `← account`
marks one that **arrived** by handoff, which needed something `lineage.json`
did not record: the conversation the handoff became. That cannot be known at
launch, because the target CLI names its session once it starts.

So the board finds it afterwards, by what the conversation was opened with. A
prompt handoff starts the target on `Read the handoff brief at <path> …`; a
pasted one begins with the brief's `# Handoff: …` heading. The conversation in
the selected account whose first prompt carries either is the one, which is a
match on what was said rather than a guess from times. Time and folder only
choose which conversations are worth reading: begun in the handoff's folder,
from two minutes before it to a day after (a pasted brief waits for whoever
pastes it). The match is written into the link as `target_session_id`, once,
and older handoffs are resolved the same way the first time their target
account is selected. A conversation read for a link and not matched is
remembered and not read for it again.

## Narrow and short terminals

The expansion's columns are the handoff's at the 146-column frame. Narrower,
they give way in this order: the glance's second launch column narrows, then
the running pane (to 36), then the recent pane moves under the running one, and
the launch columns stack. In the recent pane the folder column goes before the
title is squeezed past 28 columns, and the arrow's room is kept only when a row
has an arrow. The sheet's integrations and profile columns narrow to 32 and 36
before the three stack.

Taller than the frame, the board tries, in order: the sheet (when open), the
glance, the glance on a folded board, the compact line, and only then scrolls.
A terminal too short for the sheet shows the glance with `tab less` in its keys,
so the keypress is seen to have landed.

## Tests

`TestBoardMatchesTheExpansionDesign` and `TestSheetMatchesTheExpansionDesign`
(`cmd/ai/expansion_design_test.go`) draw the board with the mock's own sample
data and compare every row cell for cell. The rows were checked against the
handoff's rendered text and match it, except the two listed below. The
columns are hand-set in the mock (the 57-column running pane, `MODEL` at 84,
the keys at 68, the 14- and 16-column folders) and a tidy-up that rounded one
would move everything to its right, which is why whole rows are pinned rather
than a few figures. `cmd/ai/facts_test.go` covers the pure parts and the two
reads that touch real files: the lineage resolution and the second prompts.

## Where it departs from the handoff

- **The hour axis labels the hours the bars are.** The mock draws `00 06 12 18`
  from the row's first cell, as if the histogram were a calendar day. It is the
  last 24 hours with the current one last, so at 14:05 the sixes fall at 18,
  00, 06 and 12, a few cells in. The axis follows the clock.
- **The title bar is the first handoff's**, two columns wider before the folder
  than this mock draws it. The earlier frame test pins it, and this change does
  not move it.
- **Empty fields are words, not `—`.** The mock fills every field. A missing
  note or environment says `none`, a missing model `default`, an app-less
  profile `none`: `—` is what the quota cells use for "expired or unavailable",
  and the two read alike.
- **Slugs appear once a picker has asked.** The mock shows one under every
  Claude instance. Only `claude agents` knows them, the transcript does not
  record them for a plain session, and the board does not start a CLI every ten
  seconds. The `h` and `k` pickers already ask when they open, and the board
  keeps what they learned, by PID.
- **The sheet hides integrations that do not apply**, as the integrations box's
  per-profile scope does, rather than listing them as `n/a`.
- **`openusage · not installed`.** The mock has no drawing for OpenUsage with
  the tool missing; the box's `needs openusage` read oddly in a row named
  openusage.

## What it does not do yet

- **Headless instances started before this change read as interactive.** The
  flag is recorded in `instance.json` at launch from now on.
- **`showing N of M` counts the index.** A live OpenCode session not yet merged
  into the profile's store is listed but not counted; the count never reads
  below the rows shown.
- **A handoff whose conversation is not among the account's twelve most recent
  is not resolved** until it is. Old handoffs resolve as their targets come up.
- **No light-theme review**, as with the rest of the board.
