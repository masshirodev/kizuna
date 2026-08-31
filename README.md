# ai-session

Small local launcher for AI CLI accounts that need isolated authentication state.

The first version keeps account state separate instead of copying or rewriting
tokens. Each launched process receives only its profile-specific state directory:

- Codex: `CODEX_HOME`
- Claude Code: `CLAUDE_CONFIG_DIR`
- Antigravity CLI: a profile-local `HOME` containing its `.gemini` state
- OpenCode: `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, and `XDG_STATE_HOME`

This prevents separate accounts from racing over one credential file and lets
each official CLI own its refresh-token lifecycle. `ai-session` never reads,
prints, or stores token contents.

## Install

```sh
./install.sh
```

The binary is `ai`, built from `cmd/ai`. `go install ./...` produces the same
thing in `$(go env GOPATH)/bin`.

The build must not pass `-buildvcs=false`: the commit stamp the Go toolchain
embeds is what `ai version` and the TUI's update check compare against the
repository, and a binary built without it can only report that it does not know.

## Create profiles

```sh
ai profile add codex-personal codex
ai profile add codex-work codex
ai profile add claude-personal claude
ai profile add antigravity-personal antigravity
ai profile add opencode-go opencode
ai profile add deepseek opencode
```

Log in through the isolated profile:

```sh
ai login codex-personal
ai login codex-work
ai login claude-personal
ai login antigravity-personal
ai login opencode-go
```

Update a profile's CLI with its supported updater:

```sh
ai update codex-personal
ai update claude-personal
ai update antigravity-personal
ai update opencode-go
```

This runs `codex update`, `claude update`, `agy update`, or `opencode upgrade`
respectively.

`ai run` uses the credentials already stored in that profile. A new profile has
no credentials until you run `ai login <profile>`; the login flow is performed
by the official CLI and may open a browser or ask for a device code. The
launcher intentionally does not copy the account from your normal Codex,
Claude, or Antigravity configuration.

Run a profile:

```sh
ai run codex-personal
ai run codex-work exec -- "review this repository"
ai run claude-personal
ai run antigravity-personal
ai run opencode-go
```

Codex and Claude profiles can be launched concurrently from multiple terminals.
Every launch creates a temporary directory beneath the profile's `instances/`
directory for its PID lock and an `instance.json` recording the folder it was
launched in, while all instances continue to use the profile's
single `CODEX_HOME` or `CLAUDE_CONFIG_DIR`. This means one login per profile and
the same settings and session history in every instance. The instance directory
is removed when the CLI exits and reclaimed automatically after a crash.

Antigravity and OpenCode profiles remain exclusive: a second launch is refused
while the first is running because their file-backed OAuth stores are not known
to coordinate token refreshes across processes.

Antigravity does not expose a config-home override, so its entire `HOME` is the
profile's `home/` directory. On Linux, `ai-session` also clears
`DBUS_SESSION_BUS_ADDRESS` for the child so `agy` uses its profile-local
`~/.gemini/antigravity-cli/antigravity-oauth-token` instead of silently sharing
one account through Secret Service. Commands run by Antigravity inherit that
private home too; project paths are unchanged, but host-home resources such as
`~/.ssh` should be referenced by absolute path when needed.

The keyring bypass is Linux-specific; until `agy` exposes a portable keyring or
config-home override, OAuth profiles on macOS and Windows may still resolve to
the CLI's shared OS-keyring entry.

The `run` command is optional when the first argument is a profile name. These
are equivalent, and any following arguments are passed to the profile's
configured command:

```sh
ai claude-personal
ai run claude-personal
ai codex-work exec -- "review this repository"
```

If the bare argument is not an existing profile, `ai` returns an error instead
of guessing a provider or creating a profile.

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

Each profile can also store default arguments. They are prepended to arguments
provided at launch, but are not used for login or integration commands. Set
them in the interactive profile editor; shell-style quotes group values with
spaces without invoking a shell. The same editor accepts a short note for each
profile. Arguments typed for a single launch with `p` in the TUI are appended
after the stored defaults.

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

## Update check

The TUI asks GitHub whether the build is behind the repository as its main
screen appears, and offers a line under the key help when it is:

```
↑ 3 commits behind main · go install ./...
```

Nothing is shown when the build is current, and a check that could not run stays
quiet rather than nagging. Press `r` to force a fresh check and see its answer,
whatever it is, in the status line. The same answer is available from the
command line:

```sh
ai version
```
```
ai 696fc1c (built 2026-08-28T11:47:41Z)
1 commit behind main · go install ./...
```

The comparison is between the commit the Go toolchain stamped into the binary
and the head of `main`, so there is no release process to keep up with. A binary
built with `-buildvcs=false` carries no revision and cannot be compared; `ai
version` says so and names the fix. This is why `install.sh` no longer passes
that flag.

`masshirodev/ai-session` is private, so the check needs a token. It reads
`GH_TOKEN`, then `GITHUB_TOKEN`, then falls back to `gh auth token`, and
disables itself when none of the three answers. The token is only sent to
api.github.com in an `Authorization` header; it is never logged or written to
disk. Answers are cached for six hours in `update-check.json` beside
`profiles.json`, so a launch does not spend a request every time.

## Move a profile to another computer

Install [`age`](https://age-encryption.org/) on both computers, then export the
profile as an encrypted bundle:

```sh
ai profile export codex-work codex-work.ai-profile.age
ai profile import codex-work.ai-profile.age
```

The export includes only that profile's metadata and isolated CLI state. `age`
prompts for the encryption passphrase; the launcher never prints or stores it.
Imports refuse to replace an existing profile. Treat the bundle like a password
backup and delete it after transferring it if it is no longer needed. API keys
provided through environment variables, such as `DEEPSEEK_API_KEY`, are not
included and must be transferred separately through a secret manager. Codex's
runtime-only `codex/tmp` tree is omitted; Codex recreates its helper symlinks
there on the destination machine. OpenCode plugin `node_modules` directories
are also omitted because they are host-specific and reproducible; reinstall
them from each plugin's `package.json` and lockfile after importing.
Antigravity's generated caches, built-ins, updater files, logs, and helper
binaries are omitted for the same reason; its settings, conversations, and
OAuth token remain in the bundle.

Do not actively use the same imported profile on multiple computers: provider
refresh tokens may rotate when used.

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
delete confirmation, and the instance pickers — opens as a box over that screen,
so the cockpit stays put while you answer.

Narrow terminals fold columns away rather than squeezing them: under 118 columns
the live panel merges into the middle, and under 78 the whole thing stacks into
one column. Short terminals drop panels in order of what a glance can afford to
lose — the activity histogram first, then the log, then the recent list.

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

`ACTIVITY` counts sessions touched per hour over the last day, from the
timestamps on the profile's own transcripts. It is deliberately not labelled as
quota: no provider records what a limit cost at a given hour, so the histogram
measures the one thing that is actually on disk. `RECENT SESSIONS` reads the
same transcripts for what each conversation was about, skipping the preamble
both CLIs write before the first thing you actually typed.

Use the arrow keys or `j`/`k` to select a profile:

- Enter runs the selected profile.
- `p` runs it with extra arguments typed at the prompt.
- `R` resumes a previous conversation.
- `h` hijacks a running instance: it opens that instance's conversation in this
  terminal, leaving the original process running.
- `l` logs in to the selected profile.
- `u` updates the selected provider CLI.
- `c` changes the folder used for subsequent CLI launches.
- `a` adds a profile.
- `e` edits its name, provider, command, default arguments, or note.
- `r` refreshes locally cached usage percentages and re-runs the update check.
- `x` deletes it and its isolated state after confirmation.
- `K` selects a running instance to stop; Enter stops that instance, while
  `a` or `y` stops every instance for the selected profile.
- `/` filters the profile list by name or provider. Enter keeps the filter and
  hands the keys back, so a search is a way to reach one account among many
  rather than a mode to dismiss before acting; Escape clears it.
- `q` or Escape quits.

Both instance pickers — `h` and `K` — list each running instance with the
conversation it has open and the folder it was launched in, so two instances of
the same profile can be told apart by what they are doing rather than by PID.

## Resuming and hijacking

`R` starts the provider's own resume flow in the current launch folder:

| Provider | Command |
| -------- | ------- |
| Codex | `codex resume` (session picker) |
| Claude Code | `claude --resume` (session picker) |
| Antigravity | `agy --continue` |
| OpenCode | `opencode --continue` |

Antigravity and OpenCode continue the last session for the current workspace
instead of opening a picker.

`h` skips the picker. It reads the running instances the launcher already
tracks, resolves the session each one has open, and reopens exactly that
session in the folder the instance was launched from. Session titles come from
the provider itself and are best effort:

| Provider | Source |
| -------- | ------ |
| Codex | the newest session log recorded for that folder |
| Claude Code | `claude agents --json`, matched on the recorded PID |
| Antigravity | not available; the instance is listed without a title |
| OpenCode | not available; the instance is listed without a title |

Hijacking an Antigravity or OpenCode profile is refused for the same reason a
second launch is: its credential store is exclusive while the first process is
running.

The selected-profile panel shows its default arguments and note. In the profile
editor, Enter or Tab advances through all fields; on the final field it saves.
Escape cancels and Ctrl-U clears the current field.

DeepSeek should be configured in the OpenCode profile using OpenCode's normal
provider setup or an environment variable. The launcher preserves ordinary
environment variables, including `DEEPSEEK_API_KEY`, but removes shared
`CODEX_HOME`, `CLAUDE_CONFIG_DIR`, XDG paths, and any inherited `AI_PROFILE` or
`AI_PROVIDER` before adding the selected profile's values.

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
so Codex and Claude hooks are installed beside that profile's own state. Login,
integration, and export remain exclusive operations and are refused while any
instance is running. Antigravity and OpenCode also retain this exclusive lock
for ordinary runs. If a launcher is interrupted, its lock is reclaimed
automatically after all PIDs recorded in it have exited. Different profiles can
still run at once.
In the TUI, select a running profile and press `K` to choose an individual CLI
process by PID, or stop all of that profile's instances. Each lock records the
launcher PID on its first line and the child CLI PID on its second line;
orphaned locks can therefore be reclaimed after an interrupted SSH session.

## License

MIT. See [LICENSE](LICENSE).
