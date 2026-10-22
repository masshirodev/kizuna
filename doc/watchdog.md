# The idle watchdog

`kizuna watchdog` is a dead-man's switch for an agent that ends its turn
waiting on something that never finishes. An agent arms it on itself ("nudge
me if I sit idle for 30 minutes"), a systemd user timer checks every two
minutes, and an agent idle past its deadline gets one line typed at its
prompt, which wakes it. Card `c180` ("A watchdog that nudges an agent idle past
its deadline") on the AI Session board. Back to
[agent-messaging.md](agent-messaging.md) and the [README](../README.md).

## Why

A Claude Code coordinator running a long loop ended its turn waiting on a
background job, and the job hung. Nothing wakes an idle session except a
finished background task or typed input, so it sat idle for six hours
overnight. "Every background step must exit" is a rule that has to be
remembered on every turn and fails silently when it is not. The watchdog does
not depend on remembering anything after it is armed.

It is generic: idle too long, nudge. It knows nothing about waves, tests or
jobs. What the agent was waiting on goes in the nudge text, which the agent
writes itself when it arms.

## Commands

```sh
kizuna watchdog self --idle 30m "Waiting on the tier run in pane 4; if it hung, kill it by pid and carry on."
kizuna watchdog max/run-3389106237 --idle 1h --max 5    # another agent, default text
kizuna watchdog --off              # disarm this agent's
kizuna watchdog --off max/run-3389106237
kizuna watchdog --list             # what is armed (--json for an agent to read)
kizuna watchdog --check            # one round; the timer runs this
kizuna watchdog --install          # write and enable the systemd user timer
kizuna watchdog --uninstall        # disable and remove it
```

| Argument | Meaning |
| -------- | ------- |
| `<id\|self>` | Who to watch: an id from `kizuna peers`, a profile with one agent running, a Claude session name, or `self` |
| `--idle` | How long idle before a nudge, as a Go duration (`30m`, `1h30m`). At least `1m` |
| `--max` | Nudges in a row with no busy period between them before giving up (default 3) |
| text | The nudge. One line: newlines and control characters are folded to spaces. Left out, a generic "check whether what you are waiting on is still running" line |

Arming an agent that already has a watchdog replaces it, counters and all.

The same operations are MCP tools on `kizuna mcp serve`, so an agent can arm
itself without a shell: `watchdog_arm` (`idle`, `text`, `max`, `target`
defaulting to `self`), `watchdog_disarm` (`target`, default `self`) and
`watchdog_list`.

`self` is the instance the command runs inside, found the way `kizuna send`
names its sender ([agent-messaging.md](agent-messaging.md#kizuna-peers)):
`KIZUNA_INSTANCE_DIR` with `KIZUNA_PROFILE`, or, where a CLI scrubbed the
environment, the instance that recorded one of this process's ancestors. From
a plain shell `self` is refused.

### What can be armed

Only an agent the check can actually nudge, because an armed watchdog that can
never fire is the silent failure this exists to prevent:

- **Claude Code only.** It is the one provider that reports busy or idle
  (`claude agents --json`). Codex, OpenCode and Antigravity are refused.
- **Interactive only.** A headless run (`claude -p`) has no prompt.
- **In a known pane.** It must have been launched in ranma, or under the tmux
  status bar, so there is somewhere to type. `kizuna peers` shows the pane.

Arming also warns when the timer is not installed.

## What a check does

For each armed agent, using the same listing as `kizuna peers` (asked only of
the profiles something is armed in):

| Seen | Then |
| ---- | ---- |
| `busy` | Never typed into. The idle clock stops and the run of nudges resets to 0, which also clears `exhausted` |
| `idle`, first time in a row | The idle clock starts now |
| `idle` for at least `--idle` | One line is typed at its prompt, the nudge is counted, and the idle clock restarts from the nudge |
| `idle`, `--max` nudges already in a row | Nothing: it is `exhausted` until it is seen busy again |
| anything else, or not described | Never typed into. The idle stretch is broken, so the clock only runs while every check has seen it idle |
| gone (its instance lock is no longer held, or the id now belongs to another process) | Disarmed |

The line typed is

```
[kizuna watchdog] Idle for 31m (deadline 30m, nudge 1 of 3). <the armed text>
```

It goes through `typeIntoPane`, the path `kizuna send` types its own nudge
with (`ranma send -p <pane> -e`, or `tmux send-keys` for the tmux status bar),
and behind the same gate: only an agent the listing reports `idle`. A failed
type (the pane closed) is not counted, its error shows in `--list --json`, and
the next check tries again.

The idle clock starts at the first check that sees the agent idle, so a
deadline is met up to one timer interval (2 minutes) late. Claude Code does not
say when a session went idle, only that it is.

"No busy period between them" means none the checker saw. An agent that wakes
on a nudge, decides to keep waiting and ends its turn within two minutes is not
seen busy, so that run of nudges keeps counting towards `--max`. That is on
purpose: three nudges that each end in "still waiting" are the cap doing its
job, and the watchdog stops instead of typing all night.

Like any typed nudge, a half-written draft at the prompt gets the line
appended and submitted with it ([agent-messaging.md](agent-messaging.md#kizuna-send)).

## `--list`

```
ID                  IDLE  STATUS     SEEN  IDLE SINCE       NUDGES         LAST NUDGE       TEXT
max/run-3389106237  30m   armed      idle  09:32 (12m ago)  0/3 (0 total)  —                Waiting on the tier run in pane 4; if it hung…
pro2/run-1381902169 1h    exhausted  idle  03:10 (1h ago)   3/3 (5 total)  03:10 (1h ago)   —
```

`NUDGES` is the run since it was last seen busy, against `--max`, and the total
since it was armed. `--json` prints every field of the state file.

## State

One file, `~/.config/kizuna/watchdog.json` (beside `profiles.json`, under the
launcher's config base like everything else kizuna keeps), keyed by agent id.
It is only ever changed under an `flock` on `.watchdog.lock` and written
atomically, so arming, disarming and a check can land at the same moment
without one losing the other. A check reads the file, asks the listing and
types (which takes seconds) without the lock, then merges its results back
under it, and skips any entry that was re-armed or disarmed meanwhile. A second
check started while one runs (the timer and a manual `--check`) leaves at once,
through `.watchdog-check.lock`, so an agent is never nudged twice for one
deadline.

## The timer

`kizuna watchdog --install` writes two units to `~/.config/systemd/user/` and
runs `systemctl --user daemon-reload` and `enable --now kizuna-watchdog.timer`:

- `kizuna-watchdog.service`, a oneshot running `<kizuna> watchdog --check`
  (the absolute path, found the way `kizuna integrate messaging` finds it),
  bounded by `TimeoutStartSec=120`. It carries the `PATH` and config base it
  was installed from, because a user service starts with systemd's short
  default `PATH`, and the check needs `claude` to ask who is idle and `ranma` or
  `tmux` to type. Re-run `--install` after moving any of them.
- `kizuna-watchdog.timer`, every 2 minutes from boot.

With nothing armed a check reads one file and exits. Logs:
`journalctl --user -u kizuna-watchdog.service`.

Why a timer and not something else: kizuna has no daemon (the TUI runs only
while someone has it open, and `kizuna mcp serve` lives and dies with one
agent), and a dead-man's switch must not depend on the agent it watches. The
`integrate` commands configure one profile's provider; this is machine-wide,
so it has its own `--install`.

## Not done

- Only Claude Code agents can be watched, for the same reason only they get the
  automatic nudge from `kizuna send`: the others do not report idle.
- The idle clock is sampled, not exact (above).
