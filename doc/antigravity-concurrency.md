# Several Antigravity instances of one profile

Why an Antigravity profile can run more than one `agy` at once since
2026-10-27, and what keeps two of them off one conversation. Card
`c189-run-several-antigravity` ("Run several Antigravity (agy) instances of one
profile at once") on the Kizuna board. Back to
[running.md](running.md#running-several-at-once).

## What used to stop it

Antigravity profiles took the exclusive profile lock, so a second launch was
refused while the first ran. The reason given was that agy's file-backed OAuth
store was "not known to coordinate token refreshes across processes". Nobody
had checked it.

## What was checked (agy 1.4.7, the `gemini` profile)

- **The token file is not written on refresh.**
  `~/.gemini/antigravity-cli/antigravity-oauth-token` was last written on
  2026-09-24, a month of daily use earlier. agy refreshes its access token in
  memory, and a Google refresh token does not rotate, so two processes have
  nothing to race on in that file.
- **Two `agy -p` runs at once both finished**, and the token file's hash was the
  same before and after. Through kizuna, two launches of the profile were listed
  by `kizuna peers` side by side and both finished.
- **agy marks every open conversation.** While a conversation is open, its
  process holds an exclusive `flock` on
  `~/.gemini/antigravity-cli/presence/<conversation-id>.lock`. There is one such
  file per conversation, and the lock is released when the process lets go of the
  conversation.
- **agy does not refuse a second writer.** `agy --conversation <id> -p …` ran a
  turn on a conversation another process held. Two processes appending steps to
  one conversation interleave them.

The rest of the provider home is safe to share: `conversation_summaries.db` is
SQLite in WAL mode, `history.jsonl` is appended to, and each conversation has a
database of its own.

## What kizuna does now

- **Concurrent runs.** Antigravity joins Codex and Claude in
  `supportsConcurrentRuns` (`cmd/kizuna/instances.go`): every launch gets its own
  `instances/run-XXX` directory, and all of them share the profile's private
  `HOME`. Its inbox moves to the instance directory with it, like the others'.
- **Which conversation each instance has open** comes from the presence locks
  (`cmd/kizuna/antigravity_presence.go`). kizuna reads `/proc/locks` and matches
  each held flock to a presence file by device and inode. It never takes the lock
  itself, because an agy opening that conversation at that moment would find it
  taken. The holder's PID is walked up its process chain to the nearest running
  instance, so an `agy` started from inside another instance's shell is credited
  to its own launch. That gives the instance its conversation id in `kizuna
  peers`, the `K` picker and the hijack picker, and its title once agy has
  written one into its summaries.
- **No second writer.** Resuming a conversation another instance holds is
  refused, both from the `R` picker and with `kizuna <profile> resume <id>`. The
  message names the PID holding it:

  ```
  error: that conversation is open in a running agy (PID 1397699), and two agy processes on one conversation interleave its steps; close it there first
  ```

- **No hijack.** Hijacking reopens exactly the conversation the instance has
  open, so it is refused for Antigravity with the reason, instead of being left
  to agy, which would allow it.

## Limits

- **Linux only for the guard.** Without `/proc/locks` nothing reads as held, so
  on macOS a held conversation is not refused. The keyring bypass in
  [running.md](running.md#antigravitys-private-home) is Linux-only already.
- **A conversation agy has not opened yet is not held.** In the first few seconds
  of a launch, before agy takes the lock, `kizuna peers` shows the instance
  without a conversation, and a resume of that conversation is not refused.
- **A changed agy.** If a later agy stops writing presence locks, instances lose
  their titles and the guard refuses nothing: it reads as "nothing held", never as
  the wrong thing held. If a later agy starts rotating its refresh token on
  disk, the token finding above has to be checked again.
