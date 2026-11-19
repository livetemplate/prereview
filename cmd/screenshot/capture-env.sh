# Sourced by capture-readme.sh and capture-gifs.sh (after they set $bin).
#
# Points every per-user path prereview reads or writes at one fresh temp
# folder, so a capture neither shows the developer's own learnings, skills and
# checks in public README assets nor writes to their real ~/.config/prereview
# or ~/.claude (accepting a learning installs skills and edits the agents'
# memory files and Claude Code settings). The e2e suite isolates the same paths.
#
# Sets $iso; the caller removes it in its cleanup trap, and calls stop_watch.
iso=$(mktemp -d /tmp/prereview-capture-home.XXXXXX)
export PREREVIEW_LIBRARY_DIR="$iso/library"   # learnings, jobs, the first-launch answer
export PREREVIEW_UI_PREFS_PATH="$iso/ui-prefs.json" # scheme, mode, view: captures start from defaults
export PREREVIEW_HOME="$iso/home"             # where agent skills and memory files are written
export PREREVIEW_SCAN_HOME="$iso/scan"        # where past reviews are looked for
export PREREVIEW_CHECKS_DIR="$iso/checks"     # the user's own checks
export PREREVIEW_NO_UPDATE=1
watcher=""

# A stand-in agent: a real `prereview watch` looping on the queue, so every
# toolbar shows a connected agent (#194: with no consumer the Queue dot is
# hollow). It re-runs from the highest seq seen, as an agent does.
start_watch() { # <out>
	stop_watch
	(
		since=-1
		while :; do
			s=$("$bin" watch --out "$1" --since "$since" 2>/dev/null | grep -o '"seq":[0-9]*' | tail -1 | cut -d: -f2) || true
			[ -n "$s" ] && since=$s
			sleep 0.2
		done
	) &
	watcher=$!
}
stop_watch() {
	[ -n "$watcher" ] || return 0
	kill "$watcher" 2>/dev/null || true
	# The blocking watch runs inside a command substitution, a grandchild of the
	# loop; $bin is this run's own temp binary, so matching it kills only ours.
	pkill -f -- "$bin watch --out" 2>/dev/null || true
	watcher=""
}

# Answers the first-launch card ("Not now") if the review shows it: the
# library is fresh, so a later server sees an earlier one's review as a past
# review and would offer to learn from it in every frame.
prepare() { # <url>
	GOWORK=off go run ./cmd/screenshot --prepare --url "$1"
}
