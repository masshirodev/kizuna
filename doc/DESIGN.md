# Design

What kizuna is for, what it deliberately is not, and the decisions that shape
it. A change that contradicts a decision here changes this file in the same
commit. Each feature's own reasoning lives in its document in `doc/` (the
README's table lists them); this file holds what cuts across them.

## Goals

- Several accounts of each AI CLI on one machine, never sharing a credential
  file: each profile gets its own state directory, set through the variable
  its provider already honours (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, the XDG
  triple for OpenCode, a profile-local `HOME` for Antigravity).
- One place to see every account: quota headroom, login state, what it is
  running, and its recent conversations, resumable from the folder they ran in.
- Agents on different profiles that can find and message each other
  (`doc/agent-messaging.md`).

## Non-goals

- **Handling credentials.** kizuna never reads, prints, or stores token
  contents; each official CLI does its own login and refresh.
- Replacing a provider's CLI or its configuration format. kizuna points a CLI
  at a directory and launches it; what is inside is the CLI's.

## Decisions

- **Pure Go, static binary.** SQLite comes from `modernc.org/sqlite`, so a
  release is built with `CGO_ENABLED=0` and runs on any Linux
  (`doc/RELEASING.md`).
- **Packaged builds never update themselves**; a build from source does
  (`doc/updates.md`).
- **CI runs on GitHub Actions**, not the workstation runner the private
  repositories use: the repository is public, so the minutes are free.
- **`main` is protected**; every change is a pull request the owner merges.
- **Git hooks live in `.githooks/`**, armed by `scripts/hooks.sh`, not husky:
  there is no `package.json` to hang husky on. Until hajime adopted the
  repository (2026-10-28), `core.hooksPath` named a `.husky/` that `main` did
  not have, so no hook ran.
- **Coverage is held at a floor that only rises** (77% of statements when it
  was set; `doc/TESTING.md`).
