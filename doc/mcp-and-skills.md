# MCP servers and skills

Copying MCP servers and skills between profiles, including across providers.
Back to the [README](../README.md).

An MCP server is configured once and then wanted everywhere — the same
endpoint, the same token, in every account that has to reach it. Skills are the
same story with folders instead of settings. Both move between profiles without
touching anything else those profiles hold:

```sh
ai mcp list max
# context7           http    https://mcp.context7.com/mcp
# lattice            http    https://apps.example/api/mcp
ai mcp copy max pro context7 lattice     # named servers
ai mcp copy max pro                      # or all of them

ai skill list max
ai skill copy max pro release
```

An entry the destination already has is refused rather than overwritten, and
the error names `--replace`, which is how you say you meant it. Every name is
checked before the first one is written, so a call naming one server that is
already there leaves the destination exactly as it was rather than half applied.
Both commands are refused while the destination is running: its CLI reads that
config at startup and rewrites parts of it as it goes, and editing the file
underneath a live process is a race whose loser is whichever wrote first.

## The server is translated, not pasted

Each CLI writes MCP configuration in its own syntax, in its own file, so what
crosses between two profiles cannot be a fragment of one provider's config — it
has to be the server itself. `ai` reads whichever of these the source keeps, and
writes whichever the destination expects:

| Provider | File | Key |
| -------- | ---- | --- |
| Claude Code | `claude/.claude.json` | `mcpServers` |
| Codex | `codex/config.toml` | `[mcp_servers.<name>]` |
| OpenCode | `config/opencode/opencode.json[c]` | `mcp` |
| Antigravity | `home/.gemini/config/mcp_config.json` | `mcpServers` |

A stdio server carries its command, arguments, and environment; a remote one
carries its URL and headers. Nothing else does: a setting invented for one CLI
means nothing in another, and guessing at a translation is how a copy silently
changes what a server does. Copying a Codex server leaves its per-tool approval
settings behind for exactly that reason.

Header values cross translated, not verbatim — and the CLIs disagree about how
an environment variable is named inside one. Claude Code and Antigravity
expand `${VAR}` (and `$VAR`); OpenCode wants `{env:VAR}`. A copied remote
server has those references rewritten into the destination's spelling, in
headers, URLs, environment values, and arguments alike, so a server that
authenticated in one profile authenticates in the next without a hand edit.

Codex interpolates nothing and keeps environment-sourced values in keys of
their own, so the translation goes through those: a header that is exactly
one reference becomes `env_http_headers`, an `Authorization: Bearer ${VAR}`
becomes `bearer_token_env_var`, and a stdio variable passed through unchanged
(`KEY` carrying `${KEY}`) becomes an `env_vars` entry. Reading goes the other
way, so a Codex server copied elsewhere arrives with its indirections intact
and a Codex-to-Codex copy round-trips byte for byte in meaning.

Two gaps remain, both on Codex's side. A reference embedded in a longer value
(`prefix-${VAR}`) and a variable passed under another variable's name have no
spelling there and are kept literal — Codex sends them as written, so check
the copy when it carries one. And `${VAR:-default}` loses its default on the
way into OpenCode, which has no spelling for one.

Two consequences worth knowing. Every other key of the destination's file is
left byte for byte as it was, in the order it was already in — `.claude.json` is
Claude Code's own ninety-kilobyte state file, and a copy that reordered it would
be impossible to read back to check what it actually changed. And an
`opencode.jsonc` with comments in it is **refused** rather than rewritten,
because `encoding/json` cannot write those comments back and deleting them
silently is worse than not copying.

DeepSeek profiles are refused for both: `ai` sets no XDG directories for them,
so they read the machine's ordinary OpenCode config rather than an isolated one
this launcher may write into.

## Skills are files, and cross unchanged

A skill is a folder with a `SKILL.md` in it, which is the one thing Claude Code,
Codex, and OpenCode all agree on — so unlike an MCP server, a skill needs no
translation. Only the folder differs: `claude/skills/`, `codex/skills/`,
`config/opencode/skills/`. Antigravity has no skill mechanism and is refused.

A directory without a `SKILL.md` is not a skill and is passed over — Codex keeps
its built-ins under a dotted folder beside the real ones. A replacement removes
the old folder first, so a replaced skill cannot end up with a `SKILL.md` from
one and half the scripts of another.

This does copy files, and the files are whatever the skill's author put in them,
scripts included. `ai` moves them between two directories that already belong to
you; it does not read them, and it is not a way to accept a skill from anyone
else.

## From the TUI

`C` clones the selected profile after asking for a name. `m` and `s` install
MCP servers and skills **into** it from another profile — the selected profile
is the destination, the same way `l` logs into it and `u` updates its CLI.

Both open the same two frames: a list of the profiles with something to lend,
and then a multi-select of what that profile has. Space ticks a row, `a` ticks
every row the destination does not already have, and Enter installs the ticked
ones. A row the destination already has is shown as `installed` and left
unticked by `a`, so replacing one is something done on purpose to a row you
looked at rather than a side effect of a bulk key.
