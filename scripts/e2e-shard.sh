#!/usr/bin/env bash
# Run one shard of the browser e2e suite.
#
#   scripts/e2e-shard.sh <shard-index (1-based)> <total-shards>
#
# The suite is ~145 chromedp tests with no t.Parallel, so it is strictly
# serial within a process; sharding across runners is what makes it viable as
# a required check.
#
# This script exists mostly for its GUARDS. There are two ways this job can go
# green while testing nothing at all, and a required check that silently tests
# nothing is worse than no check:
#
#   1. No browser on the runner. findChromium (e2e/e2e_test.go) calls t.Skip
#      when it can't find one, and `go test` prints "ok" for a package whose
#      tests all skipped. A missing Chrome would read as a pass.
#   2. A -run pattern that matches no tests. `go test -run 'Nope'` also exits
#      0 and prints "ok".
#
# So: fail loudly if there is no browser, refuse an empty shard, and assert
# afterwards that the number of tests that actually ran matches the number
# this shard selected.
set -euo pipefail

shard="${1:?usage: e2e-shard.sh <shard-index-1-based> <total-shards>}"
total="${2:?usage: e2e-shard.sh <shard-index-1-based> <total-shards>}"

if ! [[ "$shard" =~ ^[0-9]+$ && "$total" =~ ^[0-9]+$ ]] || (( shard < 1 || total < 1 || shard > total )); then
  echo "FAIL: bad shard spec ${shard}/${total}" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
export GOWORK=off

# --- guard 1: a browser must exist, or every test would skip into a green pass.
# Keep this list in sync with findChromium in e2e/e2e_test.go.
browser=""
for c in /usr/bin/google-chrome /usr/bin/chromium /usr/bin/chromium-browser /usr/bin/chrome; do
  [ -x "$c" ] && { browser="$c"; break; }
done
[ -z "$browser" ] && browser="$(command -v chromium || command -v google-chrome || true)"
if [ -z "$browser" ]; then
  echo "FAIL: no chromium/chrome on this runner — the e2e suite would skip every" >&2
  echo "      test and report success. Install a browser or drop this job." >&2
  exit 1
fi
echo "browser: $browser ($("$browser" --version 2>/dev/null || echo 'version unknown'))"

# --- partition. Round-robin (NR % total) rather than contiguous blocks so a
# cluster of slow neighbours doesn't land entirely in one shard.
mapfile -t all < <(go test -tags browser -list 'Test' ./e2e/ | grep '^Test' | sort)
if (( ${#all[@]} == 0 )); then
  echo "FAIL: go test -list found no tests — wrong package or build tag?" >&2
  exit 1
fi
mapfile -t mine < <(printf '%s\n' "${all[@]}" | awk -v s="$shard" -v n="$total" 'NR % n == (s - 1) % n')

# --- guard 2: refuse to "pass" an empty shard.
if (( ${#mine[@]} == 0 )); then
  echo "FAIL: shard ${shard}/${total} selected 0 of ${#all[@]} tests" >&2
  exit 1
fi
echo "shard ${shard}/${total}: ${#mine[@]} of ${#all[@]} tests"

pattern="^($(IFS='|'; echo "${mine[*]}"))$"
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

set +e
go test -tags browser -v -count=1 -timeout 45m -run "$pattern" ./e2e/ 2>&1 | tee "$log"
status="${PIPESTATUS[0]}"
set -e

# --- guard 3: the tests must actually have RUN. Counts the top-level result
# lines, which subtests don't produce at column 0.
ran="$(grep -cE '^--- (PASS|FAIL|SKIP)' "$log" || true)"
skipped="$(grep -cE '^--- SKIP' "$log" || true)"
echo "shard ${shard}/${total}: selected=${#mine[@]} ran=${ran} skipped=${skipped} exit=${status}"

if (( ran != ${#mine[@]} )); then
  echo "FAIL: selected ${#mine[@]} tests but only ${ran} produced a result." >&2
  echo "      A green exit here would not mean the suite passed." >&2
  exit 1
fi
if (( skipped > 0 )); then
  echo "FAIL: ${skipped} test(s) skipped. On a runner with a browser nothing should" >&2
  echo "      skip; a skip means an unmet precondition silently reduced coverage." >&2
  grep -E '^--- SKIP' "$log" >&2 || true
  exit 1
fi

exit "$status"
