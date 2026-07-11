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
func loadAppliedSet(csvPath string) map[string]bool {
	counts := loadMarkCounts(AppliedPath(csvPath))
	if len(counts) == 0 {
		return nil
	}
	out := make(map[string]bool, len(counts))
	for id := range counts {
		out[id] = true
	}
	return out
}
