# Profile indicators, OpenUsage and ranma

Seeing which account a session is paying with, from inside the CLI, wiring
OpenUsage into each isolated profile, and giving Claude Code's agent teams
ranma panes. Back to the [README](../README.md).

## Knowing which profile you are in

Once the provider CLI starts it owns the screen, and nothing in its own
interface says which account is paying for the session. Two markers cover that:

- **The terminal title.** A launch sets the window or tab title to
  `ai · <profile> (<provider>)` and restores the previous title on exit, using
  the xterm title stack. A terminal without that stack simply keeps the title
  `ai` set, which still names the right profile. Nothing is written when output
  is redirected, so piped output stays clean.
- **`AI_PROFILE` and `AI_PROVIDER`.** Every launched process gets both, for
  every provider, so a shell prompt, a CLI statusline, or a tmux status bar can
  render the profile however you like:

  ```sh
  # a Claude Code statusline, in the profile's own settings.json
  echo "[$AI_PROFILE] $(basename "$PWD")"
  ```

  They are also listed by `ai env <profile>`. An inherited pair is stripped
  before a launch, so a session started from inside another session reports its
  own profile rather than its parent's.

## Showing the profile inside the CLI

The terminal title and the TUI header both stop being visible the moment the
provider CLI takes over the screen. `ai integrate statusline <profile>` gives a
profile the best indicator its provider supports:

```sh
ai integrate statusline claude-personal
ai integrate statusline antigravity-personal
ai integrate statusline codex-work
```

**Claude Code and Antigravity render it themselves.** The command merges a
`statusLine` into that profile's own `settings.json`, keeping every other key,
and the line reads `AI_PROFILE` out of the launch environment so it names the
profile actually in use. An existing `statusLine` is refused rather than
replaced: it is yours, and overwriting it would silently drop whatever it
showed.

**Codex and OpenCode have no equivalent**, so those profiles are marked
`"indicator": "tmux"` in `profiles.json` and launch inside a tmux session whose
status bar sits above the CLI:

```
 ai · codex-work (codex)                        ~/projects/ai-session
──────────────────────────────────────────────────────────────────────
```

The bar survives whatever the CLI draws, which a terminal escape sequence
cannot: these CLIs take the alternate screen and manage their own scroll
regions. tmux's prefix is disabled so the session stays furniture rather than a
multiplexer to think about.

Two details matter for correctness. Each wrapped launch gets its **own tmux
socket**, because tmux runs a server and an existing one would hand the CLI that
server's environment instead of the profile's isolated directories. The socket
lives under `XDG_RUNTIME_DIR` rather than beside the lock, because a Unix socket
path is capped near 108 bytes and the per-instance lock directory is long enough
to exceed that on its own. Stopping a wrapped instance kills its tmux server,
not just the client.

Codex also configures its own terminal title, so it will overwrite the one `ai`
sets; the tmux bar is the reliable indicator there.

## OpenUsage integration

OpenUsage remains the usage/limits layer. Install its official integrations
inside each isolated profile:

```sh
ai integrate openusage claude-personal
ai integrate openusage codex-personal
ai integrate openusage codex-work
ai integrate openusage opencode-go
ai integrate openusage deepseek
```

The launcher deliberately does not modify OpenUsage's database or copy tokens.
It runs OpenUsage's supported installer with the selected profile environment,
so Codex and Claude hooks are installed beside that profile's own state.
Login, integration, and export are refused while any instance of the profile
is running; see [Concurrency and locks](running.md#concurrency-and-locks).

## Agent teams in ranma panes

Claude Code's agent teams open a pane per teammate when they find themselves
inside tmux. [ranma](https://github.com/masshirodev/ranma) is not tmux, but its
**tmux shim** answers the tmux calls a program makes with ranma panes
(`ranma tmux-shim -- CMD`; ranma's `doc/CONFIG.md`, "Programs that drive tmux").
`ai integrate ranma <profile>` makes a profile launch under it:

```sh
ai integrate ranma max2          # on
ai integrate ranma max2 --off    # off again
```

It sets `"tmux_shim": true` on the profile in `profiles.json`. From then on, a
launch of that profile **from a ranma pane** — `ai max2`, `ai max2 resume`, or
the TUI — runs as `ranma tmux-shim -- <command> <arguments>`, and for a Claude
profile also sets `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`, the flag Claude Code
keeps teams behind. It is the same as typing

```sh
ranma tmux-shim -- env CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 ai max2
```

by hand, which still works and needs no setting. What decides each step:

- **Only inside ranma.** A ranma pane carries `RANMA_SOCKET` and `RANMA_PANE`;
  without both there is nowhere to put a pane, and the profile launches exactly
  as it did before. The setting says where teammates go when there is a place
  for them, not that the profile must run in ranma.
- **Teammates keep the profile.** The shim gives each pane it opens the
  environment of the command it was started for, so a teammate runs on the
  lead's `CLAUDE_CONFIG_DIR` — the same account — even though Claude Code does
  not forward that variable itself.
- **Not twice.** A launch whose `TMUX` already names this ranma's socket is
  already under the shim (it was started from a shim pane, or from a shell
  under `ranma tmux-shim`); it only gets the teams flag.
- **Your teams setting wins.** A `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS` already
  in the environment, `0` included, is left alone. Other providers get the shim
  and no flag: the shim is general, the flag is Claude's.
- **Not with the tmux indicator.** `"indicator": "tmux"` starts a real tmux with
  `TMUX` cleared, so nothing inside it could reach the shim. Turning the shim on
  for such a profile is refused, and a launch with both set fails naming them.
- **`ranma` must be on `PATH`**, which it is in any pane ranma started.

The shim execs the command in its own place, so the PID the run lock records
is still the CLI's, and stopping or hijacking the instance works as usual. The
TUI's account line shows `ranma tmux shim` for a profile that has it on.
