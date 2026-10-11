#!/bin/sh
# Builds kizuna into $(go env GOPATH)/bin. doc/RELEASING.md covers the packaged
# builds; this is the build from source, and the one self-update runs.
set -e

bin="$(go env GOPATH)/bin"

# The VCS stamp is what `kizuna version` and the TUI's update check compare
# against the repository, so this build must not pass -buildvcs=false.
GOCACHE="${GOCACHE:-$(go env GOCACHE)}" \
  go build -o "$bin/kizuna" ./cmd/kizuna
echo "Installed kizuna to $bin/kizuna"

# kizuna was ai-session, a command called `ai`, until 2026-10. An older `ai`
# that self-updates pulls this repository and runs this script, so this is
# where its user learns about the rename: the old binary becomes a link to the
# new one, which keeps the command, the hooks and MCP servers that name its
# path, and the self-update's own reopen (it execs the old path) working.
# Anyone else gets no `ai`: the name is too generic to take on a stranger's
# PATH. --alias asks for it anyway.
old_ai=
if [ -f "$bin/ai" ] && [ ! -L "$bin/ai" ] && "$bin/ai" help 2>/dev/null | grep -q 'isolated local profiles for AI CLI accounts'; then
  old_ai=yes
  echo
  echo "ai-session is kizuna now: the command is \`kizuna\`, and \`ai\` stays as a link to it."
  echo "The next run moves ~/.config/ai to ~/.config/kizuna and leaves a link behind."
  echo "doc/rename.md has the rest."
fi
if [ -n "$old_ai" ] || [ -L "$bin/ai" ] || { [ "$1" = "--alias" ] && [ ! -e "$bin/ai" ]; }; then
  ln -sfn kizuna "$bin/ai"
  echo "Linked $bin/ai -> kizuna"
elif [ "$1" = "--alias" ]; then
  echo "Left $bin/ai alone: it is not a link, and not an older ai-session" >&2
fi
