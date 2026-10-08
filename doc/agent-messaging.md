# Agents finding and messaging each other: `ai peers` and `ai send` (proposal)

Status: proposal, nothing built yet. Card `c144-agents-finding-and` ("Agents finding and messaging each other across profiles") on the AI Session board.

## Goal

Let an agent running under one profile find the agents running under every
profile, and leave one of them a message it will read without a human
switching panes.

The case that prompted it: an agent fixing a bug in `~/projects/ranma`, and a
second conversation that finds another ranma bug while the first is still
working. Today the second agent makes a worktree and fixes it there. With this
it could hand the bug to the agent already in the main checkout ("when you are
done, also fix this, here is the repro") and move on.

Non-goals:

- Interrupting an agent that is working. A message is a queued request, never
  a keystroke that lands in the middle of a turn.
- Replacing Claude Code's own peer messaging where it already reaches (see
  below). This covers what it cannot.
- A conversation protocol: threads, acknowledgements, replies. A reply is just
  another `ai send` in the other direction.

## What exists already, and where it stops

Claude Code has native peer messaging: `ListAgents` and `SendMessage` list the
other Claude sessions on the machine and deliver to them. **They only see
sessions under the same `CLAUDE_CONFIG_DIR`**, which ai-session gives each
profile on purpose. Checked on 2026-10-08: a `max` session listed the four other
`max` sessions, and not a `pro2` session that had been running in
`~/projects/ranma` for 18 hours, in ranma pane 1. Across profiles, and for
Codex, OpenCode and Antigravity at all, an agent has no way to know another
agent exists.

ai-session is the one process that sees every profile. What it already has:

| Fact | Source today |
| ---- | ------------ |
| Every live instance, every provider | `allProfileInstances` (`cmd/ai/instances.go`), from `instances/run-*/.active.lock` |
| Launcher and CLI PIDs | `.active.lock`, lines 1 and 2 (`setProfileChildPID`) |
| Launch folder, start time | `instance.json` (`setProfileInstanceMeta`) |
| Claude session id, slug, `cwd` | `claude agents --json` (`claudeLiveSessions`, `cmd/ai/sessions.go`) |
| Claude busy/idle | the same output's `status` field, **not yet read**: `claudeAgent` drops it |
| Conversation title | `titleClaudeSession` (transcript `ai-title`); Codex from its session log |
| Ranma pane | `RANMA_PANE` / `RANMA_SOCKET` in the launcher's environment, **not yet recorded** |

## Step 1: `ai peers`

A listing of every live instance across every profile, one row each:

```
PROFILE  PROVIDER  REPO    CHECKOUT       BRANCH  STATE  PANE  TITLE
pro2     claude    ranma   main checkout  main    busy   1     Tmux appearance and layout import
max      claude    ranma   main checkout  main    idle   13    Ranma SSH inside scratchpad
max      claude    kumiko  main checkout  main    busy   2     ...
```

`ai peers --json` prints the same thing for an agent, with the instance id
(`run-XXX`) to address it by. Changes:

- **Repo and checkout, not only folder.** Run `git rev-parse --show-toplevel
  --git-common-dir --abbrev-ref HEAD` in the launch folder. The common dir names
  the repository, so a worktree and its main checkout match as the same repo,
  and "main checkout" or "worktree" says which one this is. Matching on the
  folder alone fails exactly in the case above, where one of the two agents is
  in a worktree. A folder that is not a repository shows the folder.
- **Record the pane at launch.** `setProfileInstanceMeta` adds `ranma_pane` and
  `ranma_socket` to `instance.json` when the launcher runs inside ranma, and
  `tmux_socket` when the tmux indicator wrapped it. Older meta files without
  them show no pane.
- **Read `status`** from `claude agents --json`. Other providers show `?`. Codex
  might be read from its session log's last event, but that is a later step.
- Costs one `claude agents` call per Claude profile that has a live instance,
  under the existing `sessionLookupTimeout`. It is a command, not a TUI tick, so
  that is fine. The TUI's live panel can show repo and branch from the same
  helper without calling `claude agents` on every refresh.

## Step 2: `ai send` and a mailbox

```
ai send run-XXX "Second ranma bug: … Repro: … Fix it in the main checkout after the current one."
```

writes one file to `instances/run-XXX/inbox/<timestamp>-<sender>.md`, carrying
the sender's profile, instance, folder and the text. It never touches the
target's terminal. The launcher exports `AI_INSTANCE_DIR` to the child beside
`AI_PROFILE` and `AI_PROVIDER`, so code running inside the target finds its own
inbox without guessing.

Delivery is per provider, because it has to happen inside the target CLI:

- **Claude Code: hooks.** `ai integrate messaging <profile>` adds a small
  `ai inbox --drain` hook to the profile's `settings.json`. As a `PostToolUse`
  hook it returns the messages as `additionalContext`, so a busy agent reads
  them at its next tool call. As a `Stop` hook it blocks the stop with the
  messages as the reason, so an agent that is about to go idle picks them up
  first. A drained file moves to `inbox/read/`, so nothing is delivered twice
  and the sender can see it was picked up.
- **OpenCode: a plugin** on the session events, the same idea. Still to work out
  how a plugin adds context to a running session.
- **Codex, Antigravity:** unknown. Check what each offers before promising
  anything. Until then, step 3.

## Step 3: typing into the pane, as a fallback

`ranma send -p <pane> -e '<text>'` (and `tmux send-keys` for tmux-wrapped
launches) works with every provider, because it types at the CLI's prompt. It
is only safe when the target is **idle at its prompt**. If the CLI is showing a
permission dialog, the text, or the `Enter` after it, answers the dialog. So
`ai send` uses it only when the mailbox has no delivery for that provider *and*
the target reads as idle, and says which path it took. `--type` forces it, and
`--no-type` refuses it.

## Step 4: an MCP tool

An `ai` MCP server with `list_peers` (step 1's JSON) and `send` (step 2), so an
agent can do this itself rather than a human telling it to run `ai peers`.
Copied into a profile with the existing `ai mcp` plumbing.

## Open questions

- **Who may write to whose inbox.** Everything is the same Unix user, so this is
  about guarding against mistakes, not attacks. Should a message from a
  headless wave worker be marked so the receiver treats it more carefully? The
  inbox text reaches the target as context from another agent, not from the
  user, and the hook should frame it that way.
- **Messages to an instance that exits.** The inbox goes away with the instance
  directory. Either `ai send` refuses a dead target, or unread mail moves to the
  profile so the next launch in that folder gets it. Refusing is simpler.
- **OpenCode instance directories are merged and removed on exit**
  (`doc/opencode-concurrent-instances.md`). The inbox has to be left out of the
  merge.
