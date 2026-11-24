# Quota for scripts: `kizuna usage`

`kizuna usage` prints how much of each quota window a profile has left, from
the same caches the TUI's gauges read ([tui.md](tui.md)). It exists so other
tools can choose an account without scraping the TUI: Kantoku runs
`kizuna usage <profile> --json` before every launch to keep a reserve on each
profile (its card "Per-profile quota reserve").

```sh
kizuna usage                 # a table of every profile
kizuna usage max             # one profile, as a table
kizuna usage max --json      # one profile, as the JSON below
kizuna usage --json          # every profile, as a JSON array of the same objects
```

An app name works wherever a profile name does and answers for the app's
active member; `profile` in the output is that member's name.

## Where the figures come from

Each provider writes its own quota cache inside the profile, and kizuna only
reads it:

| Provider    | Source | Reading time |
| ----------- | ------ | ------------ |
| Claude Code | `claude/.claude.json`, `cachedUsageUtilization` (its `limits`, or `five_hour` and `seven_day` in older caches) | `fetchedAtMs` |
| Codex       | the newest `rate_limits` event in `codex/sessions/**/*.jsonl` | the event's `timestamp`, else the file's modification time |
| OpenCode, DeepSeek, Antigravity | none: they write no quota cache inside the profile | — |

OpenCode was checked on 2026-11-23 (opencode 2.x): its profile holds
`state/opencode/kv.json`, `model.json` and `opencode.db`, and none of them
records a rate limit or quota. Its profiles always read as unknown. If a
future OpenCode writes one, it is a new source to add here, not a guess.

kizuna never opens a credential file and never calls a provider's usage
endpoint with the profile's token. The price of that is the honest one: **a
cache is only refreshed while a session runs on that profile**, so an idle
profile's reading can be days or weeks old, and an account never used reads
as unknown.

## Stale readings

A window has rolled over since the reading when:

- its reset time has passed, or
- it has no reset time and the reading is older than the window itself (five
  hours, or seven days): it has rolled over at least once since, whatever its
  reset was.

Such a window is **stale**. It has probably refilled, but nobody knows by how
much, so its `remaining` is `null`, never the old figure: on 2026-11-23 `max`
still cached 70% used against a reset of 10 November, and reporting 30% left
would have kept a likely full account idle. A current figure for a window
always wins over a stale one when the provider reports several limits for it
(Claude's per-model weekly limits, for instance).

## The JSON

This shape is a contract: tools parse it, so a change to it is a breaking
change and goes through `doc/DESIGN.md`.

```json
{
  "profile": "max",
  "provider": "claude",
  "fetched_at": "2026-11-06T10:04:41Z",
  "five_hour": null,
  "weekly": { "remaining": null, "resets_at": "2026-11-10T12:00:00Z" },
  "stale": true
}
```

| Field | Meaning |
| ----- | ------- |
| `profile`, `provider` | The profile read (an app's active member, when an app was named). |
| `fetched_at` | When the provider took the reading, RFC 3339 in UTC. `null` when there is no reading, or the cache did not date it. |
| `five_hour`, `weekly` | The window, or **`null` when the cache does not report it at all**. Never `0` for unknown. |
| `….remaining` | Percent left, an integer 0 to 100 (100 minus the provider's utilization, rounded). `null` when the window is stale. |
| `….resets_at` | When the window rolls over, RFC 3339 in UTC. For a stale window, the rollover that already passed. `null` when the provider did not say. |
| `stale` | True when at least one window is stale. |

So a window reads one of three ways: an object with a number (current), an
object with `remaining: null` (stale: it rolled over since the reading), or
`null` (unknown: never reported). In the example, `max` caches no five-hour
window at all, and its weekly window reset after the reading.

Without a profile, `--json` prints an array of these objects, one per
profile, sorted by name.

Whenever `--json` is given, stdout carries the JSON and nothing else: the
notes kizuna prints when it reclaims a stray OpenCode instance go to stderr.

## Exit codes

For `kizuna usage <profile>`, with or without `--json`:

| Code | Meaning |
| ---- | ------- |
| 0 | Known: at least one window has a current figure. |
| 1 | Error: a malformed command line, or kizuna's own config could not be read. |
| 2 | No profile or app by that name. Nothing is printed on stdout. |
| 3 | Stale: there is a reading, but no window in it is current. |
| 4 | Unknown: no reading at all. The provider keeps no quota cache (OpenCode), or never wrote one. |

The output is printed for 3 and 4 as well, so a script can branch on the code
and still read the details. `kizuna usage` without a profile exits 0 whenever
it could print the list; each profile's state is in its row.

## The table

```
PROFILE    PROVIDER     5-HOUR                  WEEKLY                   READ
frialto    claude       99% left, resets 23:59  97% left, resets 21:59   4m ago
max        claude       unknown                 stale (reset 10 Nov)     17d ago
opencode   opencode     unknown                 unknown                  never
pro2       claude       stale (rolled over)     stale (reset 18 Oct)     37d ago
```

Times are local. The table is for people and may change; scripts read the
JSON.
