#!/usr/bin/env sh
# The suite, failing below the coverage floor.
#
#   scripts/coverage.sh
#
# go test has no fail-under flag, so this totals the profile and compares. The
# number is statement coverage: Go measures statements, not branches or
# functions, so "77%" here is a smaller promise than it is in a Node
# repository. doc/TESTING.md says so too.
#
# Packages with no test file still count, at 0%: go test puts their statements
# in the profile, so a new untested package lowers the total rather than
# hiding from it. Each package is credited only by its own tests (no
# -coverpkg), so code that is merely run by another package's tests is not
# counted as tested.
#
# This file is the repository's: excluding generated code from the profile is
# a fair edit. The floor is not; `hajime check` holds FLOOR at the repository's
# floor (`floor` in .hajime.json) or above, and that floor only goes up.
set -eu
cd "$(dirname "$0")/.."

FLOOR=77
# Under coverage/ (gitignored), not /tmp: /tmp is RAM on the workstation.
PROFILE=coverage/coverage.out

mkdir -p coverage
go test -coverprofile="$PROFILE" ./...

total=$(go tool cover -func="$PROFILE" | awk '$1 == "total:" { sub(/%$/, "", $NF); print $NF }')
if [ -z "$total" ]; then
	echo "coverage: no total in $PROFILE" >&2
	exit 1
fi

if awk -v t="$total" -v f="$FLOOR" 'BEGIN { exit !(t + 0 >= f + 0) }'; then
	echo "coverage: ${total}% of statements, floor ${FLOOR}%"
else
	echo "coverage: ${total}% of statements is below the ${FLOOR}% floor" >&2
	echo "  go tool cover -func=$PROFILE names the functions short of it" >&2
	exit 1
fi
