# Update check and self-update

Back to the [README](../README.md).

## Building

The binary is `ai`, built from `cmd/ai`. `go install ./...` produces the same
thing in `$(go env GOPATH)/bin`.

The build must not pass `-buildvcs=false`: the commit stamp the Go toolchain
embeds is what `ai version` and the TUI's update check compare against the
repository, and a binary built without it can only report that it does not know.

`ai self-update` does not rebuild from this checkout — it keeps its own clone
at `~/.config/ai/repo` and rebuilds from that instead (see the rest of this
page), so wherever you happen to run `install.sh` from doesn't
matter to it either way.

The TUI asks GitHub whether the build is behind the repository as its main
screen appears, and says so in the title bar when it is:

```
↑ 3 commits behind main · U
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

Press `U`, or run `ai self-update`, to apply it. Both rebuild from a dedicated
clone at `~/.config/ai/repo` — not this checkout, and not wherever `install.sh`
happened to run from — so updating never depends on where you keep your own
copy. It is created on first use and kept on `main` from then on:

```
› git clone git@github.com:masshirodev/ai-session.git ~/.config/ai/repo   # only if missing
› git checkout main                                                       # only if not already there
› git pull --ff-only
› sh ./install.sh
```

The first two lines are silent when there is nothing to do — most updates are
just the last two. `install.sh` is preferred over a bare `go install` so there
is one definition of how `ai` is built, including the VCS stamp the update
check itself depends on. The pull is fast-forward-only, and a refused pull
stops there: rebuilding after it would reinstall the build that is already
running and report it as an update.

The update is a keypress rather than something that happens on its own — the
check is automatic, replacing your own binary without asking is not. Once it
does run, though, finishing it is not left half-done: on success `ai` reopens
itself, so the session that asked for the update ends up running the binary
it just built rather than the one still sitting in memory. `ai self-update` at
a shell prompt lands you in the TUI; `U` from inside the TUI ends back in the
TUI, just on the new build. A failed update does not reopen anything — the
error stays on screen.

`AI_SOURCE_DIR` overrides the managed clone with an arbitrary checkout
instead, for testing self-update itself against a branch that has not been
pushed yet. It is deliberately not persisted anywhere, so it cannot be set
once and forgotten: set it only for the invocation that needs it.

```sh
AI_SOURCE_DIR=~/projects/ai-session ai self-update
```

A directory without a `go.mod` and a `.git` is refused rather than pulled and
built in, same as the managed clone. Unlike the managed clone, an
`AI_SOURCE_DIR` override is never switched to `main` on your behalf — it is
there so you can point at whatever branch you are testing.
