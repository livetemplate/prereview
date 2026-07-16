#!/usr/bin/env bash
# Regenerate the README screenshots in docs/. Builds prereview, spins up a demo
# repo + skill-mode server, drives chromedp via `cmd/screenshot --readme`, then
# tears everything down. One command so the screenshots never drift again:
#
#   make screenshots
#
# Requires a chromium/chrome on PATH (or the NixOS path the helper probes).
set -euo pipefail

cd "$(dirname "$0")/../.." # repo root

bin=$(mktemp -u /tmp/prereview-shot.XXXXXX)
demo=$(mktemp -d /tmp/prereview-demo.XXXXXX)
log=$(mktemp /tmp/prereview-shot-log.XXXXXX)
# Isolate the per-user view prefs so shots use the default scheme/mode and
# rendered (not raw) Markdown regardless of the developer's real config (same as
# the e2e suite / the gif script).
prefs=$(mktemp -u /tmp/prereview-shotprefs.XXXXXX)
export PREREVIEW_UI_PREFS_PATH="$prefs"
srv=""
cleanup() {
	[ -n "$srv" ] && kill "$srv" 2>/dev/null || true
	rm -rf "$demo" "$bin" "$log" "$prefs"
}
trap cleanup EXIT

echo "› building prereview"
GOWORK=off go build -o "$bin" .

echo "› creating demo repo"
bash cmd/screenshot/demo-repo.sh "$demo" "$(pwd)/e2e/testdata/areacomments/diagram.png"

echo "› starting server"
PREREVIEW_NO_UPDATE=1 "$bin" --agent --port 0 --host 127.0.0.1 "$demo" >"$log" 2>&1 &
srv=$!
for _ in $(seq 1 30); do
	grep -q '^READY ' "$log" && break
	sleep 0.3
done
url=$(grep -m1 '^READY ' "$log" | awk '{print $2}')
[ -n "$url" ] || {
	echo "server failed to start:"
	cat "$log"
	exit 1
}

echo "› capturing from $url"
GOWORK=off go run ./cmd/screenshot --readme --url "$url" --out docs

echo "› done → docs/"
