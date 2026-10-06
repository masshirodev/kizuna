# Updates that miss the install that runs

Back to the [README](../README.md).

`ai update <profile>` and `u` in the TUI run the provider's own updater
(`claude update`, `opencode upgrade`, …). An updater updates *its* install,
which is not always the one the profile's command resolves to. The case that
prompted this: a box with the `claude-code-stable` pacman package in
`/usr/bin/claude` and the native installer's copy in `~/.local/bin/claude`,
with `~/.local/bin` later on `PATH` than `/usr/bin`. `claude update` moved the
native symlink to the new version, reported success, and every launch went on
running the package's old binary.

ai-session cannot know which install you meant to keep, but it can look at the
files on either side of the update. Before running the updater it lists every
distinct install of the profile's command: each `PATH` match in `PATH` order
(the first is the one a launch runs), then the provider installer's own
location in case that directory is not on `PATH` at all:

| Provider | Known install locations |
| --- | --- |
| `claude` | `~/.local/bin/claude`, `~/.claude/local/claude` |
| `opencode`, `deepseek` | `~/.opencode/bin/opencode` |

Paths that resolve to the same file count once, so `/bin` symlinked to
`/usr/bin` is not two installs. After a successful update it lists them again
and compares where each resolves and its size and modification time. It warns
— on stderr from `ai update`, in the status line from `u` — when:

- **another install changed and the running one did not**:
  `the update changed ~/.local/bin/claude, but claude runs /usr/bin/claude, which did not change — …`
- **nothing changed, but the running install shadows another one**: the
  second press of `u` in the case above, where the native copy is already
  current. `claude runs /usr/bin/claude, and another install at ~/.local/bin/claude is shadowed by it; …`

It stays quiet when the install that runs is the one that moved, or when there
is only one install. A failed update reports its error and skips the check.

The fix is yours to pick: remove the copy you do not want (`sudo pacman -Rns
claude-code-stable` in the case above), or reorder `PATH` so the one the
updater maintains comes first. The check lives in `cmd/ai/shadow.go`.
