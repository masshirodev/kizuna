#!/bin/sh
# Records doc/assets/demo.gif: builds kizuna, stages the made-up accounts
# (stage.py) under a scratch home, and runs demo.tape with that home and a PATH
# of its own, so nothing of the machine recording it shows up in the gif.
# Needs vhs (and its ttyd and a Chromium); see scripts/demo/README.md.
set -eu

repo=$(cd "$(dirname "$0")/../.." && pwd)
work=${DEMO_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/kizuna-demo.XXXX")}
mkdir -p "$work/bin"

# Linked as a packaged build so the title bar carries no "behind main" banner.
(cd "$repo" && go build -ldflags "-X 'main.packaged=the demo'" -o "$work/bin/kizuna" ./cmd/kizuna)
home=$(python3 "$repo/scripts/demo/stage.py" "$work/world")

tools=$(dirname "$(command -v vhs)")
cd "$repo"
env -i HOME="$home" TERM=xterm-256color LANG=C.UTF-8 \
  PATH="$work/bin:$home/.local/bin:$tools:/usr/bin:/bin" \
  vhs scripts/demo/demo.tape

[ -n "${DEMO_DIR:-}" ] || rm -rf "$work"
echo "wrote doc/assets/demo.gif"
