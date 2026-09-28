# A session store of our own, and a fresh OpenCode store per instance

Status: **approved 2026-09-28.** P0, P1 and P2 are built (below, "As built");
P3 is not yet. It changes the seeding decision in
[opencode-concurrent-instances.md](opencode-concurrent-instances.md) and the
readers in [sessions.md](sessions.md). Back to the [README](../README.md).

## Why

Measured on the workstation on 2026-09-28:

| Profile | `opencode.db` | `event` table | `session` + `message` + `part` | Sessions |
| --- | --- | --- | --- | --- |
| opencode3 | 5.0 GB | 4.9 GB | ~180 MB | 75 |
| opencode2 | 2.8 GB | (not broken down) | | |
| opencode | 172 MB | | | |

Every launch of an OpenCode profile copies that whole file (`VACUUM INTO`,
`seedIsolatedInstance`). For opencode2 the copy took about 55 s. A six-worker
wave on opencode3 would write 30 GB to `/` before any worker started.

Almost all of that is not conversation. It is opencode's `event` table, a
per-session sync log. `message.updated` events are 4.6 GB of it, because each
one embeds `info.summary.diffs`: the full patch of every file the session has
changed so far (71 files and single patches up to 190 KB in the largest). So
every update re-stores the whole cumulative diff, and a session that edits a
lot grows roughly quadratically. Instances never merge `event` back, so these
rows came from processes running **directly** on the profile store: headless
wave workers started with plain `opencode run`.

opencode reads `event` only for **workspace sync**: the `/sync/history`,
`replay` and `steal` endpoints, and moving a session to a remote workspace
(which fails with "No events found for session" if they are missing). Local
sessions live in `session`, `message` and `part`. No workspace exists on this
machine (`select count(*) from workspace` = 0).

## Goals

1. An OpenCode instance starts on an **empty store**. It carries its
   credentials, model and prompt history, but no copy of the profile's
   database.
2. **Resume and Handoff keep working** for every session they work for today.
3. ai-session keeps **its own index of sessions, for every provider**. It holds
   titles, folders, times and the spoken text. The recent lists, the preview
   pane, the handoff brief and search read the index instead of each
   provider's store.
4. **Antigravity** can join the index. Its titles live in a protobuf blob with
   no published schema, so it is decoded **once, when recorded**, never on a
   keypress.

Non-goals: storing a provider's full-fidelity conversation (tool calls, diffs,
reasoning) in our database. Resuming needs each CLI's own format, and
re-serialising opencode's rows would mean tracking a schema that moves under
us: its migrations include `reset_v2_session_state` and
`event_sourced_session_input`, both from 2026.

## Design

### 1. Fresh OpenCode instances

`seedIsolatedInstance` stops snapshotting `opencode.db`. The rest of the seed
is unchanged: `auth.json`, `model.json` and `prompt-history.jsonl` are still
copied, and the `.seeded` receipt is still written. opencode creates and
migrates the empty database on first open.

The objection recorded when full-copy seeding was chosen, "identity", does not
hold. `project.id` is derived from the repository, not assigned per database:
kumiko is `442e24a3…` and ranobe `1850ac3a…` in three independent profile
stores. So a session created in a fresh store links to the same project row
the profile already has, and the merge's `INSERT OR IGNORE` on `project` stays
a no-op.

The merge back is unchanged: the session subtree by id, credentials when newer,
never `event`. **The profile's `opencode.db` becomes the archive** of full
conversations. It is only ever written by merges, and it stops growing
events.

### 2. Resume imports one session

`opencode --session <id>` needs the session inside the store it opens, and a
fresh store has none. Before exec, a launch that reopens session `S` carries
`S` from the archive into the new instance store with opencode's own tools:
`opencode export S` against the archive, then `opencode import` against the
instance. Their JSON is the one format opencode promises to read back across
its own schema changes; a row copy would have to follow every migration it
ships. The import writes no `event` rows.

**Import re-homes a session to the folder it runs in**: `project_id` and
`directory` come from the import's working directory, not from the JSON. Run
from `/tmp`, a hansei session came back as project `global` in `/tmp`. So the
import runs in the session's recorded folder, which yields the same
`project_id` the archive has.

The export opens the archive through opencode, which migrates it on open, so it
runs under the profile's merge lock like every other writer. `-c/--continue`
imports the newest top-level session recorded for the launch folder, which is
what `--continue` would have found in a full copy. A launch that reopens
nothing (a headless `run`, a new session) imports nothing.

**A resumed session exists on both sides, so the merge now carries edits.**
The merge used to be a union: rows the archive already had were left alone.
That silently dropped a resumed session's new title and `time_updated` and any
message it rewrote. The session subtree (`session`, `message`, `part`,
`session_message`, `session_share`, `todo`) now merges newest `time_updated`
wins, the rule the credential tables already used. `project` stays
insert-only: a fresh store writes its own project row with default fields, and
"newer wins" would wipe the archive's icon overrides and commands with them.

**Schema drift.** Only merges and exports open the archive now, so an opencode
upgrade migrates every fresh instance store and not the archive. The merge
would then see different columns and skip `session`. Before merging, if the
instance store has applied migrations the archive has not, the merge lets
opencode migrate the archive first (`opencode db "select 1"`, about 1.3 s).

The rule "a session living only in a live instance cannot be resumed" stays
as it is.

### 3. `sessions.db`: the index

`~/.config/ai/sessions.db`, SQLite through the driver ai-session already uses:

```sql
CREATE TABLE session (
  profile     TEXT NOT NULL,
  provider    TEXT NOT NULL,      -- claude | codex | opencode | antigravity
  id          TEXT NOT NULL,      -- the provider's own id, what resume takes
  title       TEXT NOT NULL,
  folder      TEXT NOT NULL,
  created     INTEGER NOT NULL,   -- unix ms
  updated     INTEGER NOT NULL,
  source      TEXT NOT NULL,      -- transcript path, or store path, or conversation db
  fingerprint TEXT NOT NULL,      -- what "unchanged since last read" means for this source
  PRIMARY KEY (profile, id)
);
CREATE TABLE turn (
  profile TEXT NOT NULL,
  id      TEXT NOT NULL,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL,          -- user | assistant
  text    TEXT NOT NULL,
  PRIMARY KEY (profile, id, seq),
  FOREIGN KEY (profile, id) REFERENCES session ON DELETE CASCADE
);
CREATE INDEX session_recent ON session (profile, updated DESC);
```

`turn` holds exactly what the handoff brief and the preview pane already
reduce a conversation to: spoken text only, with injected preambles stripped.
Lineage (`lineage.json`) can move into a table later; it does not have to.

**Ingestion is incremental, by fingerprint:**

| Provider | Source | Fingerprint | When |
| --- | --- | --- | --- |
| Claude Code | transcript `.jsonl` | size + mtime | TUI refresh, after a launch exits |
| Codex | rollout `.jsonl` | size + mtime | same |
| OpenCode | profile archive, rows by `session.time_updated` | `time_updated` | right after an instance merges, and on refresh |
| Antigravity | per-conversation SQLite | mtime | same; protobuf decoded here, once |

The existing readers (`readClaudeTranscript`, `readCodexRollout`,
`readOpenCodeMessages`) become the ingesters. They already produce
`recordedSession` and `handoffMessage`, so the parsing moves rather than being
rewritten. A source whose fingerprint has not changed is skipped, so a
refresh costs a `stat` per file.

**Readers switch to the index:** `recentSessions`, `allRecentSessions`, the
picker's search, the preview pane and the handoff brief. Live instances still
come from the lock files. A session only in a live instance is shown from its
instance store, as today. The index can always be rebuilt from the sources:
deleting `sessions.db` costs one slow refresh, never data.

### 4. Shrinking the existing archives (one-off, and then a command)

`ai compact <profile>` (not `ai opencode compact`: a profile named `opencode`
exists, and `ai opencode …` already launches it):

1. Refuse while the profile has any live instance, or any `opencode` process
   whose `XDG_DATA_HOME` is the profile's data dir (a direct launch, the way
   the waves ran), found by reading `/proc/*/environ`.
2. Back up `opencode.db` next to itself (`opencode.db.<timestamp>.bak`) and say
   so.
3. `DELETE FROM event` and `DELETE FROM event_sequence`, then `VACUUM`. Both
   explicitly: the cascade from `event_sequence` needs `foreign_keys` on,
   which the connection does not have, and a test proves it does not fire.
4. Report the size before and after.

The cost is that those sessions can no longer be moved to a remote workspace.
Nothing here uses one. Expected result: opencode3 from 5.0 GB to about 0.2 GB,
from the table sizes above.

## Phases

| Phase | What | Visible result |
| --- | --- | --- |
| P0 | `ai compact`, and run it on opencode2 and opencode3 while no wave uses them | the archives shrink; seeding gets fast even before P1 |
| P1 | fresh instances plus resume import | no seed copy at all; `LAUNCHER=ai` becomes the wave default |
| P2 | `sessions.db`, and ingesters and readers for Claude, Codex and OpenCode | lists and previews read one indexed table |
| P3 | Antigravity ingester (protobuf decode) | Antigravity sessions in the pickers, and resumable |

P0 and P1 are independent of P2 and P3 and carry the disk and time win. P2 is
the refactor that makes P3 cheap.

## Tests

- P0: a seeded store with events compacts to one with none, keeps every
  `session`/`message`/`part` row, and passes `PRAGMA integrity_check`. It is
  refused with a live instance, and refused with a fake direct process
  (environ fixture).
- P1: a fresh instance store has no `session` rows. A resume imports exactly
  `S`'s subtree and no `event`. Merge after resume is a no-op for the
  unchanged rows and adds only the new ones. `--continue` imports the folder's
  newest session. `TestRealDBSeedAndMerge` gains a fresh-seed variant against
  the operator's real store.
- P2: ingestion is idempotent (two refreshes, one row each), an unchanged
  fingerprint reads nothing, and a transcript grows by one turn → one new
  `turn` row. The readers return the same lists as the pre-index readers on a
  fixture of all three providers (a golden comparison), and deleting
  `sessions.db` rebuilds it identically.

## Decisions (2026-09-28)

1. **The full OpenCode conversation lives in the archive**, the profile's
   `opencode.db`, which only merges and exports open. `sessions.db` holds the
   index and the spoken text, never opencode's raw rows.
2. **Headless sessions are indexed but hidden by default**, with a key in the
   pickers that shows them. A session is headless when it was launched with a
   `run` subcommand (the wave workers are). This is a filter, not a delete, and
   it belongs to P2.

## Open question

**Claude and Codex transcripts are never copied** into our store beyond their
text turns, and resume stays pointed at their files. Is that the line, or
should the index keep a pointer-only row for a transcript that has since been
deleted?

## As built (P1)

- `seedIsolatedInstance` copies `auth.json`, `model.json` and prompt history,
  and no store. `snapshotSQLiteDB` is gone with the copy.
- `cmd/ai/opencode_resume.go`: `openCodeResumeTarget` reads
  `-s/--session[=]` and `-c/--continue`. `prepareOpenCodeInstance` runs in
  both launch paths that take an instance (`launchProfileCommand`,
  `launchInFolder`) and never fails a launch: a session it cannot bring in is
  reported, and opencode then says it found none.
- `mergeNewerRows` (formerly `mergeAuthTable`) serves the credential tables and
  the session subtree. `archiveBehindInstance` compares the two stores'
  `migration` ids.
- `TestRealDBResumeImportAndMerge` (replacing `TestRealDBSeedAndMerge`) runs
  the real binary against a copy of the operator's real store: a fresh
  instance, an import that lands under the archive's `project_id` with no
  events, and a merge that carries the resumed session's newer row. It is
  skipped wherever the store or the binary is missing.

## As built (P0)

- `cmd/ai/compact.go`: `ai compact <profile>` takes the exclusive profile lock
  (refused while any instance runs) and then the merge lock, and refuses while
  any process has the profile's data dir as its `XDG_DATA_HOME`
  (`/proc/*/environ`, Linux only). It backs up with `VACUUM INTO` to
  `opencode.db.<UTC timestamp>.bak`, deletes the two event tables, vacuums, and
  reports the size before and after and the backup's path and size.

## As built (P2)

- `cmd/ai/index.go`: `session` (with `headless`), `turn`, and `turn_source`
  (the fingerprint the cached turns were read at). `recentSessions` refreshes
  the profile's rows and answers from the index; `scannedRecentSessions` is the
  old reader, kept as the fallback when the index cannot be opened.
- **Turns are cached on demand, not on refresh.** Materializing every turn of
  every transcript would have put 2 GB of Claude transcripts into the first
  refresh. A handoff reads a conversation whole and caches it; a preview uses
  the cache when it is fresh, and otherwise still reads the transcript's tail.
- **Each kind is limited separately** (`limitEachKind`), after live OpenCode
  sessions join. A single limit let a night of runs take every slot, and the
  first draft forgot to re-apply it after the live union
  (`TestUnionReaderSeesLiveInstancesOnce` caught that).
- Headless is hidden in both pickers and the expanded row; `.` toggles it.
- Measured on the workstation: 448 conversations (125 headless) over eight
  profiles, first build 335 ms, refresh 20 ms.
- The open question stands: rows whose transcript was deleted are dropped with
  it, not kept as pointers.
