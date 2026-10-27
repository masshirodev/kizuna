# The kizuna-inbox mod

How a message reaches an **idle** Claude Code agent without a key being typed
into its terminal. Card `c186-deliver-messages-to-an` ("Deliver messages to an
idle Claude session through a mod, never by typing") on the Kizuna board. Back
to [agent-messaging.md](agent-messaging.md).

## Why typing was not enough

Before this, `kizuna send` woke an idle agent by typing a one-line nudge at its
prompt (`typeIntoPane`, through `ranma send -e` or `tmux send-keys`), and the
`UserPromptSubmit` hook handed over the message on that prompt. On 2026-10-27
it failed in two ways:

- **It typed over the user.** A half-written draft got the nudge appended and
  was submitted with it, jumbled.
- **The Enter was lost.** ranma sends Enter as a key of its own 50 ms after the
  text (`ENTER_GAP`, ranma `98a4dce`), because Claude Code takes a fast burst
  for a paste. Claude Code 2.1.311 still counted the Enter as part of that
  paste, so the nudge sat in the prompt unsent. A longer gap is a guess at
  Claude Code's paste window, which the next release can move again.

Both come from typing into a terminal someone else may be using, so the fix
for Claude Code is to not type.

## What the mod does

Claude Code (2.1.311 and later) loads **mods**: plugins with a hooks module that
can run on a timer, start processes and submit a prompt. `kizuna-inbox` is one,
embedded in the kizuna binary (`cmd/kizuna/inboxmod/`).

- Every **2 seconds** it runs `kizuna inbox --mod`, and adds `--drain` when no
  turn is running (it tracks the main loop's `turn.start` and
  `turn.complete`; a subagent's turns do not count).
- `kizuna inbox --mod` touches `mod-alive` in the instance directory, the
  **heartbeat**, and prints `{"unread": N}`. With `--drain` it moves the unread
  messages to `inbox/read/` and prints them as `{"text": "…"}`, rendered exactly
  as the hooks render them: framed as another agent's message, with the
  `kizuna send <sender>` line to reply with.
- The mod submits that text with `$.prompt.submit`. It starts a turn of its own
  and never touches the prompt box, so a draft stays as the user left it. With a
  draft present, a toast says a message arrived and the draft is untouched.
- While a turn runs, the mod only keeps the heartbeat fresh. The
  `PostToolUse` and `Stop` hooks deliver at the next tool call, as before.
- If `kizuna inbox --mod` fails (a session kizuna did not launch, so there is no
  instance, or a kizuna too old to know `--mod`), the mod stops polling for the
  rest of the session and says why in the debug log.

Draining is the claim: kizuna renames each message into `read/`, so the mod and
a hook draining at the same moment cannot both deliver it.

## How `kizuna send` knows

The profile's settings say only that a profile *has* the mod. Whether a given
session *runs* it is the heartbeat's job: a session started before the mod was
installed has the hooks but not the mod, and still needs the typed nudge.

`kizuna peers` reads a heartbeat younger than **15 seconds** as a live mod and
shows the peer's delivery as `mod` (MAIL column, `delivery` in `--json`). For
such a peer, `kizuna send` never types: the message waits in the inbox, and the
mod picks it up within a poll. `--type` still types if asked to.

## Where it is installed

`kizuna integrate messaging <claude profile>` writes the mod into
`<profile>/claude/skills/kizuna-inbox/`. Claude Code auto-loads a plugin folder
under the config dir's `skills/` as `kizuna-inbox@skills-dir`, so nothing is
registered with `claude plugin`. `--off` removes the folder with the hooks.

Every Claude launch of a profile with messaging on rewrites the folder from the
binary first (`refreshInboxMod`), so a profile integrated by an older kizuna
gets the mod, and an upgraded kizuna's mod reaches the next session. Files that
already match are not touched, because Claude Code reloads a skills-dir plugin
when its files change. Do not edit the installed copy: the next launch puts it
back.

Skill copying ([mcp-and-skills.md](mcp-and-skills.md)) passes the folder over,
because it has no `SKILL.md`.

## Checking it by hand

1. `CLAUDE_CONFIG_DIR=<profile>/claude claude plugin list` shows
   `kizuna-inbox@skills-dir` as loaded.
2. `ls -l --time-style=+%T $KIZUNA_INSTANCE_DIR/mod-alive` shows a time from the
   last couple of seconds, and `kizuna peers` shows that agent's MAIL as `mod`.
3. From another agent, `kizuna send <id> "hi"`: the summary says the mod hands it
   over, and the idle session starts a turn on its own a few seconds later.

The 2026-10-27 prototype was proven the same way, with a message file dropped
straight into the inbox by a systemd timer, so neither typing nor the sending
agent could have woken the session.

## Not done yet

- **Codex, OpenCode and Antigravity still need the typed nudge**, as does a
  Claude session started before the mod was installed. Making that path leave a
  draft alone and confirm its Enter is card `c187-typed-nudges-leave-a`
  ("Typed nudges: leave a half-typed draft alone, and resend Enter when the
  nudge stays unsent").
- **The watchdog still types** its nudge ([watchdog.md](watchdog.md)). Routing
  it through the mod is card `c188-watchdog-nudges-through` ("Watchdog nudges
  through the kizuna-inbox mod instead of typed keys").
- **A message the mod drained is lost if the submit then fails.** It is already
  in `read/`, the same as with a hook that drains and then cannot answer.
