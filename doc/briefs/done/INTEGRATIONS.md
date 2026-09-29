# Design brief: an integrations box

Written 2026-09-29, from: *"we should have a integrate modal in ai-session no?
showing what is integrated and what we can integrate through it"*

This is a brief to forward to Claude Design, not a plan. It is a new box in the
TUI, and what a row says, how a profile is chosen and what `↵` does are layout
questions. They are cheaper to argue over on a canvas than in a diff. Nothing
below is built.

## What exists today, measured

Integrations exist only on the command line, as `ai integrate <what>
<profile>`. The TUI has no key and no palette entry for them. The board shows a
little of the result: the expanded row's launch line (`launchSummary`) names
the indicator (`own status line`, `tmux status bar`, `no indicator`) and, since
PR #36, `ranma tmux shim`.

There are three integrations, and each one has a different shape:

| Integration | Command | What it writes | Undo | Can it be read back? |
| --- | --- | --- | --- | --- |
| **Status line** | `ai integrate statusline <p>` | Claude Code or Antigravity: a `statusLine` merged into the profile's own `settings.json`. Codex or OpenCode: `"indicator": "tmux"` on the profile in `profiles.json`. | None in the CLI. Delete the key by hand. | Yes. But a `statusLine` already in `settings.json` may be the user's own and not ai's, and installing over one is refused. So there are three states, not two: *ai's line*, *another line*, *none*. |
| **OpenUsage** | `ai integrate openusage <p>` | Runs OpenUsage's own installer (`openusage integrations install claude_code\|codex\|opencode`) with the profile's environment. It installs hooks beside the profile's state. | None from ai. | **Unknown.** ai never looks at what OpenUsage wrote. `openusage` is not installed on this workstation. Refused while any instance of the profile is running (exclusive lock). |
| **ranma tmux shim** | `ai integrate ranma <p> [--off]` | `"tmux_shim": true` on the profile. For Claude, a launch also gets `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`. | `--off` | Yes, from the config. Whether it takes effect depends on where the launch happens: only from a ranma pane. |

Other constraints that already exist:

- **Not every integration applies to every provider.** OpenUsage has no
  Antigravity integration: `openUsageIntegration` passes the provider name
  through, and OpenUsage has nothing by that name. The status line differs by
  provider (native or tmux). The shim applies to any provider, but only Claude
  has agent teams for it to serve.
- **Integrations can conflict.** The tmux indicator and the ranma shim are
  refused together, both at `integrate` time and at launch.
- **Some have prerequisites.** The tmux indicator needs `tmux` on PATH.
  OpenUsage needs `openusage`. The shim needs `ranma` and only acts inside a
  ranma pane.
- **An app name resolves to its active member.** On the CLI,
  `ai integrate … <app>` acts on whichever profile is active.

## Tensions the design has to resolve

1. **Per profile, or per integration.** One option is a box for the profile
   under the cursor that lists every integration and its state. The other is a
   matrix of integrations × profiles, which shows the whole machine at once. The
   matrix answers "who has OpenUsage?"; the per-profile box answers "what does
   max2 have?". The account list already has one profile under the cursor, so
   the per-profile box is the natural entry, but the owner said "what is
   integrated", which reads as an overview.
2. **Three truthful states versus a checkbox.** A tick box suggests on and off.
   The real states are richer: *on*, *off*, *on but someone else's* (the
   status line), *unknown* (OpenUsage), *not available for this provider*,
   *blocked by a missing tool*, *blocked by another integration*, and *on but
   inert here* (the shim outside ranma). The box has to say all of these
   without turning into a paragraph per row.
3. **Acting versus explaining.** Turning the shim on is a config write and
   instant. Installing OpenUsage runs an external installer that prints output,
   the way `i` (install the CLI) shows its command first. The status line
   writes into the provider's settings. Does one key do all of these, or does
   the heavier kind confirm first?

## What the design needs to decide

- **Where it opens from.** A new board key (`I`, `g`?), with a palette entry
  under **PROFILE** or **PROVIDER CLI**. Which group does it belong to?
- **The shape.** Take the one-question box (88 wide), the MCP/skill box's two
  panes (list left, detail right), or the matrix. For a two-pane box, the right
  pane could say what the integration writes, where, and how to undo it, the
  same way the editor's `LAUNCHES AS` shows the real command.
- **The row.** Name, one-word state, and what? The file it touches? The
  consequence (`teammates open in ranma panes`)?
- **Unavailable rows.** For "not for this provider", "needs tmux" and
  "conflicts with the tmux indicator": hide them, dim them with a reason, or
  put them in a trailing group like the MCP box's `nothing to lend`?
- **The OpenUsage row when ai cannot tell.** Show `unknown` honestly, or leave
  out a state and offer only `install`? (Reading OpenUsage's hooks back is its
  own piece of work and may not be worth it.)
- **Undo.** The status line and OpenUsage have no undo in the CLI today. Does
  the box offer one, and so commit the code to growing it, or show them as
  install-only?
- **Running instances.** OpenUsage is refused while the profile runs. Is that
  row locked and dimmed, the way the editor locks name, provider and command
  under a `running:` line?
- **The ranma row's context.** Say whether the TUI itself is in a ranma pane
  right now (`applies here` / `applies only when launched from ranma`)?
- **Apps.** Does the box act on an app's active member, and does it say so?
- **Discoverability on the board.** Should the collapsed row carry a small
  marker for integrations, or does the expanded launch line stay the only
  place?

## Constraints the design must hold

- It is a full-screen Bubble Tea TUI with **no mouse**. Everything is done from
  the keyboard.
- Box sizing follows `doc/tui.md`, "Box sizing". A box grows with the window up
  to its own ceiling (one-question boxes 88, editor 92, palette 120, pickers up
  to 140) and gives six columns back to the board. A picker-like box keeps its
  height fixed, and its status line rides inside it.
- Selection and ticking follow the MCP/skill box where they overlap: `space`
  ticks, `←`/`→` move between panes, `a` ticks all.
- ai never reads or copies token contents. No integration row may show one.
- Whatever the box says is on must come from the same checks that the launch
  and `ai integrate` use, so the box can't claim a state the launch disagrees
  with. The MCP box's `TRANSLATED ON THE WAY` notes hold themselves to the same
  rule.
- The CLI stays the full interface. The box is another way in, not the only
  one.

## Adjacent, not part of this

MCP servers (`m`) and skills (`s`) are also "things installed into a profile",
but they are copied from other profiles, and they already have their own box.
Folding them in is a separate decision. The design may say whether the new box
links to them. An `ai integrate list` on the CLI would be the text form of the
same states. Once the states are agreed, it is cheap to add.
