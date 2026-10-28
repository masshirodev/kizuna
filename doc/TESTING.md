# Testing

## Suite

`go test ./...`: Go's own runner, nothing to install. Everything is
`package main` under `cmd/kizuna`, with each file's tests beside it as
`*_test.go`.

The suite must pass on a clean runner, which has none of the provider CLIs
and none of a workstation's processes: a test that needs a CLI present or
absent says so with `stubCommands` or `scopePATH` (`install_test.go`), and a
test that needs a profile tree points `XDG_CONFIG_HOME` at a `t.TempDir()`.

`realdb_verify_test.go` is the one exception on purpose: it copies the
operator's real `opencode` profile store and drives the real `opencode`
binary, and skips wherever either is missing (CI included). It is the only
test that sees OpenCode's true schema, so a failure there is a finding about
kizuna against the installed OpenCode, not noise to skip.

## Commands

| Command                   | What                                                 |
| ------------------------- | ---------------------------------------------------- |
| `go test ./...`           | The suite                                            |
| `scripts/coverage.sh`     | The suite, failing below 77% of statements           |
| `test -z "$(gofmt -l .)"` | Format check (`gofmt -l` exits 0 even when it lists) |
| `gofmt -w .`              | Format, writing                                      |
| `go vet ./...`            | Vet                                                  |

## Coverage

**The floor is 77%** of statements, set once as `FLOOR` in
`scripts/coverage.sh` and enforced at every push. A change that lowers it adds
tests in the same commit; the floor is not lowered to make a push pass.

Go measures **statement coverage only**. It has no branch or function
coverage, so the 77% here is not the lines-branches-functions floor of the
Node repositories: an `if` whose `else` path is never taken can be fully
covered. A test per branch is the habit that stands in for what the tool
cannot see.

The floor was measured when hajime adopted the repository (2026-10-28): 77.9%
of statements on a clean environment like CI's, 78.0% on the workstation,
rounded down to 77. It only goes up. Each package is credited only by its own
tests, and nothing is excluded. The profile is written to
`coverage/coverage.out` (gitignored); `go tool cover -html=coverage/coverage.out`
shows it line by line.

## Where each check runs

| Check                        | pre-commit | pre-push | CI  |
| ---------------------------- | ---------- | -------- | --- |
| gofmt, go vet                | tree       |          | yes |
| Agent instruction budget     | yes        |          |     |
| House rules (`hajime check`) |            | yes      |     |
| Hooks armed                  |            | yes      |     |
| Suite + 77% coverage    |            | yes      | yes |

## Git hooks

| Hook         | Runs                                                                        | Skip flags                                       |
| ------------ | --------------------------------------------------------------------------- | ------------------------------------------------ |
| `pre-commit` | `gofmt -l`, `go vet ./...`, `agent-docs-budget`                             | `SKIP_FORMAT SKIP_LINT SKIP_AGENT_DOCS SKIP_ALL` |
| `pre-push`   | `scripts/hooks.sh --check`, `scripts/coverage.sh`, `hajime check --offline` | `SKIP_HOOKS SKIP_TESTS SKIP_HAJIME SKIP_ALL`     |
| `commit-msg` | refuses co-author trailers, `Claude-Session`, robot emoji                   | `SKIP_COAUTHOR SKIP_ALL`                         |

The hooks live in `.githooks/`, which git only runs once `core.hooksPath`
points at it. A clone does not carry that setting: run `scripts/hooks.sh`
once per clone, and `scripts/hooks.sh --check` says whether it has been.

`pre-push` clears git's own hook environment before any step
(`git rev-parse --local-env-vars`). Several tests make a repository with
`git init` in a `t.TempDir()`; with `GIT_DIR` inherited from the hook they
acted on kizuna's own repository instead, set `core.bare` on the main
checkout and committed onto the branch being pushed.
