package review

import "path/filepath"

// applied.go wires the agent's "I applied this accepted suggestion" ack (#159) into the
// review UI. It is the suggestion sibling of processed.jsonl: an append-only, agent-owned
// log of accepted-suggestion ids the agent has WRITTEN to disk (via `prereview applied
// <id>`). The server reads the set every Mount and (a) marks the suggestion "applied" — a
// state distinct from "accepted (pending apply)" — and (b) drops it from the agent's
// actionable snapshot, since the agent is done with it. Kept a pure append, so it never
// races the server's writes; durable across relaunch (openStore does not reset it).

// AppliedFileName is the fixed name of the agent-written, append-only applied-marks file.
const AppliedFileName = "applied.jsonl"

// AppliedPath returns the applied-marks path for a store whose CSV lives at csvPath —
// alongside processed.jsonl / suggestions.jsonl.
func AppliedPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), AppliedFileName)
}

// loadAppliedSet reads the applied-marks file into a set of suggestion ids. Reuses the
// append-only {"id":…} marks loader (loadMarkCounts); any recorded id is applied. Nil
// when none, so a missing key indexes to false.
//
// #159 M4.2: "applied" is NET of reverts. Both files are agent-owned append-only
// tallies read by loadMarkCounts, so a suggestion is live-on-disk iff the agent has
// applied it more times than it has reverted it (appliedCount > revertedCount). This
// count arithmetic handles the whole accept→apply→revert→re-accept→re-apply cycle
// with no cancellation bookkeeping — each revert just decrements the derived flag.
func loadAppliedSet(csvPath string) map[string]bool {
	applied := loadMarkCounts(AppliedPath(csvPath))
	if len(applied) == 0 {
		return nil
	}
	reverted := loadMarkCounts(RevertedPath(csvPath))
	out := make(map[string]bool, len(applied))
	for id, n := range applied {
		if n > reverted[id] {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RevertedFileName is the agent-written, append-only revert-ack file: one {"id":…}
// line each time the agent RESTORES an applied suggestion's original text to disk
// (via `prereview reverted <id>`), the mirror of applied.jsonl. See loadAppliedSet
// for how the two counts net out to the live "applied" state.
const RevertedFileName = "reverted.jsonl"

// RevertedPath returns the revert-marks path for a store whose CSV lives at csvPath.
func RevertedPath(csvPath string) string {
	return filepath.Join(filepath.Dir(csvPath), RevertedFileName)
}
