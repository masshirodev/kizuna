# kizuna

*Kizuna* (絆) is a bond: the ties between your accounts, and between the
agents running on them.

![kizuna demo](doc/assets/demo.gif)

One launcher for several AI CLI accounts: Claude Code, Codex, OpenCode, and
Antigravity. Each profile gets its own state directory, so two accounts never
share a credential file. Each official CLI still handles its own logins and
token refreshes. `kizuna` never reads, prints, or stores token contents.

| Provider | Isolated through |
| -------- | ---------------- |
| Claude Code | `CLAUDE_CONFIG_DIR` |
| Codex | `CODEX_HOME` |
| OpenCode | `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_STATE_HOME` |
| Antigravity | a profile-local `HOME` |

## Install

On Arch, from the AUR: `kizuna-bin` (the release binary) or `kizuna-git`
(built from `main`). Elsewhere, take a binary from the
[releases](https://github.com/masshirodev/kizuna/releases): it is static, so
any Linux on amd64 or arm64 runs it. Or build from source:

```sh
./install.sh
```

That builds `kizuna` from `cmd/kizuna` into `$(go env GOPATH)/bin`, and after
that `kizuna self-update` (or `U` in the TUI) keeps it current. A packaged
kizuna leaves updating to its package manager. See
[doc/RELEASING.md](doc/RELEASING.md).

**Coming from ai-session?** kizuna is its new name, and the command was `ai`.
Self-updating the old `ai` installs kizuna and keeps `ai` as a link to it; the
first run moves `~/.config/ai` to `~/.config/kizuna`. See
[doc/rename.md](doc/rename.md).

## Quickstart

```sh
kizuna profile add work claude # make a profile
kizuna install work            # install its provider's CLI, if missing
kizuna login work              # log in through the official CLI
kizuna work                    # run it (same as: kizuna run work)
kizuna                         # or open the TUI
```

`kizuna run -p work [arguments...]` launches with only those arguments, skipping the
profile's stored defaults. See [doc/running.md](doc/running.md#run).

## What it does

- **Isolated accounts.** Any number of profiles per provider. Claude, Codex, and
  OpenCode profiles can each run in several terminals at once.
- **A TUI cockpit.** One table of every profile, sorted by quota headroom, with
  a gauge per window, its login state, and what it is running; the selected
  account expands in place to show how it launches, its integrations, what it
  is running and its recent work, and `tab` opens the full sheet.
- **Resume anything.** `R` reopens any recent conversation in the folder it ran
  in. `h` attaches to one that is still running.
- **Find it by what was said.** `ctrl+f` searches what was said in every
  account's past conversations, newest first, and a hit resumes or hands off from
  there; `kizuna search` is the same from the command line. See
  [doc/session-search.md](doc/session-search.md).
- **Hand off when quota runs out.** `H` turns a conversation into a short brief
  and starts another account on it in the same folder — or copies the brief to
  the clipboard and opens that account, for a provider that cannot be started
  on a prompt.
- **Agents that find each other.** `kizuna peers` lists every running agent across
  profiles, with the repository and checkout it is in. `kizuna send` leaves one a
  message it reads at its next tool call. See
  [doc/agent-messaging.md](doc/agent-messaging.md).
- **Back after a power cut.** `kizuna resume-all` reopens every session a
  blackout killed, each in a ranma pane in its old workspace. See
  [doc/restore.md](doc/restore.md).
- **Shared setup.** Clone a profile, or copy MCP servers and skills between
  profiles, even when they use different providers.
- **Portable.** Export a profile as an `age`-encrypted bundle and import it
  on another machine.

Press `space` (or `?`) in the TUI for every action, with what each would act
on right now.

## Documentation

| Topic | Doc |
| ----- | --- |
| Creating, cloning, and moving profiles; app profiles | [doc/profiles.md](doc/profiles.md) |
| Installing CLIs, logging in, running, concurrency | [doc/running.md](doc/running.md) |
| The TUI: panels, keys, saved arguments | [doc/tui.md](doc/tui.md) |
| The expanded account: the glance and the sheet (`tab`) | [doc/board-expansion.md](doc/board-expansion.md) |
| Environment variables on a launch: defaults, `p`, who wins | [doc/launch-env.md](doc/launch-env.md) |
| The launch folder tree (`c`) | [doc/launch-folder-tree.md](doc/launch-folder-tree.md) |
| Resuming, hijacking, and handing off sessions | [doc/sessions.md](doc/sessions.md) |
| Copying MCP servers and skills | [doc/mcp-and-skills.md](doc/mcp-and-skills.md) |
| Seeing the active profile inside a CLI; OpenUsage; agent teams in ranma panes | [doc/integrations.md](doc/integrations.md) |
| Update check and self-update | [doc/updates.md](doc/updates.md) |
| Provider updates that miss the install that runs | [doc/shadowed-installs.md](doc/shadowed-installs.md) |
| How concurrent OpenCode instances work | [doc/opencode-concurrent-instances.md](doc/opencode-concurrent-instances.md) |
| Fresh OpenCode stores, resume by import, `kizuna compact` | [doc/session-store.md](doc/session-store.md) |
| Searching what was said across every account: `ctrl+f`, `kizuna search` | [doc/session-search.md](doc/session-search.md) |
| Agents finding and messaging each other: `kizuna peers`, `kizuna send` | [doc/agent-messaging.md](doc/agent-messaging.md) |
| Reopening every session a power cut killed: `kizuna resume-all` | [doc/restore.md](doc/restore.md) |
| Coming from ai-session: what the rename moved, and what it kept | [doc/rename.md](doc/rename.md) |
| Cutting a release, and the AUR packages | [doc/RELEASING.md](doc/RELEASING.md) |

## License

MIT. See [LICENSE](LICENSE).
