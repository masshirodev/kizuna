# AGENTS.md

kizuna is a launcher for several AI CLI accounts (Claude Code, Codex,
OpenCode, Antigravity), each profile isolated in its own state directory, plus
the TUI cockpit, session resume and search, and the messaging that lets agents
on different profiles find each other. It is a public Go module
(`masshirodev/kizuna`, MIT) packaged on the AUR. The `README.md` is the map;
each feature has its own document in `doc/`. Read `doc/DESIGN.md` before
changing behaviour: it records the decisions.

## Before any change

- **Do it right, not quick.** Between a way that works now and the way that
  holds, take the one that holds, even when it is more work. Say what the
  cheaper way would cost instead of quietly taking it.
- **kizuna never reads, prints, or stores token contents.** Every provider
  keeps handling its own logins; kizuna only points it at a directory.
- **The installed `kizuna` runs every agent session on this machine**, yours
  included. Do not reinstall or rebuild it from a branch to try a change; build
  into the worktree (`go build ./cmd/kizuna`) and run that binary instead.
- A decision that changes how the project works goes into `doc/DESIGN.md` in
  the same commit, and a feature's own document in `doc/` is updated with it.
- New screens get a brief in `doc/briefs/` and a design handoff before a plan
  is approved (`doc/briefs/README.md`).
- `AGENTS.local.md` is gitignored and describes this checkout on this machine.
  Read it when it exists; never commit it.

## Before a commit

```sh
test -z "$(gofmt -l .)" && go vet ./... && scripts/coverage.sh
```

The git hooks in `.githooks/` enforce this once `scripts/hooks.sh` has armed
them (once per clone; worktrees share it). `commit-msg` refuses
`Co-Authored-By` trailers, `Claude-Session` references and emoji. Make the
hooks pass; skip flags are for emergencies, not routine use. `doc/TESTING.md`
says what each check covers and where it runs.

**Coverage is held at 77%** of statements (Go measures no branches or
functions). A change that drops it below adds the tests in the same commit; it
does not lower the bar.

## Conventions

- Everything is `package main` under `cmd/kizuna`, one file per concern with
  its `*_test.go` beside it, on the standard `testing` package.
- The suite passes on a clean runner with no provider CLI installed: a test
  that needs a CLI present or absent says so with `stubCommands` or
  `scopePATH` (`install_test.go`) rather than trusting the machine
  (`doc/RELEASING.md`, "CI").
- A test that needs a directory uses `t.TempDir()`, and one that needs a
  profile tree points `XDG_CONFIG_HOME` into it, never at the real
  `~/.config/kizuna`.
- gofmt is the format and `go vet` the lint; there is no golangci-lint.
- Comments explain why, not what. No emoji in code or commits.
- Commit subjects are conventional (`feat(scope): ...`, `fix(...)`,
  `docs(...)`) and name the card they close, `(c189)`.
- Work is tracked on the **ai-session** board in Kumiko. Name cards by id and
  title together.
- **`main` is protected: every change goes through a pull request**, never a
  direct push. The owner merges.
- A release is a `v*` tag whose version matches `cmd/kizuna/version.go`;
  `doc/RELEASING.md` is the procedure, AUR packages included.
- This repository is held to the house conventions by hajime (`.hajime.json`);
  `hajime check` says what has drifted.
