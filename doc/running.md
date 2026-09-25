# Installing, logging in, and running

How a profile gets its CLI, its credentials, and a launch, and what each
provider needs to run in isolation. Back to the [README](../README.md).

## Install the provider CLIs

`ai-session` isolates state; it does not ship the CLIs themselves. A profile
can be fully configured and still fail at launch because its command was never
installed, and `exec`'s own message for that names neither the profile nor the
way out. The selected-profile panel therefore shows a `cli` line — where the
command resolves, or `not installed — press i` — and `ai install` runs the
provider's own installer:

```sh
ai install claude-personal   # a profile: installs its provider's CLI
ai install codex             # or a bare provider, before any profile exists
```

| Provider | Installer |
| -------- | --------- |
| Codex | `curl -fsSL https://chatgpt.com/codex/install.sh \| sh` |
| Claude Code | `curl -fsSL https://claude.ai/install.sh \| bash` |
| Antigravity | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
| OpenCode, DeepSeek | `curl -fsSL https://opencode.ai/install \| bash` |

These are each vendor's own documented script. The launcher deliberately keeps
no package list of its own: it would be one more thing to keep correct, and
wrong here means installing the wrong software.

Two details differ from typing the one-liner yourself. The script is
**downloaded before it is run** rather than piped straight into a shell — the
same thing ends up executing, but a truncated response cannot half-execute and
the script is on disk to read when an install goes wrong. And it runs in your
**ordinary environment, not a profile's**: the CLI binary is shared by every
profile, and Antigravity's private `HOME` would otherwise bury the install
inside one profile's state directory. In the TUI, `i` shows the exact command
and asks before running it.

A provider with no known installer is refused rather than guessed at.

## Log in and update

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

## Run

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

The profile's default arguments, if it has any, are placed according to what
starts the CLI: after a subcommand that opens a session (`opencode run --auto
"msg"`), dropped for a subcommand that manages state instead (`opencode models`,
`claude mcp`), and in front when the launch has no subcommand at all. This keeps
a session flag from landing before the subcommand, where opencode and claude
read it as their own and print help instead. See
[profiles.md](profiles.md#default-arguments-and-notes).

To launch with the arguments given and no defaults at all, pass `-p` (or
`--plain`) before the profile name:

```sh
ai run -p opencode-go models
ai -p opencode-go
```

The flag belongs to `ai`, so it is read before the profile name and never
confused with a provider's own `-p`.

## Running several at once

Codex and Claude profiles can be launched concurrently from multiple terminals.
Every launch creates a temporary directory beneath the profile's `instances/`
directory for its PID lock and an `instance.json` recording the folder it was
launched in, while all instances continue to use the profile's
single `CODEX_HOME` or `CLAUDE_CONFIG_DIR`. This means one login per profile and
the same settings and session history in every instance. The instance directory
is removed when the CLI exits and reclaimed automatically after a crash.

OpenCode profiles run concurrently too, but on a private copy instead of the
shared store: each launch seeds `instances/run-XXX/data` and `.../state` from
the profile's `data/opencode` (`opencode.db` snapshotted with `VACUUM INTO`,
plus `auth.json`) and `state/opencode` (`model.json`, prompt history), and
points `XDG_DATA_HOME`/`XDG_STATE_HOME` there while keeping the profile's
`XDG_CONFIG_HOME`. Sharing one `opencode.db` between live processes corrupts
it or fails with `SQLITE_BUSY` upstream, so the copy is the whole point. When
the instance exits — normally, through `K`, or by crashing — its store merges
back into the profile: new sessions and their messages by id, credential rows
only when newer (`time_updated`), `auth.json` only when this instance actually
refreshed it (a stale copy never overwrites a newer one), prompt history
appended, logs carried over. Identity, migration, and event tables never
cross. A merge that cannot finish keeps the store and retries on the next
start: opening `ai` reclaims stray instances left by power-offs, killed
terminals, and `kill -9`s before anything else runs.

Two consequences. The recent list shows every live instance's sessions, but
only merged ones can be resumed — a fresh launch seeds from the profile store,
so resuming a session that still lives in another running instance is refused
with the reason instead of opening nothing. And hijacking a live OpenCode
instance stays refused for the same reason a second launch used to be: two
writers on one session is exactly the shared-database bug.

Antigravity profiles remain exclusive: a second launch is refused
while the first is running because its file-backed OAuth stores are not known
to coordinate token refreshes across processes.

## Antigravity's private home

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

## Shorthand

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

## Concurrency and locks

Login, integration, and export remain exclusive operations and are refused while any
instance is running. Antigravity also retains the exclusive lock
for ordinary runs. OpenCode runs concurrently on private per-instance stores
(see [Running several at once](#running-several-at-once)); stopping one from the TUI merges its store back
first. If a launcher is interrupted, its lock is reclaimed
automatically after all PIDs recorded in it have exited, and an OpenCode store
left behind merges on the next start. Different profiles can
still run at once.
In the TUI, select a running profile and press `K` to choose an individual CLI
process by PID, or stop all of that profile's instances. Each lock records the
launcher PID on its first line and the child CLI PID on its second line;
orphaned locks can therefore be reclaimed after an interrupted SSH session.

## Environment

DeepSeek should be configured in the OpenCode profile using OpenCode's normal
provider setup or an environment variable. The launcher preserves ordinary
environment variables, including `DEEPSEEK_API_KEY`, but removes shared
`CODEX_HOME`, `CLAUDE_CONFIG_DIR`, XDG paths, and any inherited `AI_PROFILE` or
`AI_PROVIDER` before adding the selected profile's values.
