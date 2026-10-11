# Coming back from a power cut: `kizuna resume-all`

When the machine goes down with agents running, `kizuna resume-all` reopens every
conversation it killed, each in a ranma pane in the workspace it used to be in.
Card `c161-reopen-the-sessions-a` ("Reopen the sessions a power cut killed: kizuna
resume-all") on the AI Session board. Back to the [README](../README.md).

It was built on 2026-10-10, after three blackouts in twenty minutes, each
followed by finding and reopening every session by hand.

```
$ kizuna resume-all -n
PROFILE  STATE  WHERE   FOLDER                          TITLE                   COMMAND
pro2     busy   main:1  /home/masshiro/projects/ranma   Fix the resize flicker  cd /home/masshiro/projects/ranma && kizuna pro2 --resume 3f2c…; exec "${SHELL:-sh}"
max      idle   main:3  /home/masshiro/projects/kumiko  Hours rules             cd /home/masshiro/projects/kumiko && kizuna max --resume 9a01…; exec "${SHELL:-sh}"

$ kizuna resume-all
reopened pro2 Fix the resize flicker in /home/masshiro/projects/ranma (main:1)
reopened max Hours rules in /home/masshiro/projects/kumiko (main:3)
2 of 2 session(s) reopened
```

| Flag | What it does |
| ---- | ------------ |
| (none) | Reopen every session the cut killed. |
| `--busy` | Only the ones whose agent was mid-turn at the last refresh. The rest stay recorded for a later run. |
| `-n`, `--dry-run` | List them and the command each would run; touch nothing. |
| `--forget` | Drop them without reopening anything. |

Nothing is typed into the reopened sessions: the conversation comes back, and
what to say to it is yours.

## How it knows

Every interactive launch (`kizuna <profile>`, `kizuna <profile> resume …`, a launch
from the TUI) writes a **restore record** to `~/.config/kizuna/restore/` as it
starts, and rewrites it every 20 seconds while it runs
(`restoreRefreshInterval` in `cmd/kizuna/restore.go`). A record holds:

- the profile, provider and folder;
- the conversation's id: Claude Code's comes from the file it keeps per process
  under the profile's `claude/sessions/`, Codex's from its newest rollout for
  the folder. OpenCode, Antigravity and DeepSeek report none, so they reopen
  with `--continue`, the folder's latest conversation;
- `busy` or `idle`, where the provider says (Claude Code only);
- the ranma session, workspace number and workspace name the pane sits in,
  asked of `ranma panes --json` on every refresh, since panes move;
- the boot id and the launcher's pid, which say whether it is still alive.

The records live outside the instance directories on purpose: those are
reclaimed by the first `kizuna` that finds their lock dead, which after a reboot is
whichever runs first.

Headless runs (`claude -p`, `codex exec`, `opencode run`, wave workers) and CLI
management commands (`claude mcp list`, `opencode models`) keep no record: they
end by themselves and have nothing to come back to.

## When a record is kept

| How the launch ended | Record |
| -------------------- | ------ |
| The CLI exited (`/exit`, Ctrl-D, a crash) | dropped |
| SIGHUP: a ranma pane or terminal closed by hand | dropped |
| SIGHUP while `systemctl is-system-running` says `stopping` | kept |
| SIGTERM: a shutdown or reboot | kept |
| Power cut, SIGKILL, kernel panic: no signal at all | kept |

A record is **dead**, and a candidate, when it comes from another boot
(`/proc/sys/kernel/random/boot_id` differs, so a reused pid cannot fool it) or
its launcher's pid is gone. Of the dead ones, `resume-all` skips any whose
conversation is open again in a live launch (you already reopened it by hand),
keeps only the newest record of a conversation recorded twice, and drops
records of profiles since removed. Those skipped records are deleted; so is
each one it reopens, since the new launch writes its own.

## Where they open

Run it from a ranma pane. Each session opens with `ranma open --background`:
in its old session and workspace (`--session main --workspace 3`, with the
workspace's name if it had one) when it was launched inside ranma, in an empty
workspace otherwise. Focus stays where it is. The pane runs
`kizuna <profile> <reopen arguments>; exec $SHELL`, so a shell is left behind when
the agent exits, as in a pane opened by hand. A session whose folder is gone is
skipped and reported.

Outside ranma nothing is opened: it prints the table with a `cd … && …` line
per session to run in terminals of your own.

The layout inside a workspace is not restored: two sessions that shared
workspace 3 come back side by side in workspace 3, split however ranma splits
it, not at their old sizes.

## Limits

- A conversation switched inside the CLI (`/resume`, `/clear`) less than 20
  seconds before the cut reopens as the one before.
- Only sessions started by a `kizuna` that has this feature keep records. Install
  it (`go install ./...`, or `kizuna self-update`) and relaunch before the next
  blackout, or the sessions running now are not covered.
- OpenCode's private instance stores are merged back by
  `reclaimStrayIsolatedInstances` at the start of every `kizuna` command, including
  this one, so `--continue` finds the conversation in the profile's store.
