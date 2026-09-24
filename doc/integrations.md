# Profile indicators and OpenUsage

Seeing which account a session is paying with, from inside the CLI, and
wiring OpenUsage into each isolated profile. Back to the [README](../README.md).

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
