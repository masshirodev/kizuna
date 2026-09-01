#!/bin/sh
set -e

# The VCS stamp is what `ai version` and the TUI's update check compare against
# the repository, so this build must not pass -buildvcs=false.
GOCACHE=/tmp/ai-session-go-cache \
  go build -o "$(go env GOPATH)/bin/ai" ./cmd/ai

# A Go binary carries the commit it was built from but not the directory, and
# `ai self-update` needs the checkout to pull and rebuild. One plain line, so
# this stays a printf rather than a JSON escaping problem.
config_dir="${XDG_CONFIG_HOME:-$HOME/.config}/ai"
mkdir -p "$config_dir"
printf '%s\n' "$(pwd -P)" >"$config_dir/source"

echo "Installed ai to $(go env GOPATH)/bin/ai"
