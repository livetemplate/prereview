#!/usr/bin/env bash
# Regenerate the animated README GIFs in docs/. Builds prereview, then records
# four scripted flows with chromedp and encodes them as pure-Go GIFs (no
# ffmpeg/gifsicle). One command so the GIFs never drift:
#
#   make gifs
#
# hero/image/markdown run against one skill-mode demo-repo server; external runs
# against a demo static site proxied by `prereview --external`.
#
# Requires a chromium/chrome on PATH (or the NixOS path the helper probes).
set -euo pipefail

cd "$(dirname "$0")/../.." # repo root

bin=$(mktemp -u /tmp/prereview-gif.XXXXXX)
demo=$(mktemp -d /tmp/prereview-gifdemo.XXXXXX)
extout=$(mktemp -d /tmp/prereview-gifext.XXXXXX)
log=$(mktemp /tmp/prereview-gif-log.XXXXXX)
sitelog=$(mktemp /tmp/prereview-site-log.XXXXXX)
extlog=$(mktemp /tmp/prereview-extui-log.XXXXXX)
srv=""; site=""; ext=""
cleanup() {
	for p in "$srv" "$site" "$ext"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	rm -rf "$demo" "$extout" "$bin" "$log" "$sitelog" "$extlog"
}
trap cleanup EXIT

wait_ready() { # <logfile> -> echoes the READY url
	local f="$1"
	for _ in $(seq 1 40); do grep -q '^READY ' "$f" && break; sleep 0.3; done
	grep -m1 '^READY ' "$f" | awk '{print $2}'
}

echo "› building prereview"
GOWORK=off go build -o "$bin" .

# ---- hero / image / markdown : one skill-mode demo-repo server ---------------
echo "› creating demo repo"
bash cmd/screenshot/demo-repo.sh "$demo" "$(pwd)/e2e/testdata/areacomments/diagram.png"

echo "› starting demo-repo server"
PREREVIEW_NO_UPDATE=1 "$bin" --skill --port 0 --host 127.0.0.1 "$demo" >"$log" 2>&1 &
srv=$!
url=$(wait_ready "$log")
[ -n "$url" ] || { echo "server failed:"; cat "$log"; exit 1; }

# image/markdown first — they read the pristine demo tree. hero runs LAST
# because it edits payment.go on disk (the scripted "Claude fix") to show the
# review→fix loop close; running it after the others keeps their diffs pristine.
for flow in image markdown; do
	echo "› capturing gif:$flow"
	GOWORK=off go run ./cmd/screenshot --gif "$flow" --url "$url" --out docs
done

echo "› capturing gif:hero"
GOWORK=off go run ./cmd/screenshot --gif hero --url "$url" --repo "$demo" --out docs

# ---- external : demo static site proxied by prereview --external ------------
echo "› starting demo site"
GOWORK=off go run cmd/screenshot/demosite.go --port 0 >"$sitelog" 2>&1 &
site=$!
siteurl=$(wait_ready "$sitelog")
[ -n "$siteurl" ] || { echo "demo site failed:"; cat "$sitelog"; exit 1; }

echo "› starting prereview --external"
PREREVIEW_NO_UPDATE=1 "$bin" --external "$siteurl" --out "$extout" --skill --port 0 --host 127.0.0.1 >"$extlog" 2>&1 &
ext=$!
exturl=$(wait_ready "$extlog")
[ -n "$exturl" ] || { echo "external UI failed:"; cat "$extlog"; exit 1; }

echo "› capturing gif:external"
GOWORK=off go run ./cmd/screenshot --gif external --url "$exturl" --out docs

echo "› done → docs/*.gif"
