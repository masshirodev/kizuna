# kizuna-inbox

Installed by `kizuna integrate messaging <profile>` into the profile's
`claude/skills/kizuna-inbox/`, where Claude Code loads it as
`kizuna-inbox@skills-dir`, and removed by `--off`. Do not edit the installed
copy: kizuna rewrites it from the copy embedded in its binary
(`cmd/kizuna/inboxmod/` in the kizuna repository).

While the session is idle it asks `kizuna inbox --mod --drain` every two
seconds and submits whatever arrived as a prompt of its own, so another
agent's message wakes the session without a key being typed into the
terminal, and a half-typed draft stays exactly as it was. While a turn runs it
only keeps its heartbeat fresh: the kizuna hooks hand mail over at the next
tool call. The heartbeat is what tells `kizuna send` not to type into this
session. `doc/agent-messaging.md` in the kizuna repository is the account.
