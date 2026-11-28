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
isolate_under() { # <name>: a fresh set of per-user folders under $iso/<name>
	# The library sits in the agent home, as it does by default, so pages that
	# show its paths show ~/.config/prereview/…, not a temp folder.
	export PREREVIEW_LIBRARY_DIR="$iso/$1/home/.config/prereview" # learnings, jobs, the first-launch answer
	export PREREVIEW_UI_PREFS_PATH="$iso/$1/ui-prefs.json" # scheme, mode, view: captures start from defaults
	export PREREVIEW_HOME="$iso/$1/home"                # where agent skills and memory files are written
	export PREREVIEW_SCAN_HOME="$iso/$1/scan"           # where past reviews are looked for
	export PREREVIEW_CHECKS_DIR="$iso/$1/checks"        # the user's own checks
}
isolate_under base
export PREREVIEW_NO_UPDATE=1
# Overrides that would reach past the isolated library (prompts, shortcuts,
# quizzes, walkthrough guidance), change the printed URL, or retime the agent
# states the frames show.
unset PREREVIEW_PROMPTS_DIR PREREVIEW_SHORTCUTS_DIR PREREVIEW_QUIZZES_DIR PREREVIEW_EXPLAIN_DIR \
	PREREVIEW_PUBLIC_URL PREREVIEW_AUTO_UPDATE \
	PREREVIEW_AGENT_HEARTBEAT PREREVIEW_AGENT_STALE PREREVIEW_AGENT_STALL_AFTER PREREVIEW_EDIT_SETTLE
watcher=""

# A stand-in agent: a real `prereview watch` looping on the queue, so every
# toolbar shows a connected agent (#194: with no consumer the Queue dot is
# hollow). It re-runs from the highest seq seen, as an agent does.
start_watch() { # <out>
	stop_watch
	(
		since=-1
		while :; do
			s=$("$bin" watch --out "$1" --since "$since" 2>/dev/null | grep -o '"seq":[0-9]*' | tail -1 | cut -d: -f2) || sleep 1
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

wait_ready() { # <logfile> -> echoes the READY url
	local f="$1"
	for _ in $(seq 1 40); do grep -q '^READY ' "$f" && break; sleep 0.3; done
	grep -m1 '^READY ' "$f" | awk '{print $2}'
}
