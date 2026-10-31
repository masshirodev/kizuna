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
called first in `prepareOpenCodeInstance`):

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
- `TestMergeSettlesCopiedCredentialsAgainstTheReceipt`: untouched, refreshed,
  logged out, logged out but re-added elsewhere, and new, on a single-key
  and a composite-key table.
- `TestRealOpenCodeCarriesCredentialsAndLogouts`: the real 2.x binary on
  stores it creates with made-up keys (no operator data). A stored key
  reaches the instance, an untouched copy does not restamp the archive, and
  a logout in the instance reaches the archive.
