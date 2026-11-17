package review

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
)

// rippleState: comment c1 with three follow-ons, r2/r3 alternatives for one text.
func rippleState() PrereviewState {
	return PrereviewState{SelectedFile: "a.md", Suggestions: []Suggestion{
		{ID: "r1", For: "c1", File: "a.md", FromLine: 2, ToLine: 2, Side: "new", OriginalText: "two"},
		{ID: "r2", For: "c1", File: "a.md", FromLine: 3, ToLine: 3, Side: "new", OriginalText: "three", ProposedText: "3a"},
		{ID: "r3", For: "c1", File: "a.md", FromLine: 3, ToLine: 3, Side: "new", OriginalText: "three", ProposedText: "3b"},
		{ID: "x", File: "a.md", FromLine: 9, ToLine: 9, Side: "new", OriginalText: "nine"},
	}}
}

func rippleCtx(action, comment string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), action, map[string]interface{}{"value": comment})
}

func TestRippleByComment(t *testing.T) {
	g := rippleState().RippleByComment()["c1"]
	if g == nil || g.Total != 3 || g.Open != 3 || g.Acceptable != 1 || g.NeedPick != 1 {
		t.Fatalf("group = %+v, want 3 total, 3 open, 1 acceptable, 1 set to pick", g)
	}
	if len(rippleState().RippleByComment()) != 1 {
		t.Error("a suggestion without for must not form a group")
	}
}

// Accept all takes what's unambiguous and leaves the alternatives; Reject all
// refuses every undecided member; unrelated suggestions are untouched.
func TestAcceptRejectRipple(t *testing.T) {
	c := decisionController(t)
	st, _ := c.AcceptRipple(rippleState(), rippleCtx("acceptRipple", "c1"))
	d := st.DecisionsBySuggestion()
	if d["r1"].Verdict != verdictAccept || d["r2"].Verdict != "" || d["r3"].Verdict != "" || d["x"].Verdict != "" {
		t.Fatalf("after Accept all: %+v", d)
	}
	if g := st.RippleByComment()["c1"]; g.Open != 2 || g.Acceptable != 0 || g.NeedPick != 1 {
		t.Errorf("group after Accept all = %+v", g)
	}
	st, _ = c.RejectRipple(st, rippleCtx("rejectRipple", "c1"))
	d = st.DecisionsBySuggestion()
	if d["r1"].Verdict != verdictAccept || d["r2"].Verdict != verdictReject || d["r3"].Verdict != verdictReject || d["x"].Verdict != "" {
		t.Errorf("after Reject all: %+v", d)
	}
}

// For rides a revision (same id re-appended) and reaches the agent's snapshot.
func TestRippleFor_SurvivesRevisionAndReachesTheAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), SuggestionFileName)
	lines := `{"id":"r1","for":"c1","file":"a.md","from_line":2,"to_line":2,"original":"two","proposed":""}
{"id":"r1","for":"c1","file":"a.md","from_line":2,"to_line":2,"original":"two","proposed":"2"}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	sgs := loadSuggestions(path)
	if len(sgs) != 1 || sgs[0].For != "c1" || sgs[0].ProposedText != "2" {
		t.Fatalf("loaded %+v", sgs)
	}
	decided := map[string]SuggestionDecision{"r1": newDecision(sgs[0], verdictAccept, "", false)}
	out := actionableDecisions(sgs, decided, nil, nil)
	if len(out) != 1 || out[0].For != "c1" {
		t.Errorf("snapshot decision = %+v, want for=c1", out)
	}
}
