# Concurrent OpenCode instances: per-instance data dirs with merge-back

## Goal

Let one `opencode` profile run in N terminals at once, the way `codex` and
`claude` profiles already do — without sharing one `opencode.db` between live
processes.

Non-goals:

- Sharing a single `opencode.db` between concurrent processes. Upstream does
  not support it (`SQLiteError: locking protocol`, `SQLITE_BUSY` with
  `busy_timeout=0`, two TUIs silently sharing one session, init deadlocks —
  see `anomalyco/opencode#15188`, `#21215`, `#21443`, `#29395`, `#31307`).
  The launcher must not reintroduce this by flipping
  `supportsConcurrentRuns` (`cmd/ai/instances.go:52`) to include `opencode`.
- Covering `deepseek` profiles. They run the `opencode` binary but get no
  isolated XDG dirs from `profileEnv` (`cmd/ai/main.go:522`), so there is no
  per-profile DB to isolate or merge. Unchanged: exclusive, unmerged.
- Live two-views-on-one-session (`h` hijack of a running OpenCode session).
  Hijack reopens the conversation while the original keeps running, which for
  Claude/Codex is safe (append-only `.jsonl` transcripts) and for OpenCode
  means two writers on one DB again. Concurrent runs are for *distinct*
  sessions; hijack of a live OpenCode instance stays refused.

## Current layout (what the spec builds on)

Per profile (`~/.config/ai/profiles/<name>/`):

- `config/opencode/` — `opencode.json[c]`, plugins, `node_modules`. Read-mostly
  during a run. `profileConfigPaths` (`cmd/ai/clone.go:60`) already treats only
  this tree as "configuration".
- `data/opencode/` — `auth.json`, `opencode.db` (+ `-wal`/`-shm`), `log/`,
  `repos/`. The contention lives here.
- `state/opencode/` — `model.json`, `prompt-history.jsonl`, `kv.json`,
  `locks/`. Append/overwrite at runtime; must not be shared either
  (`prompt-history.jsonl` interleaving, opencode's own `locks/`).
- Locking: `acquireProfileRunLock` (`cmd/ai/instances.go:59`) sends OpenCode
  down `acquireExclusiveRunLock` (`workdir/.active.lock`); a second launch
  fails. `TestOpenCodeKeepsExclusiveRunLock` (`cmd/ai/main_test.go:460`)
  pins this. Login/integration/export use `acquireProfileLock`, which refuses
  while any instance lock exists.
- Session reading: `opencodeSessions` (`cmd/ai/sessions.go:230`) queries the
  profile DB's `session(id, title, directory, time_created)` directly.
  `describeInstances` (`cmd/ai/sessions.go:37`) leaves OpenCode undescribed;
  resume is `opencode --continue`, reopen is `opencode --session <id>`
  (`cmd/ai/main.go:456,474`).

## Design: one data dir per instance, merge on exit

Each launch gets the existing `instances/run-XXX/` directory (PID lock +
`instance.json`), extended to also hold that instance's private OpenCode
storage:

```
profiles/<name>/
  .active.lock              # still used ONLY by exclusive ops (login etc.)
  instances/run-XXX/
    .active.lock            # this instance's liveness (as today)
    instance.json           # folder + started (as today)
    data/opencode/          # XDG_DATA_HOME for this launch
      auth.json             # copy of the profile's at launch
      opencode.db           # SEEDED copy of the profile's at launch
    state/opencode/         # XDG_STATE_HOME for this launch (fresh)
```

And the launch env becomes, for OpenCode concurrent launches only:

- `XDG_CONFIG_HOME=<profile>/config` — still shared. Config reads are
  concurrent-safe in practice; plugin installs while running stay racy, same
  as two shells editing one file. Document, don't solve.
- `XDG_DATA_HOME=<instance>/data` — private per instance.
- `XDG_STATE_HOME=<instance>/state` — private per instance.

Exclusive operations (`ai login`, `ai integrate`, export, `runLockedCommand`)
keep using the profile-wide `.active.lock` and the profile dirs, and keep
refusing while any `instances/run-*/` lock is live (no change to
`acquireProfileLock`).

### Launch sequence

1. `acquireProfileInstance(workdir)` as today (rename-staging + exclusive-lock
   check), so login-vs-run mutual exclusion is preserved.
2. Checkpoint + seed: `sqlite3 profile.db "PRAGMA wal_checkpoint(TRUNCATE);"`
   best-effort, then copy the profile `opencode.db` file (plus `auth.json`)
   into the instance dir. No profile instance may be running at seed time
   except other *isolated* instances — the profile DB itself is only ever
   opened by merges and readers, never by a live TUI after this change, so
   the copy is stable. If the profile DB is missing (never run), skip the
   copy and let opencode migrate a fresh DB.
3. Fresh `state/opencode/` in the instance dir.
4. Exec with the per-instance XDG env. On exit (or `K` kill, or crash
   reclamation): merge, then delete the instance dir.

Seeding from a full copy (rather than an empty DB) is what keeps identity
stable: `project`/`workspace` row ids match the profile DB, so sessions the
instance creates link to the same projects, and `--continue` inside the
instance still sees the profile's history up to launch time. Sessions created
*after* launch in another instance are invisible until merged — accepted,
documented limitation (same as the upstream per-`XDG_DATA_HOME` workaround).

### Crash recovery

A dead instance's dir (stale `.active.lock`, reclaimed by
`activeProfileInstanceLocks` as today) still holds an unmerged DB. Never
delete it silently. Next launch or next TUI refresh surfaces
`run-XXX has an unmerged session store` and offers merge; a new
`ai opencode merge <profile>` (name TBD) merges all quaratined instance DBs
explicitly. Merge must open the instance DB through SQLite (WAL recovery runs
automatically) rather than file-copying it.

## DB merge

### Table inventory (from a live 1.x `opencode.db`)

19 tables. Merge set vs never-merge set:

Merge (session subtree + the project stubs it FK-references):

- `project`, `project_directory`, `workspace` — `INSERT OR IGNORE` by PK.
  Seeded copies make these almost always no-ops; they cover the case where
  the instance visited a directory the profile had never seen.
- `session` + `message` + `part` — the conversation itself.
- `session_message`, `session_input`, `session_context_epoch`,
  `session_share` — per-session runtime/derived state, FK-chained to
  `session.id`.
- `todo` — check: if rows key on session, merge with the subtree; if global,
  merge `OR IGNORE`. Confirm against schema at implementation time
  (this table was not in the inspected dump's FK list).

Never merge:

- `account`, `account_state`, `control_account`, `credential` — identity and
  secrets. The instance's copies are launch-time snapshots; merging them back
  would replay stale OAuth rotations over the profile's live tokens.
- `auth.json` (file, not a table) — same reason; see below.
- `migration`, `data_migration` — schema bookkeeping; the profile DB is
  migrated by opencode itself whenever it opens it.
- `event`, `event_sequence` — ephemeral coordinator state; merging replays
  already-consumed sequences.
- `permission` — project-scoped rules that also live in config; merging
  risks duplicating rows the user already changed via the profile.

### Algorithm

For one instance DB `I` into profile DB `P`, under a profile merge lock
(a new `profiles/<name>/.merge.lock` PID lock, so two instances exiting at
once serialize; `acquireProfileLock` exclusive ops also take it):

1. Open `P`, `ATTACH I AS incoming`.
2. In FK order (`project` → `project_directory` → `workspace` →
   `session` → `message` → `part` → `session_message` → `session_input` →
   `session_context_epoch` → `session_share` → `todo` if applicable):
   `INSERT OR IGNORE INTO main.<t> SELECT * FROM incoming.<t>;`
   IDs are UUIDs; a collision means "already merged", and `OR IGNORE` makes
   re-merge idempotent. No snapshots or delta bookkeeping needed.
3. `DETACH`, checkpoint `P`, close. Only then delete the instance dir.

Idempotency matters: merge-then-crash-before-cleanup must be safe to retry,
and a quarantine re-merge after a partial failure must not duplicate rows.

### `auth.json` and `state/` merge-back

- `auth.json`: content-compare with the profile's. Identical → skip (no
  write, no mtime churn). Differ → the instance refreshed OAuth mid-run:
  back up profile `auth.json` to `data/opencode/auth.json.<timestamp>.bak`
  and copy the instance's over (last-writer-wins). Log the backup path.
  API-key auth (`{"type":"api",...}`, e.g. the current `opencode-go` key)
  never refreshes itself, so this path stays quiet for those profiles.
- `state/opencode/model.json`: last-writer-wins copy, same content-compare
  skip. `prompt-history.jsonl`: append instance-only lines (compare by line
  count/order against profile's launch-time content — simplest: append lines
  not already present as a trailing suffix match; on ambiguity, keep the
  profile's and log it). `kv.json`, `locks/`: do not merge. `log/`: move
  instance logs under the profile `log/` with an instance prefix for
  debuggability rather than merging.

## Reader and TUI changes

- `recentSessions` / `opencodeSessions`: union the profile DB with every
  live (and quarantined-unmerged) instance DB, newest-first, honoring the
  existing limit. Reads are plain `SELECT`s on distinct files — no lock
  contention by construction. Dedup by session id (merged-then-not-yet-
  cleaned rows appear twice transiently).
- `describeLiveInstances`: unchanged shape; OpenCode rows now resolve via
  the union read above (launch folder match), which also finally gives
  OpenCode instances titles in the live panel.
- `h` hijack: stays refused for OpenCode *live* instances, with the message
  updated to say why (two writers on one session is exactly the shared-DB
  bug). `R` resume of a *merged/stopped* session works via the profile DB
  as today.
- `K` kill of an OpenCode instance triggers the same merge-then-cleanup as
  natural exit. Killing the tmux server (`stopTmuxServer`) must precede the
  merge so opencode has closed the DB (WAL checkpoint on clean close).
- Status/usage text updates: `main.go:941` usage line
  (`Concurrent runs: ...`), README sections "Run a profile" (exclusive list),
  "Resuming and hijacking" (hijack refusal), and the `model`/`AUTH` notes —
  plus this doc linked from the README's OpenCode rows.
- `describeInstances` keeps leaving OpenCode to the log-based path (no
  `claude agents`-equivalent exists); no subprocess lookup added.

## Tests (update + add)

- Update `TestOpenCodeKeepsExclusiveRunLock` → concurrent acquisition test
  mirroring `TestCodexAndClaudeUseIndependentRunLocks`, plus
  "instance env points XDG_DATA/STATE_HOME at the instance dir, config at
  the profile".
- Merge tests on seeded SQLite DBs (extend `seedOpenCodeSession`-style
  helpers to the real `session`/`message`/`part` columns): disjoint sessions
  merge fully; already-merged re-run is a no-op row-count-wise; FK order
  holds (no `FOREIGN KEY` failure); `account`/`credential`/`event*` rows
  never cross; two instances exiting concurrently serialize (merge-lock
  test).
- Union-reader tests: profile + two instance DBs → newest-first, limit
  honored, dedup by id.
- Crash-recovery test: instance dir with stale lock + unmerged DB is kept
  (not reclaimed by `activeProfileInstanceLocks` cleanup) and reported.
- `auth.json` merge-back: identical → untouched mtime; refreshed →
  backup + replace.

## Rollout

Single rollout with quarantine-first semantics: auto-merge on clean exit,
quarantine + surfaced warning on anything else, explicit merge command for
recovery. No silent data loss in any path — an unmerged instance dir is data,
and only merge success deletes it. Phased alternative (concurrent runs first
with merge deferred) is rejected: it strands sessions in throwaway dirs with
no path back, which is the documented downside of the raw upstream
workaround this spec exists to fix.

## Open questions (resolve before implementing)

1. `todo` table keying: per-session (merge with subtree) or global
   (`OR IGNORE`)? Read the FKs at implementation time.
2. `XDG_CACHE_HOME`: opencode's models cache resolves independently of the
   data dir per the upstream workaround — confirm no per-instance override
   needed so instances don't re-fetch.
3. Config-write contention: two instances editing `opencode.json` (e.g.
   `/model` default change) last-writer-wins silently. Accept + document,
   or take a shared flock around config writes? Lean: document.
4. `opencode serve` mode under a profile: same isolation should apply, but
   long-lived servers make "merge on exit" rare — quarantine surfacing must
   cover them or serve stays exclusive. Lean: serve stays exclusive.

## Implementation notes (as built)

- Seed is a full copy via `VACUUM INTO` (transactionally consistent even
  mid-merge, WAL folded in), not a table subset — so project ids, history,
  and OAuth rows all match the profile, and there is no per-table schema
  bookkeeping at seed time. Cost: one full copy per launch.
- The seed receipt (`.seeded`, with seed hashes of `auth.json`/`model.json`
  and the history size) doubles as the merge contract: no receipt means a
  failed seed the scanner may delete; a receipt means user data the scanner
  must never delete.
- No `.seeding` marker was needed: the staging lock survives the rename as
  the instance's liveness lock, so a mid-seed directory already reads as
  active to every scanner.
- Claim-by-rename (`run-XXX.merging-<pid>`) serialises concurrent mergers
  (kill racing natural exit, two stray scans); the merge itself is
  idempotent, so the loser backing off is an optimisation, not a
  correctness requirement.
- Credential tables merge as UPDATE-newer + INSERT-missing, not UPSERT:
  `INSERT .. SELECT .. FROM .. ON CONFLICT` does not parse (the `ON` binds
  to the `FROM` as a join constraint), and neither does a schema-qualified
  UPSERT target.
- The merge pins `SetMaxOpenConns(1)`: `ATTACH` is per-connection, and a
  pooled query landing on a second connection sees no `incoming`. The
  session count runs before `Begin` for the same reason.
- `todo` merges `OR IGNORE` with everything else; `permission` too. A
  drifted table is skipped with a warning naming it.
- Resume of a session that only exists in a live instance is refused with
  the reason (`opencodeSessionInProfileDB`); the picker still shows it.
- `TestRealDBSeedAndMerge` seeds and merges a copy of the operator's real
  `opencode.db` when present (skips otherwise), guarding the generic table
  walk against true schema drift.
