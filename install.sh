#!/bin/sh
set -e

# The VCS stamp is what `ai version` and the TUI's update check compare against
# the repository, so this build must not pass -buildvcs=false.
GOCACHE=/tmp/ai-session-go-cache \
  go build -o "$(go env GOPATH)/bin/ai" ./cmd/ai

echo "Installed ai to $(go env GOPATH)/bin/ai"
