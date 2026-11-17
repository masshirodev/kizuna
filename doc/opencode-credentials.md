# OpenCode credentials in a fresh instance

Back to the [README](../README.md). Builds on
[session-store.md](session-store.md) and
[opencode-service.md](opencode-service.md). Card c194.

## The bug

OpenCode 2.x keeps what `/connect` and `auth login` store in `opencode.db`
(the `credential`, `account` and `control_account` tables), not in
`auth.json`. An instance starts on an empty store (session-store.md, "Fresh
OpenCode instances"), and the seed copied only `auth.json`, `model.json` and
prompt history. So a provider connected inside one instance merged back into
the profile's archive on exit, then never reached the next instance: every
launch came up without it. Each time it was added again, the archive got one
more row for the same provider.

Seen on 2026-10-31 on the `opencode` profile: Anthropic, connected at 18:51,
was in the archive and missing from the instance launched after it.
`opencode-go` survived only because its key predates 2.x and still lives in
`auth.json`.

## What kizuna does

**At launch** (`seedOpenCodeCredentials`, `cmd/kizuna/opencode_credentials.go`,
called first in `prepareOpenCodeInstance`, which every launch path that takes
an instance runs; see "The TUI path" below):

1. Skip unless the binary is 2.x (1.x keeps credentials in `auth.json`,
   which the seed already copies) and the archive has a credential row.
2. Under the profile's merge lock, read each archive credential row's primary
   key and `time_updated`. Values are never read.
3. Run `opencode auth export --standalone` against the archive with its
   stdout wired straight into `opencode auth import --standalone` on the
   instance store. This is opencode's own format, the same choice as the
   session import, and the secrets go from one opencode process to the other
   without passing through kizuna.
4. Record in the `.seeded` receipt (`credentials`) each row present on both
   sides: table, key, the archive's `time_updated`, and the instance's right
   after the import.

A failure is reported on stderr and the launch goes on, as a failed session
import does.

**At merge** (`settleSeededCredentials`, run before the newest-wins pass over
each credential table). For every recorded row:

| In the instance                  | What happens                                                                                           |
| -------------------------------- | ------------------------------------------------------------------------------------------------------ |
| still there, stamp unchanged     | not merged: the archive's row stands, even if another instance refreshed it meanwhile                 |
| gone                             | a logout: the archive's row is deleted, unless its `time_updated` moved since the export              |
| restamped                        | a real change (an OAuth refresh): left to the newest-wins pass                                         |

Rows the instance created itself are not in the receipt and merge
newest-wins, as before. So does every row of an instance launched by a kizuna
from before this change, whose receipt lists no credentials.

## Duplicate logins from auth.json (c199)

Seen on 2026-11-17: OpenCode's "Connected accounts" list for OpenCode Go
showed the same key four, then seven times. The `opencode` profile's
archive held seven `opencode-go` rows for four keys, and `opencode2` held
five rows for one key, one more for every launch since 2026-11-16.

OpenCode 2.x has a one-time schema migration,
`20260805200742_import_legacy_credentials`. On a store's first open it reads
`auth.json` and adds a credential row, under a new id, for each provider the
store has no row for. Every profile here ran it on 2026-10-24, folding its
pre-2.x `opencode-go` key in. But the seed still copied `auth.json` into
each instance, and the instance's store is created by the credential import
(step 3 above), so the migration ran there too, on a store that was still
empty: a second row for the same key, created a second before the import
wrote the archive's copy. The receipt cannot match it (no archive row shares
its id), so the merge treated it as a row the instance made and added it to
the archive. Fixed on 2026-11-17.

**What kizuna does now:** `seedOpenCodeCredentials` renames the instance's
`auth.json` to `auth.json.kizuna-aside` for the length of the import and
renames it back after, whether the import worked or not. The migration runs
during the import, finds no `auth.json`, and is recorded as done in the
instance's store, so it never runs there again. Nothing is lost by it: the
export opens the archive with `auth.json` in place, so whatever that file
holds is already among the archive's rows, and the import carries them. The
file comes back unchanged, so the receipt's `auth_sha256` stays true and the
merge leaves the profile's copy alone; it stays for whatever OpenCode still
reads from it at run time (the GitLab provider does).

When the rename fails, the import goes ahead anyway and the failure is
reported: one duplicate row is a lesser harm than an instance without the
profile's providers. A profile with no stored credentials runs no import,
and its first launch folds `auth.json` in inside the instance, which the
merge then carries back once, as before.

**What it does not do:** it removes no duplicate already in an archive.
Deciding that two rows are the same login means comparing their secrets,
which kizuna never reads. They are removed by hand, from OpenCode's own
account list, or once with SQL against the archive while no instance of the
profile runs.

## The TUI path

The first version of this fix ran only for `kizuna <profile>` and `resume`
from a shell. A launch from the TUI (Enter, `p`, a hijack, a reopen) builds
its command in `cmd/kizuna/tui.go`, apart from `launchProfileCommand`, and
that code never called `prepareOpenCodeInstance`. So once installed, the fix
changed nothing for the way the profile is actually opened: on 2026-10-31 an
instance launched from the TUI, on the fixed binary, came up with no
`credentials` in its receipt and no Anthropic, though the export and import
worked when run by hand against copies of the same stores. The same gap had
kept a session chosen in the TUI out of its fresh instance since c115.

`tuiLaunch` now builds the TUI's launch and gives it a `prepare` step, which
`trackedExecCommand` runs just before starting the command. It runs there,
not while the launch is built, so the second or two it takes is spent after
Bubble Tea has released the terminal (the picker does not freeze), and a
"could not bring … credentials" message reaches the terminal as it does from
a shell. A login runs under the exclusive lock, has no instance, and gets no
`prepare` step.

## Why the receipt is needed

`auth export` carries no timestamps, and `auth import` stamps every row it
writes with the time of the import. Without the receipt, an untouched copy
would always look newer than the archive's row. Picture A launching, then B,
then A refreshing an OAuth token and exiting. B's stale copy, stamped at B's
launch, would beat A's refreshed token at B's merge, and with refresh-token
rotation that logs the profile out. The receipt is the stamp version of the
seed hashes `auth.json` already uses: a file merges back only when the
instance changed it.

Logouts need it too: `auth logout` deletes the row, and the additive merge
cannot carry a missing row. Without the receipt, a provider removed in an
instance would come back at the next launch.

## Cost

About 1.7 s per launch on this machine (two opencode processes, both
`--standalone`), paid only by profiles that have stored credentials. The
alternatives were worse. Copying rows straight from the archive would need
the instance store to exist first, which means one opencode start anyway,
and would have to track every schema change opencode ships. Copying the
archive's schema by hand would bring back the full-copy seed's fragility.

## Also changed

On 2.2.5, every `auth` verb (`list`, `logout`, `export`, `import`, `switch`)
reaches the background service, not only `login`, so
`openCodeStandaloneCommands` gives them all `--standalone`
(opencode-service.md).

## Tests

- `TestLaunchCarriesTheProfilesCredentialsIntoTheInstance`, with a stand-in
  binary: the pipe, the receipt, and a row the import skipped left out.
- `TestTUILaunchCarriesTheProfilesCredentialsIntoTheInstance`: the same
  through the TUI's `tuiLaunch`, checking that nothing runs before the
  terminal is handed over and that the session starts after the import.
  `TestTUILoginPreparesNoInstance` covers the login.
- `TestMergeSettlesCopiedCredentialsAgainstTheReceipt`: untouched, refreshed,
  logged out, logged out but re-added elsewhere, and new, on a single-key
  and a composite-key table.
- `TestRealOpenCodeCarriesCredentialsAndLogouts`: the real 2.x binary on
  stores it creates with made-up keys (no operator data). A stored key
  reaches the instance, an untouched copy does not restamp the archive, and
  a logout in the instance reaches the archive. The profile also holds an
  `auth.json` written after its store, as every pre-2.x profile does, and
  the instance must still hold exactly the profile's two rows (it held
  three before the fix).
- `TestLaunchCarriesTheProfilesCredentialsIntoTheInstance` also checks that
  the stand-in import found no `auth.json` and that the instance has it back
  unchanged afterwards; `TestSetAsideLegacyAuthWithoutAnAuthFileHasNothingToRestore`
  covers an instance without one.
