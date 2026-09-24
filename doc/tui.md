# The TUI

Running `ai` with no arguments opens the cockpit. Back to the [README](../README.md).

## Layout

Running `ai` without arguments opens an interactive Bubble Tea TUI: one
fullscreen screen with a title bar, three columns, and a key bar.

- **Profiles**, on the left — every account with its provider and the two quota
  figures, then which of them are logged in, and the launch folder pinned to the
  bottom.
- **The selected profile**, in the middle — how it launches, what quota it has
  left and when that resets, how busy it has been over the last day, and what it
  was last working on.
- **What is live**, on the right — every running instance across every profile
  with the conversation it has open and how long it has been up, then a short
  log of what the launcher has done.

The title bar names the profile under the cursor next to the profile count, so
the screen says which account is in play rather than leaving it to the row
highlight, and it carries the launch folder and any pending update.

Everything else — the profile editor, the folder and argument prompts, the
delete confirmation, the clone and copy pickers, and the instance pickers —
opens as a box over that screen, so the cockpit stays put while you answer.

Narrow terminals fold columns away rather than squeezing them: under 118 columns
the live panel merges into the middle, and under 78 the whole thing stacks into
one column. Short terminals drop panels in order of what a glance can afford to
lose — the activity histogram first, then the log, then the recent list.

## The profile panel

Providers are colour-coded, a profile with active launches is marked `▶ running`
or `▶ N running`, and the `AUTH` block shows whether the profile has logged in:

- `● yes` — the provider's credential file exists in the isolated state
  directory, so `ai run` can use it.
- `● key` — a `deepseek` profile with `DEEPSEEK_API_KEY` exported, or an
  Antigravity profile with `modelProvider: "gemini"` and `GEMINI_API_KEY`.
  These authenticate from the environment rather than a stored OAuth file.
- `○ no` — no credentials yet; press `l` to log in.
- `· ?` — the provider has no known credential location, so the launcher does
  not guess.

The `cli` field shows where the profile's command resolves on `PATH`, or
`not installed — press i`. See
[Install the provider CLIs](running.md#install-the-provider-clis).

The `model` field shows the model the profile will start with, read from that
CLI's own settings inside the isolated directory. `—` means the provider has no
discoverable answer yet:

| Provider | Source |
| -------- | ------ |
| Codex | `codex/config.toml`, top-level `model` |
| Claude Code | `claude/settings.json`, `model` |
| OpenCode | `state/opencode/model.json`, most recent entry; falls back to `model` in `config/opencode/opencode.json[c]` |

OpenCode's state file is preferred because it holds the model last chosen in the
TUI, which is what OpenCode restores on the next start; the config default only
applies before anything has been picked.

The `5H` and `7D` columns, and the `QUOTA` meters beside them, show the
remaining five-hour and weekly quotas reported by the selected account's own
local CLI cache, along with when each window rolls over. Codex is read from the
newest rate-limit events in that profile's session logs; Claude Code is read
from its cached usage utilization. Expired or unavailable windows show `—`.
Antigravity, OpenCode, and DeepSeek do not currently expose comparable local
quota caches that this launcher reads. This is intentionally profile-local and
does not use OpenUsage, so multiple accounts for the same provider stay
separate. Credential files are never opened.

The check only tests whether the file exists, and reads the API key variable
only to see whether it is empty; `ai-session` still never opens or prints
credentials. A profile whose token has expired therefore keeps showing `● yes`
until the official CLI asks you to log in again.

The TUI starts in the directory where you ran `ai`. Press `c` to set another
launch folder without leaving the TUI; relative paths resolve from the current
launch folder and `~` is supported. The chosen folder applies to subsequent
CLI launches in that TUI session and does not change the parent shell's
directory.

## Activity and recent sessions

`ACTIVITY` counts sessions touched per hour over the last day, from the
timestamps on the profile's own transcripts. It is deliberately not labelled as
quota: no provider records what a limit cost at a given hour, so the histogram
measures the one thing that is actually on disk. `RECENT SESSIONS` reads the
same transcripts for what each conversation was about, skipping the preamble
both CLIs write before the first thing you actually typed. Two shapes are
handled, because the CLIs use both: a block sent *ahead* of the prompt is
skipped whole (environment dumps, harness reminders, Codex's `AGENTS.md`
instruction dump), while a block wrapped *around* the prompt is unwrapped
instead — Codex's IDE integration leads with your open tabs and labels the part
you typed, so skipping the message would lose the prompt with it. `R` turns that panel
into a picker and resumes the row you choose.

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

Use the arrow keys or `j`/`k` to select a profile:

- Enter runs the selected profile.
- `p` runs it with extra arguments typed at the prompt, or with a recent or
  pinned set picked from under it.
- `R` resumes one of the recent sessions, in the folder it ran in.
- `h` hijacks a running instance: it opens that instance's conversation in this
  terminal, leaving the original process running.
- `H` hands a session to another account, for when one has run out of quota.
- `A` turns auto-swap on or off. Off by default.
- `l` logs in to the selected profile.
- `i` installs the selected provider's CLI, after showing the command.
- `u` updates the selected provider CLI.
- `U` updates `ai-session` itself, after showing the checkout and the steps.
- `c` changes the folder used for subsequent CLI launches.
- `a` adds a profile.
- `e` edits its name, provider, command, default arguments, or note.
- `C` clones it: a new profile with the same setup and no credentials.
- `m` installs MCP servers into it from another profile.
- `s` installs skills into it from another profile.
- `r` refreshes locally cached usage percentages and re-runs the update check.
- `x` deletes it and its isolated state after confirmation.
- `K` selects a running instance to stop; Enter stops that instance, while
  `a` or `y` stops every instance for the selected profile.
- `/` filters the profile list by name or provider. Enter keeps the filter and
  hands the keys back, so a search is a way to reach one account among many
  rather than a mode to dismiss before acting; Escape clears it.
- `?` opens the key pane, and any key closes it.
- `q` or Escape quits.

The key bar along the bottom drops entries from the end until it fits, so on a
narrow terminal it is not the whole list — and the entries it drops are exactly
the ones nobody has learned yet. `?` is the whole list, grouped by what each key
acts on: a conversation, a profile, a provider's CLI, or `ai-session` itself. It
folds to one column rather than clipping when the terminal is too narrow for
two.

Both instance pickers — `h` and `K` — list each running instance with the
conversation it has open and the folder it was launched in, so two instances of
the same profile can be told apart by what they are doing rather than by PID.
The resume picker — `R` — lists transcripts rather than processes, so its rows
are dated instead of numbered.

## Recent and pinned arguments

The `p` prompt lists what it has been given before under the field: `PINNED`
sets first, in the order they were pinned, then the last five sets run under
`RECENT`, newest first. Only a launch that actually started is recorded, and
only when it carried arguments — a plain run is what Enter is for.

The list is shared by every profile and every provider, which is the point:
the `--model` you gave one Claude account is one keypress away on the next.
It is also the risk, since a flag one CLI understands is one another rejects,
so each row names the profile and provider it last ran on. Running a set that
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

## Modal sizing

**The box is sized to the terminal, not to what it happens to be showing.** Every
modal grows in width with the window up to a ceiling of its own — the pickers are
the widest, the help pane next, and the boxes that ask one question stop soonest,
since a label and the value beside it read worse spread across an ultrawide than
they do in a column. The width a wide terminal adds past that goes to the preview
rather than the list: a session row is a time, a title, a folder and the id, and
beyond the width those need, more columns only pad the gaps between them, while
the preview turns every extra column into a sentence that fits on one line.

Height is the frame's, and it does not move. The pickers, the item list of a
share, and the handoff confirmation are drawn at the full height the frame allows
and padded out to it, however much the row under the cursor has to say. Sizing
them to their content instead — which is what they used to do — meant a different
box for every row: stepping from a long conversation to a two-message one
collapsed the list beside it as well, and the row being aimed at moved while it
was being aimed at. A status line inside one of these boxes comes out of that
height rather than being added to it, so a message about the last keypress does
not push the box two rows taller either. The boxes that ask one question are
still as tall as the question.
