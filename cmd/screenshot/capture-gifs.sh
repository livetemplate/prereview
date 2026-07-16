#!/usr/bin/env bash
# Regenerate the animated README GIFs in docs/. Builds prereview, then records
# nine scripted flows with chromedp and encodes them as pure-Go GIFs (no
# ffmpeg/gifsicle). One command so the GIFs never drift:
#
#   make gifs
#
# hero/image/markdown/suggestion/search/themes run against one agent-mode
# demo-repo server; versions/thread run against a SECOND, throwaway demo-repo
# server (so their seeded agent state — recorded versions, replies, status — never
# bleeds into the other flows' captures); external runs against a demo static site
# proxied by `prereview --external`.
#
# Requires a chromium/chrome on PATH (or the NixOS path the helper probes).
set -euo pipefail

cd "$(dirname "$0")/../.." # repo root

bin=$(mktemp -u /tmp/prereview-gif.XXXXXX)
demo=$(mktemp -d /tmp/prereview-gifdemo.XXXXXX)
demo2=$(mktemp -d /tmp/prereview-gifdemo2.XXXXXX)
extout=$(mktemp -d /tmp/prereview-gifext.XXXXXX)
log=$(mktemp /tmp/prereview-gif-log.XXXXXX)
log2=$(mktemp /tmp/prereview-gif-log2.XXXXXX)
sitelog=$(mktemp /tmp/prereview-site-log.XXXXXX)
extlog=$(mktemp /tmp/prereview-extui-log.XXXXXX)
# Isolate the per-USER view prefs so captures are deterministic (default
# scheme/mode, rendered — not raw — Markdown) regardless of the developer's real
# ~/.config/prereview/ui-prefs.json, and so the themes flow's scheme changes
# don't pollute it. Same trick the e2e suite uses (prefsIsolatedEnv). A -u path
# doesn't exist yet, so the server starts from clean defaults and writes here.
prefs=$(mktemp -u /tmp/prereview-gifprefs.XXXXXX)
export PREREVIEW_UI_PREFS_PATH="$prefs"
srv=""; srv2=""; site=""; ext=""
cleanup() {
	for p in "$srv" "$srv2" "$site" "$ext"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	rm -rf "$demo" "$demo2" "$extout" "$bin" "$log" "$log2" "$sitelog" "$extlog" "$prefs"
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
PREREVIEW_NO_UPDATE=1 "$bin" --agent --port 0 --host 127.0.0.1 "$demo" >"$log" 2>&1 &
srv=$!
url=$(wait_ready "$log")
[ -n "$url" ] || { echo "server failed:"; cat "$log"; exit 1; }

# Seed a suggestion (agent → user, issue #98) for the suggestion flow: the
# `suggest` subcommand appends to .prereview/suggestions.jsonl, which the server
# surfaces as an inline before→after box on guide.md.
echo "› seeding a suggestion"
"$bin" suggest --out "$demo" <<'JSON'
{"id":"sg-guide","file":"guide.md","from_line":12,"to_line":12,"original":"Transient gateway errors are retried with backoff.","proposed":"Transient gateway errors are retried with exponential backoff, up to maxRetries attempts.","note":"be specific about the backoff strategy"}
JSON

# image + the read-only flows (suggestion/search/themes) first — they read the
# pristine demo tree. hero runs LAST because it edits payment.go on disk (the
# scripted "Claude fix"); running it after the others keeps their diffs pristine.
# suggestion runs before markdown so guide.md carries only the suggestion box
# (no stray comment) in that capture.
for flow in image suggestion search themes markdown; do
	echo "› capturing gif:$flow"
	GOWORK=off go run ./cmd/screenshot --gif "$flow" --url "$url" --out docs
done

echo "› capturing gif:hero"
GOWORK=off go run ./cmd/screenshot --gif hero --url "$url" --repo "$demo" --out docs

# ---- versions / thread : each on its OWN throwaway demo-repo server ----------
# These flows drive agent-side subcommands (status/reply) that mutate the store —
# recorded versions, agent replies, edited files. A fresh repo + server PER FLOW
# keeps that state out of the flows above (whose captures assume a pristine tree
# and no status pill) AND out of each other (versions edits payment.go; thread
# comments on the retry loop and needs it un-edited). The Go flow gets the binary
# via --bin so it can invoke `prereview status`/`reply` itself.
for flow in versions thread; do
	echo "› creating demo repo for gif:$flow"
	rm -rf "$demo2"
	bash cmd/screenshot/demo-repo.sh "$demo2" "$(pwd)/e2e/testdata/areacomments/diagram.png"

	echo "› starting server for gif:$flow"
	: >"$log2"
	PREREVIEW_NO_UPDATE=1 "$bin" --agent --port 0 --host 127.0.0.1 "$demo2" >"$log2" 2>&1 &
	srv2=$!
	url2=$(wait_ready "$log2")
	[ -n "$url2" ] || { echo "server for gif:$flow failed:"; cat "$log2"; exit 1; }

	echo "› capturing gif:$flow"
	GOWORK=off go run ./cmd/screenshot --gif "$flow" --url "$url2" --repo "$demo2" --bin "$bin" --out docs

	kill "$srv2" 2>/dev/null || true
	srv2=""
done

# ---- external : demo static site proxied by prereview --external ------------
echo "› starting demo site"
GOWORK=off go run cmd/screenshot/demosite.go --port 0 >"$sitelog" 2>&1 &
site=$!
siteurl=$(wait_ready "$sitelog")
[ -n "$siteurl" ] || { echo "demo site failed:"; cat "$sitelog"; exit 1; }

echo "› starting prereview --external"
PREREVIEW_NO_UPDATE=1 "$bin" --external "$siteurl" --out "$extout" --agent --port 0 --host 127.0.0.1 >"$extlog" 2>&1 &
ext=$!
exturl=$(wait_ready "$extlog")
[ -n "$exturl" ] || { echo "external UI failed:"; cat "$extlog"; exit 1; }

echo "› capturing gif:external"
GOWORK=off go run ./cmd/screenshot --gif external --url "$exturl" --out docs

echo "› done → docs/*.gif"
