# Feature brief: hand off an OpenCode session

> **Implemented 2026-09-27**, tier 2 (the full reader) rather than the pointer
> fallback. What shipped, and where it departs, is in
> [doc/sessions.md](../../sessions.md#reading-opencode). This file is kept as the
> record of what was asked.

Written 2026-09-27, from: *"let me handoff an opencode session, even if the
handoff that I have to copy and send is 'read sessions `[id]` from
`~/.config/ai/profiles/<profile>`', if we can do more than that, great, if not,
good too."*

This is not a design handoff — the handoff wizard already exists and its shape
does not change. It is a **capability brief**: `H` moves work out of a Claude or
Codex account today and cannot move it out of an OpenCode one. The two tiers
below are the fallback the request allows and the fuller version worth reaching
for. It is written down before it is built so the shape is agreed first.

## What happens today, measured

- `H` on an OpenCode row reaches `buildBrief` → `readSessionMessages` →
  `transcriptPath`, which returns `provider "opencode" records no transcript to
  read back`. The status line shows that; no brief is written and no file lands
  under `~/.config/ai/handoffs/`.
- The same error fills the picker's preview pane, because the pane is filled
  from the same reader. `doc/sessions.md` is explicit about it: *"OpenCode and
  Antigravity say so instead of sitting on `reading…`"*, and *"`H` can hand off
  from Claude Code and Codex."*
- Claude and Codex each keep a transcript file per conversation; the recent list
  parses it for a title and the brief is built from it.

## Why there is no file, and what there is instead

OpenCode keeps the conversation in SQLite (`opencode.db`), not in a per-session
file. Measured against `opencode` 1.18.32 in
`<profile>/data/opencode/opencode.db`:

| Table     | Columns that matter                                                                  |
| --------- | ------------------------------------------------------------------------------------ |
| `session` | `id`, `directory`, `title`, `time_created`, `time_updated` — what the list already reads |
| `message` | `id`, `session_id`, `time_created`, `data`; `data.role` is `user` or `assistant`      |
| `part`    | `id`, `message_id`, `session_id`, `time_created`, `data`; `data.type` and the payload |

Only `part.data.type = 'text'` carries prose (`data.text`). `reasoning`, `tool`,
`step-start`, `step-finish`, `patch` and `file` do not. So the same reduction
Claude and Codex get is available: user turns in full and in order, the model's
long tail as notes, joined through `message_id` and ordered by `time_created`.

Two complications, both already understood elsewhere in the code:

- **Isolation.** Each OpenCode launch runs on a private copy of the store
  (`instances/<run>/data/opencode/opencode.db`) that merges back on exit, so a
  session may exist in the profile database, in a live instance's copy, or both.
  The lister already unions these (`opencodeSessionDBs`); a brief reader must do
  the same, read-only.
- **Size.** The store is large — the profile database on this machine is
  179 MB, the machine-default one 6.4 GB. Read by `session_id`; never read the
  file whole.

`opencode export <sessionID>` exists as a CLI command and is the obvious way to
point at a session. It was tried against a session that is present in the
profile database, with that profile's `XDG_DATA_HOME`/`XDG_STATE_HOME`/
`XDG_CONFIG_HOME`, and it answered `Session not found`. Treat the database as
ground truth and `export` as unverified until it is made to work.

## Tier 1 — the pointer brief (the fallback that was asked for)

When the messages cannot be extracted, still write a brief; make it a short one
that points at the conversation instead of reproducing it.

- No *"What was asked"* and no *"Where the previous agent left off"* — there is
  nothing extracted to put there.
- A *"The conversation this came from"* section: the account, the session id,
  the folder, the store path, and one read-only command to open it (a `sqlite3`
  query over `message`/`part`, or `opencode export` once it works).
- *"The repository, as of the handoff"* is unchanged, and is most of the value:
  git state is collected from the folder at handoff time and depends on the
  transcript not at all.
- It is delivered exactly like any other brief: the destination is launched on
  the file, or the text is put on the clipboard when the destination cannot be
  opened on a prompt.

This tier needs one change to the wizard: `buildBrief` refuses a session that
asked nothing (*"nothing was asked in this session"*), and both the Brief step
and `renderBrief` assume an excerpt exists. Both must accept a pointer brief
with no prompts.

## Tier 2 — the real brief (preferable, and feasible)

Extract the prose and reduce it the way the other two providers are.

- Add an OpenCode branch to the message reader (a `decodeHandoffLine`
  equivalent for provider `opencode`) over the tables above, reusing
  `buildBrief`'s reduction (`maxBriefPrompts`, `minBriefNote`, `maxBriefNotes`)
  and `closingTurns`.
- `transcriptPath` cannot return a file. The reader needs a store-and-session
  reference instead, and `readSessionMessages`, `readSessionPreview` and
  `readAllMessages` must branch on provider rather than assume a file.
- The picker preview then fills for OpenCode like the others — it comes for free,
  being the same reader.
- The brief's *"If you need more"* line must stop saying *"grep it"* at a
  SQLite file; name the store, the session id, and the query/`export` command
  instead.

With tier 2, OpenCode becomes a first-class source and `doc/sessions.md` loses
its "OpenCode cannot be read" caveat. The destination side is untouched: OpenCode
still takes a brief by clipboard, because its CLI cannot be opened on a prompt
(`opensOnPrompt`).

## Non-goals

- **Antigravity.** Its store is a per-conversation SQLite database whose title
  lives in a protobuf blob with no published schema. This brief does not touch
  it.
- **Resuming** an OpenCode session — already works (`opencode --session <id>`).
- **Opening OpenCode on a prompt** — the CLI does not take one.

## What must be verified before this is called done

- A handoff from a session in the **profile database**, and one from a **live
  instance copy** (the isolation union).
- A handoff from a session whose folder has since **moved** — git state and the
  recorded folder behave as they do for Claude and Codex.
- The picker preview for OpenCode **no longer** says "records no transcript".
- Tests for the new reader beside `handoff_test.go` / `sessions_test.go`; doc
  updates in `doc/sessions.md` and the README's handoff bullet.

## Open questions for the owner

1. Pointer tier, full tier, or both (pointer only as a fallback when extraction
   fails)?
2. For the pointer: prefer `opencode export` (needs the profile's environment)
   or a raw `sqlite3` query (needs `sqlite3` on the destination machine)?
3. Does the pointer need only the session id, or also the store it was read from
   (profile database versus a live instance copy)?
