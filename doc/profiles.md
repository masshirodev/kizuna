# Profiles

Creating, editing, cloning, and moving profiles, and the app profiles that give
another program a stable path to one. Back to the [README](../README.md).

## Create profiles

```sh
ai profile add codex-personal codex
ai profile add codex-work codex
ai profile add claude-personal claude
ai profile add antigravity-personal antigravity
ai profile add opencode-go opencode
ai profile add deepseek opencode
```

## Default arguments and notes

Each profile can also store default arguments. They configure the session a CLI
is about to start, so where they land depends on what starts it:

- **After a subcommand that starts a session.** `opencode run` takes the same
  session flags the bare TUI does, so a profile with `--auto` runs
  `opencode run --auto "msg"`. Placed before the subcommand, as they once were,
  opencode reads them as its own and prints its top-level help instead.
- **Not at all for a subcommand that manages state.** `opencode models`,
  `opencode auth`, and `claude mcp` take no session flags, so their defaults are
  dropped rather than forced onto a command that would reject them.
- **In front when there is no subcommand.** A bare interactive launch, a prompt
  that is not a subcommand, or a run whose arguments are only flags keeps the
  defaults first, exactly as before.

`ai run -p <profile> [arguments...]` (or the shorthand `ai -p <profile>
[arguments...]`) drops the defaults for that one launch: the CLI gets the
arguments given and nothing else. The flag comes before the profile name, so it
cannot collide with a provider's own `-p` after it (opencode's `auth login -p`).
Everything else about the launch is unchanged — the environment, the run lock,
the OpenCode instance copy, and the indicator.

Default arguments are not used for login or integration commands. Set them in
the interactive profile editor; shell-style quotes group values with spaces
without invoking a shell. The same editor accepts a short note for each profile.
Arguments typed for a single launch with `p` in the TUI are placed by the same
rule against the stored defaults.

The selected-profile panel shows its default arguments and note. In the profile
editor, Enter or Tab advances through all fields; on the final field it saves.
Escape cancels. Each field edits at its caret: the arrows, Home and End move it,
and Ctrl-U clears back to the start (see [Typing in a field](tui.md#typing-in-a-field)).

## Application profiles

A regular profile is for you, launched interactively. An **app profile**
is for something else — a program of your own that spawns an agent CLI as a
subprocess and points it at a state directory through its *own* static
config, never asking `ai` what that path currently means. `ai run` can
re-resolve a profile name on every launch; a program that isn't `ai` can't,
so app profiles exist to give it a path that never changes while what it
actually points at does.

```sh
ai profile add gemini-work antigravity
ai profile add gemini-personal antigravity
ai app add shiori gemini-work gemini-personal
ai app path shiori
# /home/you/.config/ai/profiles/apps/shiori
```

Paste that path into shiori's own config wherever it expects an agent's home
directory (`HOME` for Antigravity, `CLAUDE_CONFIG_DIR` for Claude Code,
`CODEX_HOME` for Codex — whatever shiori's backend reads). The first member
listed becomes active. To switch which one shiori actually uses:

```sh
ai app use shiori gemini-personal
```

`ai app path shiori` never changes; what it resolves to does. Nothing is
copied and no new credential store is created — the app profile is a symlink
at `ai app path shiori`, and switching just repoints it at a different
member's real profile directory (`ai app list` shows the current mapping).
Members don't have to share a provider: each provider's env vars address a
subpath of its own profile directory (`.../gemini-work/home`,
`.../codex-work/codex`, and so on), and the symlink stands for the whole
directory, so whichever provider-shaped path shiori's config already points
into resolves correctly as long as the active member is that provider.
Switching to a member of a *different* provider than shiori was last
configured for means updating shiori's own backend selection too — that part
is shiori's integration, not something `ai` can do on its behalf.

The roster is not fixed at creation. `ai app add` names the members an app
starts with; `ai app member` changes them afterwards:

```sh
ai app member add shiori codex-work        # one or more profiles
ai app member remove shiori gemini-work
```

Neither touches the symlink, because neither can change which member is
active — widening a roster that something else is already pointed at must not
move what that path resolves to. Use `ai app use` for that, and note the
ordering it implies: a profile has to be a member before it can be made
active.

Two removals are refused rather than resolved. **The active member** cannot be
removed, because dropping it would force the symlink to be repointed silently,
which is the one thing an app profile promises not to do on its own — switch
away with `ai app use` first. And the **last** member cannot be removed, since
an app with an empty roster still has a link resolving to a profile it no
longer names. Both errors say which command to reach for instead.

Every name is checked before anything is written, so a call naming one bad
profile leaves the roster exactly as it was rather than half-applied.

There's no live switching: `ai app use` only has to be right for shiori's
*next* launch, not for one already running with the old target open.

Ordinary `ai` commands also accept an app name in place of a profile name,
resolving to whichever member is currently active — useful for testing an
app profile interactively without touching shiori at all:

```sh
ai run shiori          # launches gemini-personal, same as ai run gemini-personal
ai login shiori
ai install shiori
```

The terminal title, `AI_PROFILE`, and any status line still name the real
member (`gemini-personal`), not the app (`shiori`) — that's the identity
actually paying for the session, and showing it as-is means every existing
indicator works unchanged.

App names and profile names share one namespace: you can't name an app the
same as an existing profile (or vice versa), and `apps` itself is reserved.
There's no `ai app rm` yet, matching plain profiles, which have no delete
command either — and a profile currently used by an app can't be renamed
from the TUI, since that would leave the app's symlink pointing nowhere. For
the same reason, a profile that is still a member has to be removed from the
app before it can be renamed.

## Clone a profile

A second account for the same provider usually wants the first one's setup: the
same MCP servers, the same skills, the same settings, the same default
arguments. `ai profile clone` makes that profile without making it by hand.

```sh
ai profile clone max spare
# cloned max into spare (claude)
# configuration copied; credentials were not — run 'ai login spare'
```

**A clone copies the setup, not the account.** That is the whole decision this
command makes, and it follows from what the launcher is for: two profiles exist
to keep two accounts apart, and a clone carrying the credential file would hand
one refresh token to two directories and let the provider's own rotation
invalidate whichever was used second. The new profile starts logged out.

What "configuration" means is an allowlist per provider, not a list of
exclusions — a state directory also holds conversation history, caches, machine
identifiers, and the credential file, and forgetting to exclude one of those
costs more than a clone arriving without some setting nobody noticed:

| Provider | Copied |
| -------- | ------ |
| Claude Code | `settings.json`, `skills/`, `agents/`, `commands/`, `hooks/`, `rules/`, `output-styles/`, `plugins/config.json`, `CLAUDE.md`, `AGENTS.md`, and the `mcpServers` key of `.claude.json` |
| Codex | `config.toml`, `skills/`, `prompts/`, `rules/`, `hooks.json`, `AGENTS.md` |
| OpenCode | the whole `config/opencode` tree, less `node_modules` |
| Antigravity | `config/config.json`, `config/mcp_config.json`, `antigravity-cli/settings.json` |

Claude Code's `.claude.json` is the one file copied in part rather than whole.
It holds the MCP servers, but the rest of it is the account — user id, OAuth
record, per-project history — so only `mcpServers` crosses.

Symlinks are recreated as symlinks. A profile whose `AGENTS.md` points at your
dotfiles repo gets a clone that tracks the same file, rather than a copy that
silently stops following it.

`--with-state` copies everything instead, credentials and history included,
minus the lock and instance bookkeeping that describes processes running right
now. It exists for the case where the export/import round trip below is not
what you want, and it says what it did rather than being the quiet default:

```sh
ai profile clone max spare --with-state
# credentials came with it; do not run both accounts at once
```

Cloning is refused while the source profile is running, for the same reason
export is: a config file read while its CLI is writing one is a clone of a
half-written file.

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
