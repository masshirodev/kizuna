# Agents finding and messaging each other

`ai peers` lists every agent running under any profile, with the repository and
checkout it works in. `ai send` leaves one of them a message, and the receiver
reads it at its next tool call or before it stops. Card
`c144-agents-finding-and` ("Agents finding and messaging each other across
profiles") on the AI Session board. Back to the [README](../README.md).

The case it was built for: an agent fixing a bug in `~/projects/ranma`, and a
second conversation that finds another ranma bug while the first is still
working. Instead of making a worktree, the second agent hands the bug to the
agent already in the main checkout ("when you are done, also fix this, here is
the repro") and moves on.

## Why ai-session, and not Claude Code's own messaging

Claude Code has native peer messaging (`ListAgents`, `SendMessage`), but it
**only sees sessions under the same `CLAUDE_CONFIG_DIR`**, which ai-session
gives each profile on purpose. On 2026-10-08 a `max` session listed the other
`max` sessions and not a `pro2` session that had been in `~/projects/ranma`
for 18 hours. Codex, OpenCode and Antigravity have no peer messaging at all.
The launcher is the one process that sees every profile, so it keeps the
directory and the inboxes.

## `ai peers`

```
$ ai peers
ID                        PROVIDER  WHERE                       BRANCH                STATE  PANE      MAIL   TITLE
max/run-3656556723 (you)  claude    ai-session (main checkout)  feat/agent-messaging  busy   ranma 14  hooks  Ai-session inter-agent messaging
max/run-2705011154        claude    ranma (main checkout)       main                  idle   ranma 13  hooks  Ranma SSH inside scratchpad
pro2/run-1381902169       claude    ranma (main checkout)       main                  busy   ranma 1   —      Tmux appearance and layout import
```

`ai peers ranma` keeps the agents in one repository, named by its name or any
path inside it. `ai peers --json` prints the same thing for an agent to read.

| Column | Where it comes from |
| ------ | ------------------- |
| ID | `<profile>/<run-dir>` for a concurrent instance, the profile name for one holding its profile's exclusive lock (Antigravity) |
| WHERE | `git rev-parse` in the launch folder. The git common dir names the repository, so a worktree and its main checkout match as one repo, and the column says which one this is. A folder outside any repository shows the folder |
| STATE | `busy` or `idle` from `claude agents --json`. Other providers do not say, and show `—` |
| PANE | The ranma pane the launch ran in, recorded in `instance.json` (`ranma_pane`, `ranma_socket`). Instances started before this was recorded are read from the launcher's `/proc/<pid>/environ`. `tmux` for a launch inside the tmux status bar |
| MAIL | How a message reaches it (below), and how many are unread |
| TITLE | The conversation's title, as in the `h` picker ([sessions.md](sessions.md#hijacking-a-running-instance)) |

`(you)` marks the instance asking. That comes from `AI_INSTANCE_DIR`, or, where
a CLI scrubbed the environment, from finding the instance whose recorded CLI
PID is one of this process's ancestors.

## `ai send`

```
ai send pro2/run-1381902169 "Second ranma bug: … Repro: … Fix it in the main checkout after the current one."
echo "…" | ai send ranma-18      # text from stdin
```

A target is an ID, a profile with exactly one instance running, or Claude
Code's session name (`ranma-18`). A name that could mean more than one agent is
refused with the IDs it could mean, because a message delivered to the wrong
agent is acted on by the wrong agent. Sending to yourself is refused too.

The message becomes one JSON file in `instances/run-XXX/inbox/`, written
atomically, carrying the sender's ID, folder, time and text. It **never** goes
to the target's terminal while it works. What happens next depends on the
target:

| Target | Delivery |
| ------ | -------- |
| Claude Code, messaging integrated, busy | Read at its next tool call (`PostToolUse` hook) |
| Claude Code, messaging integrated, idle | A one-line nudge is typed at its prompt, which wakes it, and the `UserPromptSubmit` hook hands it the message on that same prompt |
| Anything with the MCP server only | Waits until the agent calls `read_inbox` |
| Nothing integrated | Waits in the inbox; `ai send` says so and how to change it |

`--type` types the nudge whatever the state, and `--no-type` never types it.
Typing is only automatic for an agent that reports `idle`, because text typed
into a busy one can land in a permission dialog, and the `Enter` after it can
answer the dialog. Even at an idle prompt, a half-written draft of the user's
gets the nudge appended and submitted with it. That is the price of waking an
idle agent, and why `--no-type` exists.

The receiver reads the message framed as coming from another agent, not from
the user. It is told to treat it as a colleague's request, fit it around its
current work, and ask the user before anything destructive. It also gets the
`ai send <sender>` line to reply with.

## `ai inbox`

`ai inbox` prints this instance's unread messages and moves them to
`inbox/read/`. `--peek` leaves them unread, and `ai inbox <id>` reads someone
else's. `ai inbox --hook` is what the Claude Code hooks run. It reads the hook
event from stdin and answers `PostToolUse` and `UserPromptSubmit` with
`additionalContext`, and `Stop` with `{"decision":"block","reason":…}`, which
is how a hook gives Claude Code something to do before it stops. The Stop loop
ends by itself, because the inbox is empty the next time round. An event it
does not know puts the messages back. In hook mode every failure is silent and
exits 0, because a hook must never break the agent it runs in.

## `ai integrate messaging <profile>`

```
ai integrate messaging max          # on
ai integrate messaging max --off    # off again
```

- **Every provider with an MCP config** (Claude Code, Codex, OpenCode,
  Antigravity) gets the `ai` MCP server, `ai mcp serve`. It is written the same
  way `ai mcp copy` writes servers, so the rest of the file stays as it was.
- **Claude Code** also gets three hooks in the profile's `settings.json`:
  `PostToolUse` (matcher `*`), `Stop` and `UserPromptSubmit`, each running
  `<ai> inbox --hook` with a 10-second timeout. The install is idempotent. An
  entry is recognised as ai's by `inbox --hook` in its command, and `--off`
  removes exactly those entries, plus any event left with no hooks.
- Sessions already running pick the hooks up when they restart.

The command written is `ai`'s absolute path from `PATH` (falling back to the
running binary), so a reinstall to the same place keeps working.

## `ai mcp serve`

A stdio MCP server, one JSON-RPC object per line, with three tools:

| Tool | Does |
| ---- | ---- |
| `list_peers` | `ai peers --json`, optionally filtered by `repo` |
| `send_message` | `ai send`; `type_into_pane` is `auto` (default), `always` or `never` |
| `read_inbox` | `ai inbox` for the instance the server runs inside |

Its `instructions` tell the agent what it is for: find the agent already in a
checkout before opening a parallel worktree there.

## Environment a launch now carries

| Variable | Value | Why |
| -------- | ----- | --- |
| `AI_INSTANCE_DIR` | the instance's lock directory | where its inbox is |
| `AI_CONFIG_HOME` | the launcher's config base (normally `~/.config`) | an OpenCode launch repoints `XDG_CONFIG_HOME` at the profile, so an `ai` started inside it would otherwise find no `profiles.json` |

`AI_CONFIG_HOME` is only believed while `XDG_CONFIG_HOME` really points inside
that base's `profiles/`. A shell or a test that set its own `XDG_CONFIG_HOME`
keeps it, so a leftover variable cannot point anything at the real profiles.

## Not done yet

- **OpenCode, Codex and Antigravity read their inbox only when asked** (the MCP
  tool, `ai inbox`, or a typed nudge). No hook makes them read it on their own:
  an OpenCode plugin on session events could, and what Codex and Antigravity
  offer has not been looked at.
- **No busy/idle for those three**, so the automatic nudge never fires for
  them. `--type` does it by hand.
- **The TUI does not show messaging yet.** The integrations box was laid out
  for three integrations, and a fourth is a layout change for its own card.
- **Mail to an instance that exits** goes with its instance directory. `ai
  send` only reaches live instances, so nothing is sent into the void, but
  unread mail at exit is lost. The exception is an exclusive-lock profile,
  whose inbox sits in the profile directory and survives to the next launch.
