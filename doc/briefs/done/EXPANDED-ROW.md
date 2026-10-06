# Design brief: the expanded row shows what it knows

Written 2026-10-06, from a screenshot of the board with `max2` selected and:
*"lets write a design brief to change this view, too much info is hidden"*

This brief is for Claude Design. It is not a plan. It reshapes the board's
selected-account expansion (`doc/tui.md`, "The board": "The selected account
expands in place"), which is the screen people look at most. What it carries,
in what order, and what gives way first are layout questions. Nothing below is
built.

## What exists today, measured

The expansion is drawn by `expansion()` in `cmd/ai/cockpit.go`. At the board's
full frame (146 wide, 4-column indent, so 142 inside) it has three parts:

1. **One launch line** (`launchSummary`). It joins these with `  ·  ` in launch
   order: the env prefix, command and default args; where the CLI resolves;
   the model; the indicator; `ranma tmux shim`; the note. The whole string is
   cut to 142 with `…`.
2. **Two panes side by side.** The left one is 60 wide (`min(60, inner*44/100)`)
   and holds `RUNNING HERE`, with each instance on two lines (title, then
   `PID · folder · uptime`) and the 24h sparkline under it. The right one is 79
   wide and holds `RECENT`, four rows of `when · title · folder`, with the
   `R`/`H`/`h` keys under them. When the right pane would be under 40 wide, the
   two stack.
3. **One blank line**, then the next account.

### Where it hides things, from the screenshot

| What is cut | Why | What is lost |
| --- | --- | --- |
| **The launch line's tail.** `… own status line · ranma tmux shim …` | It is one line, and it puts the longest and least-changing part first (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 claude --dangerously-skip-permissions` takes 75 columns). | The **note**, which is how the owner tells two accounts of the same provider apart (on `max2` it is the account it signs in with). The model would be cut too, on a profile that sets one. |
| **Recent titles.** `Mouse movement auto select with cli…` | The title is capped at 38 columns and the folder at 22, so a row uses 68 of the pane's 79. The cut happens while 11 columns sit empty. | The part of the title that tells two similar conversations apart. |
| **Only four recent rows.** | `expansionRecentRows = 4`. | Everything else is behind `R`. Two of the four rows in the screenshot are `What's next`. |
| **Running instances.** | Title, PID, folder, uptime. Nothing else. | Claude's session slug (`instanceSession.name`, the handle `claude agents` and the status line use). Whether the instance is headless, i.e. a wave worker. Which conversation id it is on. |
| **The sparkline.** `24h ▁▁▁▁█  peak 14:00` | 24 cells, one per hour, with only the peak labelled. | No hour axis, and no total ("31 sessions today"). |

### What the board knows and never shows on the board

All of these are already computed or one cheap read away:

- **Integrations.** The integrations box (`I`) knows each one's state:
  status line *ai's / another / none*, OpenUsage, the shim *applies here / only
  from ranma*. The board says only `own status line` and `ranma tmux shim`.
  Those are the configured values, not the states the box checks.
- **MCP servers and skills** in the profile (`m`, `s` count them).
- **App membership.** Whether the profile is the active member of an app
  (`ai app`). This matters, because something outside ai points at the app's
  path.
- **Exact reset times.** The row says `17:00` and `mon`. Going from "mon" to
  a date and hour takes a guess.
- **Handoff lineage** in both directions. `→ max` is drawn only on recent
  rows that were handed *out*. Nothing shows a conversation that arrived
  *from* another account.
- **Hidden headless sessions.** `RECENT` drops them, and nothing says how many
  were dropped.
- **The state directory**, i.e. where this profile's config, sessions and
  memory actually live.

## Tensions the design has to resolve

1. **The expansion versus the board.** Each line the expansion gains pushes
   other accounts down. The board stops at 40 rows. On a short terminal it
   already drops the expansion's second half before it drops rows, and then it
   scrolls. With eight profiles, the screenshot has room to spare. With
   fifteen, it would not.
2. **One glance versus everything.** The expansion is read while moving the
   cursor down the list, so it changes every keypress. A full profile sheet
   reads well when you stop on an account. It is noise when you are passing
   through. One answer is a short form while moving and a longer one after a
   pause. Another is a key that toggles a detail level. A third is a separate
   box (`↵` on the row is already *run*, so not that key).
3. **Configured versus true.** "own status line" is what the profile asks for.
   The integrations box says whether it is actually installed and whose it is.
   If the board starts showing states, they have to be the same checks the
   box and the launch use, or the board will contradict them.

## What the design needs to decide

- **The launch line.** Should it break into labelled fields (`COMMAND`, `CLI`,
  `MODEL`, `NOTE`, …) the way the editor's `LAUNCHES AS` does? Or stay one
  line but reorder it so the short, telling parts (note, model) come first and
  the long command goes last, where it can be cut? Or wrap onto a second line?
  The note in particular: is it a field, or a subtitle beside the name on the
  row itself?
- **The env prefix.** It belongs to the command, but it is often the longest
  token. Show it in full, collapse it to `+1 env`, or give it its own line?
- **Pane widths.** The 60/79 split and the 38/22 caps were set before the
  board was centred at 146. Should `RECENT` use its whole width for titles,
  with the folder giving way first (as the code comment already intends)?
- **How many recent rows**, and should two rows with the same title get the
  conversation's first distinguishing words, or its slug?
- **Running rows.** Add the slug, the headless marker, the model it started
  with? Keep two lines per instance, or go to one?
- **Integrations, MCP, skills, app.** Should they appear on the expansion,
  and how small? A row of chips (`statusline ai · openusage — · shim here ·
  3 mcp · 5 skills · app claude`)? A third pane? Or a one-word count that
  points at `I`, `m`, `s`?
- **Quota detail.** Should the expansion repeat the two windows with absolute
  reset times (`5h 92% → 17:00 today`, `7d 71% → Mon 12 Oct 09:00`), since
  the row only has room for the short form?
- **The sparkline.** Add an hour axis, a total, or neither?
- **What gives way first** when the terminal is short or narrow, in order. The
  current order is: second half, then gauges, then reset times, then live
  count, then auth, then the name column narrows. The new fields have to slot
  into it.
- **Whether a second level exists.** For example `tab`/`v` toggling a fuller
  sheet in place, a `d` "details" box, or nothing, with the expansion simply
  getting denser.

## Constraints the design must hold

- It is a full-screen Bubble Tea TUI with **no mouse**. Everything goes through
  the keyboard, and the key bar's five keys (`↵ run`, `R`, `H`, `p`, `/`) stay
  as they are unless the design argues otherwise.
- The board is at most **146 × 40**, centred, on the design's own canvas
  (`#0B0B0E`). Every tone is chosen against that canvas. The vocabulary is the
  gauge board's: uppercase faint section labels, the `▌` cursor and tinted
  selected rows, thin `━` gauges, and spaced key hints.
- The expansion is **tinted as part of the selection** (`tintLine`). Whatever
  it adds is read on that tint.
- `TestBoardMatchesTheDesignFrame` pins the board cell for cell against the
  original handoff's sample data. A redesign will move those cells on purpose,
  so the handoff should carry sample data again so that the test can be
  re-pinned against it.
- **ai never reads or prints credential contents.** Auth stays a file-exists
  check. The note is free text the owner typed and may hold an email, so show
  it as typed and don't parse it.
- Nothing may be drawn as known before it is read. The board already shows `…`
  until the first read (`pending()`), and new fields follow the same rule.
- `RECENT` is ordered by last message, not by start. That rule stays.

## Adjacent, not part of this

- The **nested instances under other accounts' rows** (`└ ▶ title  PID · folder
  · uptime`) are cut the same way. The design may restyle them to match
  whatever the running pane becomes, but they are not the subject.
- The **resume picker** (`R`) is where the full session list lives. This brief
  does not change it.
- A **CLI form** of the same sheet (`ai show <profile>`) would be cheap once the
  fields are agreed.
