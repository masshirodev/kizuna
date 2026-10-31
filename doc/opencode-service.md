# OpenCode 2.x and its background service

Back to the [README](../README.md). Builds on
[opencode-concurrent-instances.md](opencode-concurrent-instances.md) and
[session-store.md](session-store.md).

## What changed upstream

OpenCode 2.x (seen on 2.0.32) stopped running its server inside the process
that needs it. The TUI, `run`, `mini`, `models`, `stats`, `auth login` and
`session list|delete|export|import` all look for a **background service** and
start one when they find none:

- Discovery: `$XDG_STATE_HOME/opencode/service.json` (url, pid, password).
- The daemon: `opencode serve --service`, detached and reparented to init. It
  outlives whatever started it and keeps `$XDG_DATA_HOME/opencode/opencode.db`
  open for as long as it lives.
- Its port: pinned on first start in `$XDG_CONFIG_HOME/opencode/service.json`
  and reused on every later start (`opencode service get|set port`).

## Why that breaks a kizuna profile

A concurrent OpenCode instance gets a fresh `XDG_STATE_HOME` and shares the
profile's `XDG_CONFIG_HOME` (opencode-concurrent-instances.md, "Design"). So
every launch finds no discovery file, and every launch reads the same pinned
port. Each one starts another daemon on a port an earlier daemon still holds:

```
Managed service port 49375 on 127.0.0.1 is already in use by another process.
Configure another port with `opencode service set port <port>` and start the
service again.
```

A daemon that does start is worse than one that fails. It holds the
instance's store open after the TUI has exited and the merge-back has run, or,
when the session import's `export` started it, it holds the profile's archive
open under every later merge. Found on 2026-10-26: one orphaned daemon on the
`opencode` profile's archive, and one on the machine's ordinary store.

Setting a different port per instance would only trade the collision for one
leaked daemon per launch.

## What kizuna does

It passes `--standalone`, which gives the 1.x shape back: a private server
inside the process, gone when the process is. OpenCode has no environment
variable or config key for it (the binary only knows the flag), so
`withOpenCodeStandalone` (`cmd/kizuna/opencode_service.go`) adds the flag at
each place kizuna runs the profile's binary: the TUI and CLI launches, the
exclusive ones (`kizuna <profile> login`), and resume-in-folder.

- **Where it goes.** First for the bare TUI, after the subcommand words for
  the rest: `run --standalone …`, `session export --standalone <id>`,
  `auth login --standalone`.
- **Where it does not.** Subcommands that never reach the service (`upgrade`,
  `mcp`, `service`, …), and `api`, `reload` and `pair`, which
  exist to talk to a running service and are left to whoever typed them. An
  invocation that already says `--standalone` or `--server` keeps its choice.
  Other tools a profile runs under its environment (openusage) never get it.
- **Which binaries.** 1.x rejects the flag, so kizuna asks the binary
  (`opencode --version`, about 0.1 s) once per resolved path per process, and
  only an answer of 2 or later turns the flag on. A binary that does not
  answer counts as 1.x: a missing flag brings back the daemon, a wrong one
  breaks every launch. DeepSeek profiles run the same binary and get the
  same treatment.

On 2.2.5 every `auth` verb (`list`, `logout`, `export`, `import`, `switch`)
reaches the service, not only `login`, and all of them get the flag. The
credential seed a launch runs (`auth export | auth import`,
[opencode-credentials.md](opencode-credentials.md)) passes it itself.

The session import that a resume runs (session-store.md) moved too: 2.x
spells it `session export` and `session import`, and its import takes
`--directory`. `openCodeSessionToolArgs` picks the spelling by the same
probe.

## Cleaning up after the old behaviour

The fix stops new daemons. It does not stop ones already running, and it
leaves the pinned port in `config/opencode/service.json`. The port file is
harmless: a standalone run never reads it. To stop a leftover daemon, run
OpenCode's own stop under the environment that started it:

```sh
P=~/.config/kizuna/profiles/<profile>
XDG_CONFIG_HOME=$P/config XDG_DATA_HOME=$P/data XDG_STATE_HOME=$P/state \
  opencode service stop
```

`ss -ltnp | grep opencode` lists the ones still listening, and
`/proc/<pid>/environ` says which store each one serves.

## The 2.x schema

2.x also moved sessions to new tables: `session_v2` for the sessions and
`session_message` for their turns, one row a turn ordered by `seq`. A fresh
2.x store has no `session`, `message`, `part` or `todo` at all; a store 2.x
migrated from 1.x keeps both, and the v2 tables are the superset. On
2026-10-28 the `opencode` profile's archive held 139 legacy sessions, every
one of them also in `session_v2` beside 10 newer ones, and the legacy tables
had not moved since 2026-10-14.

Every reader asks the store which it has and prefers v2
(`cmd/kizuna/opencode_schema.go`): the session list and its headless marks,
the resume lookups (the folder a session ran in, the newest one in a folder,
whether the archive holds it), the freshest copy of a live session, the
conversation read for search, previews and handoff briefs, and the
`sqlite3` command a brief hands over. A 1.x store still reads as before.

The merge carries `session_v2` newest `time_updated` wins, like the legacy
session table, and counts the sessions of whichever session table both stores
share. It also compares columns as a set: a migrated archive orders
`session_v2` and `project` differently from a fresh instance store (a
migration's columns come last), and the ordered comparison skipped both as
"schema drift" whenever the two orders differed. The merge still succeeded,
so the instance store was retired with its sessions never carried into the
archive. Which past launches that hit is not known (no instance store
survives to check); from this fix on it cannot happen.

`TestRealDBResumeImportAndMerge` runs the real binary on a copy of the real
store (the original is only read) and checks the import, kizuna's readers on
the instance, and the merge back, against whichever schema the store has.
