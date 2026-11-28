#!/usr/bin/env bash
# Regenerate the animated README GIFs in docs/. Builds prereview, then records
# its scripted flows with chromedp and encodes them as pure-Go GIFs (no
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
shot=$(mktemp -u /tmp/prereview-gifshot.XXXXXX)
demo=$(mktemp -d /tmp/prereview-gifdemo.XXXXXX)
demo2=$(mktemp -d /tmp/prereview-gifdemo2.XXXXXX)
extout=$(mktemp -d /tmp/prereview-gifext.XXXXXX)
log=$(mktemp /tmp/prereview-gif-log.XXXXXX)
log2=$(mktemp /tmp/prereview-gif-log2.XXXXXX)
sitelog=$(mktemp /tmp/prereview-site-log.XXXXXX)
extlog=$(mktemp /tmp/prereview-extui-log.XXXXXX)
# Isolated per-user paths, the stand-in agent and wait_ready: see capture-env.sh.
source cmd/screenshot/capture-env.sh
srv=""; srv2=""; site=""; ext=""
cleanup() {
	stop_watch
	for p in "$srv" "$srv2" "$site" "$ext"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
	rm -rf "$demo" "$demo2" "$extout" "$bin" "$shot" "$log" "$log2" "$sitelog" "$extlog" "$iso"
}
trap cleanup EXIT

# Every flow runs even when one fails: a failed flow keeps its old GIF (encode
# writes nothing after a failure), and the run exits 1 at the end, naming it.
failed=()
capture() { # <flow> <capture-tool args…>
	local flow=$1
	shift
	echo "› capturing gif:$flow"
	# Each flow starts from default view settings (theme, view, focus): the
	# server reads the saved prefs on every page load.
	rm -f "$PREREVIEW_UI_PREFS_PATH"
	"$shot" --gif "$flow" "$@" --out docs || failed+=("$flow")
}

echo "› building prereview and the capture tool"
GOWORK=off go build -o "$bin" .
GOWORK=off go build -o "$shot" ./cmd/screenshot
"$shot" -skip-first-launch

# ---- hero / image / markdown : one skill-mode demo-repo server ---------------
echo "› creating demo repo"
bash cmd/screenshot/demo-repo.sh "$demo" "$(pwd)/e2e/testdata/areacomments/diagram.png"

echo "› starting demo-repo server"
"$bin" --agent --port 0 --host 127.0.0.1 "$demo" >"$log" 2>&1 &
srv=$!
url=$(wait_ready "$log")
[ -n "$url" ] || { echo "server failed:"; cat "$log"; exit 1; }
start_watch "$demo"

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
	capture "$flow" --url "$url"
done

capture hero --url "$url" --repo "$demo"

# ---- the agent flows: each on its OWN repo, server and per-user folders -----
# These drive the agent-side subcommands and write state: versions edits
# payment.go and records versions; thread replies; checks adds a judge; learning
# accepts a learning and installs skills; alternatives records shortcut taps;
# questions asks and answers; annotations resolves every comment. A
# fresh repo, server and set of folders per flow keeps each out of the captures
# above and out of each other. The flow gets the binary via --bin.
for flow in versions thread checks learning understand alternatives questions annotations; do
	echo "› creating demo repo and folders for gif:$flow"
	rm -rf "$demo2"
	bash cmd/screenshot/demo-repo.sh "$demo2" "$(pwd)/e2e/testdata/areacomments/diagram.png"
	isolate_under "$flow"
	"$shot" -skip-first-launch
	# The checks flow's one filler word, written before the server starts so the
	# change isn't reported as an edit on disk.
	[ "$flow" = checks ] && printf '\nRetries are very cheap.\n' >>"$demo2/guide.md"

	echo "› starting server for gif:$flow"
	: >"$log2"
	"$bin" --agent --port 0 --host 127.0.0.1 "$demo2" >"$log2" 2>&1 &
	srv2=$!
	url2=$(wait_ready "$log2")
	[ -n "$url2" ] || { echo "server for gif:$flow failed:"; cat "$log2"; exit 1; }
	start_watch "$demo2"

	capture "$flow" --url "$url2" --repo "$demo2" --bin "$bin"

	stop_watch
	kill "$srv2" 2>/dev/null || true
	srv2=""
done
isolate_under base

# ---- external : demo static site proxied by prereview --external ------------
echo "› starting demo site"
GOWORK=off go run cmd/screenshot/demosite.go --port 0 >"$sitelog" 2>&1 &
site=$!
siteurl=$(wait_ready "$sitelog")
[ -n "$siteurl" ] || { echo "demo site failed:"; cat "$sitelog"; exit 1; }

echo "› starting prereview --external"
"$bin" --external "$siteurl" --out "$extout" --agent --port 0 --host 127.0.0.1 >"$extlog" 2>&1 &
ext=$!
exturl=$(wait_ready "$extlog")
[ -n "$exturl" ] || { echo "external UI failed:"; cat "$extlog"; exit 1; }
start_watch "$extout"

capture external --url "$exturl"

if [ ${#failed[@]} -gt 0 ]; then
	echo "✗ ${#failed[@]} flow(s) failed, their GIFs left as they were: ${failed[*]}"
	exit 1
fi
echo "› done → docs/*.gif (look at every frame: go run ./cmd/screenshot -dump docs/<name>.gif -out <dir>)"
