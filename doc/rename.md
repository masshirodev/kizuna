# The rename: ai-session is kizuna

Back to the [README](../README.md).

Until 2026-10 this project was **ai-session**, its command was **`ai`**, and it
kept its files in `~/.config/ai`. It is **kizuna** now. `ai` is about as
generic as a command name gets: the AUR already had a package providing it,
and a tool published for strangers cannot claim it on their `PATH`, or
`~/.config/ai`, or `AI_PROFILE` in their environment.

## What moved

| Before | Now |
| ------ | --- |
| repository `masshirodev/ai-session` | `masshirodev/kizuna` (GitHub redirects the old name) |
| Go module `github.com/masshiro/ai-session` | `github.com/masshirodev/kizuna` (the old path never matched the repository, so `go install` from it could not work) |
| command `ai`, built from `cmd/ai` | `kizuna`, from `cmd/kizuna` |
| `~/.config/ai/` | `~/.config/kizuna/`, with a link left at the old path |
| `AI_PROFILE`, `AI_PROVIDER`, `AI_INSTANCE_DIR`, `AI_CONFIG_HOME`, `AI_SOURCE_DIR` | `KIZUNA_PROFILE`, `KIZUNA_PROVIDER`, `KIZUNA_INSTANCE_DIR`, `KIZUNA_CONFIG_HOME`, `KIZUNA_SOURCE_DIR` |
| the messaging MCP server `ai` (tools `mcp__ai__*`) | `kizuna` (`mcp__kizuna__*`) |
| tmux indicator sockets `ai-<hash>.sock` | `kizuna-<hash>.sock` |

## How an existing install gets there

Nothing has to be done by hand. An older `ai` finds the update the way it
always did: its update check asks GitHub about `masshirodev/ai-session`, which
redirects, and `ai self-update` pulls its managed clone (the redirect again)
and runs that clone's `install.sh`. That script is the new one, so it:

1. builds `kizuna` into `$(go env GOPATH)/bin`;
2. sees the old `ai` binary beside it (a real file whose help is ai-session's)
   and replaces it with a link to `kizuna`, saying so. The link keeps the
   command working, keeps working the hooks and MCP servers that name the old
   binary's path, and is what self-update's own reopen runs: it execs the path
   it was started from.

The first `kizuna` to start, under either name, then migrates
(`cmd/kizuna/migrate.go`):

- **The directory.** `~/.config/ai` is renamed to `~/.config/kizuna`, and a
  relative link `ai -> kizuna` takes its place. It only does this when the old
  directory holds a `profiles.json` (so another tool's `~/.config/ai` is never
  touched) and the new one does not exist yet (so it runs once, and never over
  a directory kizuna already has).
- **The status line.** Each Claude Code and Antigravity profile whose
  `statusLine` is exactly the one ai-session installed (it read `AI_PROFILE`)
  gets the one that reads `KIZUNA_PROFILE`. Anything else there is left alone.
- **Messaging.** Each profile with the messaging integration on gets it again:
  the hooks and the MCP server now run the current binary, and the server is
  registered as `kizuna`, with the old `ai` entry removed when it is ours (it
  runs `mcp serve`).

It prints one line per thing it did, on stderr, and nothing on any later run.

## Sessions that were already running

A session an older binary launched keeps running until it is closed, and its
environment still says `AI_PROFILE` and names paths through `~/.config/ai`.
That is why the old path stays a link rather than going away, and why kizuna
still reads every old variable when the new one is not set: `kizuna inbox`,
`kizuna send` and the hooks inside such a session keep working, `kizuna
compact` still sees that session holding its store, and `h` still attaches to
its tmux status bar under the old socket name.

A session launched by kizuna gets only the new names. Something of your own
that reads `AI_PROFILE` (a prompt, a script) should read `KIZUNA_PROFILE`.

## Keeping the redirect

GitHub redirects `masshirodev/ai-session` for as long as nothing else takes
that name. **Never create a repository called `ai-session` under the same
owner**: it would break the update path of every `ai` that has not updated yet.

## Removing the leftovers

Once no session an older binary started is running, the link at
`~/.config/ai` can go (`rm ~/.config/ai`, which removes the link, not the
directory). The `ai` link in `$(go env GOPATH)/bin` can go too, once nothing
you run calls `ai`; `kizuna integrate messaging <profile>` points the hooks
at `kizuna` first if they still name it.
