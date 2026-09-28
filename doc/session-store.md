# A session store of our own, and a fresh OpenCode store per instance

Status: **proposed**, not built. It changes the seeding decision in
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
fresh store has none. Before exec, a resume of session `S` copies `S`'s
subtree from the profile archive into the new instance store. This is the
merge, run in reverse and filtered to one session: `ATTACH` the archive, then
`INSERT OR IGNORE` of `project` (`S`'s row), `session`, `message`, `part`,
`session_message`, `session_input`, `session_context_epoch`, `session_share`
and `todo` where `session_id = S`, in the merge's FK order and through the same
generic table walk, so a drifted table is skipped with a warning, not fatal.
It never copies `event`. On exit, `S` merges back like any other session, and
the `INSERT OR IGNORE` merge already handles a session that exists on both
sides.

`opencode --continue` (the fallback when nothing is recorded) imports the
newest session whose `directory` is the launch folder, which is what
`--continue` would have picked in a full copy.

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

`ai opencode compact <profile>`:

1. Refuse while the profile has any live instance, or any `opencode` process
   whose `XDG_DATA_HOME` is the profile's data dir (a direct launch, the way
   the waves ran), found by reading `/proc/*/environ`.
2. Back up `opencode.db` next to itself (`opencode.db.<timestamp>.bak`) and say
   so.
3. `DELETE FROM event_sequence`, which cascades to `event`, then `VACUUM`.
4. Report the size before and after.

The cost is that those sessions can no longer be moved to a remote workspace.
Nothing here uses one. Expected result: opencode3 from 5.0 GB to about 0.2 GB,
from the table sizes above.

## Phases

| Phase | What | Visible result |
| --- | --- | --- |
| P0 | `ai opencode compact`, and run it on opencode2 and opencode3 while no wave uses them | the archives shrink; seeding gets fast even before P1 |
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

## Open questions

1. **Where the full OpenCode conversation lives.** Proposed: the profile's
   `opencode.db`, as an archive only merges write. The alternative is raw
   opencode rows in `sessions.db`, which ties us to their schema (see
   Non-goals). Recommendation: the archive.
2. **Should wave workers be indexed at all?** A wave launches dozens of
   one-shot headless sessions that no one resumes. The index could mark
   sessions launched with `-p` and a `run` subcommand as `headless` and keep
   them out of the default list (a filter, not a delete).
3. **Claude and Codex transcripts are never copied** into our store beyond
   their text turns. Resume stays pointed at their files. Is that the line,
   or should the index also keep a pointer-only row for a transcript that has
   since been deleted?
