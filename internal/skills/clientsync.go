package skills

// ClientSync is what a launch did to one agent's installed prereview
// integration (#208): refreshed to match the binary, or left alone and why. A
// target that was current or never installed has none.
type ClientSync struct {
	Client string   // the --client id
	Name   string   // the agent, as the notice names it ("Claude Code")
	Paths  []string // the files concerned
	State  string   // Refreshed, EditedThere or Failed
	Err    string   // why it failed (Failed only)
}

// Client sync states, besides EditedThere (sync.go).
const (
	Refreshed = "refreshed"
	Failed    = "failed"
)

// SyncRank orders a target's file outcomes, so its row shows the one that most
// needs you when its files fared differently.
func SyncRank(state string) int {
	switch state {
	case Refreshed:
		return 1
	case EditedThere:
		return 2
	case Failed:
		return 3
	}
	return 0
}

// Fix is the command that replaces the files with this binary's copy, for a
// sync that didn't refresh them; "" when it did.
func (s ClientSync) Fix() string {
	if s.State == Refreshed {
		return ""
	}
	return "prereview --install-skill --client=" + s.Client
}

// Status is the row's state as the notice shows it.
func (s ClientSync) Status() string {
	if s.State == Failed && s.Err != "" {
		return Failed + ": " + s.Err
	}
	return s.State
}

// AnyRefreshed reports whether a sync rewrote client's files ("" = any client).
func AnyRefreshed(rs []ClientSync, client string) bool {
	for _, r := range rs {
		if r.State == Refreshed && (client == "" || r.Client == client) {
			return true
		}
	}
	return false
}
