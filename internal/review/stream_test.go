package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedTS is a deterministic timestamp for stream tests.
var fixedTS = time.Date(2026, 6, 17, 9, 30, 0, 0, time.UTC)

// decodeEvents splits JSONL output into decoded streamEvents.
func decodeEvents(t *testing.T, b []byte) []StreamEvent {
	t.Helper()
	var evs []StreamEvent
	for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev StreamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("decode event line %q: %v", line, err)
		}
		evs = append(evs, ev)
	}
	return evs
}

func TestEventStream_SeqMonotonicAndDualSink(t *testing.T) {
	var out bytes.Buffer
	file := filepath.Join(t.TempDir(), "events.jsonl")
	es := NewEventStream(&out, file)

	if err := es.EmitReady("/repo", "/repo/.prereview/comments.csv", false, false, fixedTS); err != nil {
		t.Fatalf("EmitReady: %v", err)
	}
	if err := es.EmitSnapshot(nil, nil, nil, nil, nil, false, fixedTS); err != nil {
		t.Fatalf("EmitSnapshot: %v", err)
	}
	if err := es.EmitEnd(fixedTS); err != nil {
		t.Fatalf("EmitEnd: %v", err)
	}

	// Both sinks must hold byte-identical output.
	fileBytes, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if !bytes.Equal(out.Bytes(), fileBytes) {
		t.Fatalf("stdout and events.jsonl differ:\n stdout=%q\n file=%q", out.Bytes(), fileBytes)
	}

	evs := decodeEvents(t, out.Bytes())
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d", len(evs))
	}
	wantTypes := []string{"ready", "snapshot", "end"}
	for i, ev := range evs {
		if ev.Event != wantTypes[i] {
			t.Errorf("event %d: want type %q, got %q", i, wantTypes[i], ev.Event)
		}
		if ev.Seq != i { // ready=0, snapshot=1, end=2
			t.Errorf("event %d (%s): want seq %d, got %d", i, ev.Event, i, ev.Seq)
		}
		if ev.Ts != fixedTS.Format(time.RFC3339) {
			t.Errorf("event %d (%s): want ts %q, got %q", i, ev.Event, fixedTS.Format(time.RFC3339), ev.Ts)
		}
	}
}

// TestEmitReady_SkillUpdatedField pins the drift signal: a launch that refreshed
// the installed skill sets skill_updated on the ready event (so the agent knows
// its loaded skill is stale), and the steady state omits it.
func TestEmitReady_SkillUpdatedField(t *testing.T) {
	var out bytes.Buffer
	if err := NewEventStream(&out, "").EmitReady("/r", "/r/c.csv", false, true, fixedTS); err != nil {
		t.Fatalf("EmitReady: %v", err)
	}
	if !strings.Contains(out.String(), `"skill_updated":true`) {
		t.Errorf("ready must carry skill_updated:true after a skill refresh; got %s", out.String())
	}

	out.Reset()
	if err := NewEventStream(&out, "").EmitReady("/r", "/r/c.csv", false, false, fixedTS); err != nil {
		t.Fatalf("EmitReady: %v", err)
	}
	if strings.Contains(out.String(), "skill_updated") {
		t.Errorf("ready must omit skill_updated when the skill is current; got %s", out.String())
	}
}

// TestEventStream_CommentsKeyPresence pins the contract: a snapshot ALWAYS
// carries the comments key (an empty `[]` when nothing is actionable, never
// absent), while ready/end omit it entirely.
func TestEventStream_CommentsKeyPresence(t *testing.T) {
	var out bytes.Buffer
	es := NewEventStream(&out, "")
	if err := es.EmitReady("/r", "/r/c.csv", false, false, fixedTS); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := es.EmitSnapshot(nil, nil, nil, nil, nil, false, fixedTS); err != nil { // no actionable comments
		t.Fatalf("handoff: %v", err)
	}
	if err := es.EmitEnd(fixedTS); err != nil {
		t.Fatalf("end: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if n := len(lines); n != 3 {
		t.Fatalf("want 3 lines, got %d", n)
	}
	if strings.Contains(lines[0], "comments") {
		t.Errorf("ready should omit comments: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"comments":[]`) {
		t.Errorf("empty handoff must carry comments:[] (not absent): %s", lines[1])
	}
	// Same contract for the suggestions key (issue #98).
	if !strings.Contains(lines[1], `"suggestions":[]`) {
		t.Errorf("empty handoff must carry suggestions:[] (not absent): %s", lines[1])
	}
	if strings.Contains(lines[2], "comments") || strings.Contains(lines[2], "suggestions") {
		t.Errorf("end should omit comments/suggestions: %s", lines[2])
	}
}

// TestActionableDecisions_FiltersAndMaps pins the snapshot decision payload: only
// fingerprint-matched, non-outdated decided suggestions ship, carrying the
// verdict + note joined with the suggestion's content/location.
func TestActionableDecisions_FiltersAndMaps(t *testing.T) {
	sgOK := Suggestion{ID: "ok", File: "a.md", FromLine: 3, ToLine: 3, Side: "new",
		OriginalText: "old", ProposedText: "new", AnchorStatus: anchorOK}
	// A moved suggestion IS emitted (unlike outdated) — its lines were already
	// re-anchored to follow the content, so from_line/to_line are trustworthy.
	sgMoved := Suggestion{ID: "moved", File: "a.md", FromLine: 9, ToLine: 9, Side: "new",
		OriginalText: "shifted", ProposedText: "SHIFTED", AnchorStatus: anchorMoved}
	sgOutdated := Suggestion{ID: "stale", File: "a.md", OriginalText: "gone", ProposedText: "x",
		AnchorStatus: anchorOutdated}
	sgUndecided := Suggestion{ID: "none", File: "a.md", OriginalText: "p", ProposedText: "q"}
	st := PrereviewState{
		Suggestions: []Suggestion{sgOK, sgMoved, sgOutdated, sgUndecided},
		Decisions: []SuggestionDecision{
			{SuggestionID: "ok", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(sgOK)},
			{SuggestionID: "moved", Verdict: verdictAccept, Fingerprint: suggestionFingerprint(sgMoved)},
			{SuggestionID: "stale", Verdict: verdictReject, Fingerprint: suggestionFingerprint(sgOutdated)},
			// "none" has no decision.
		},
	}
	got := actionableDecisions(st.Suggestions, st.DecisionsBySuggestion(), nil, nil)
	byID := map[string]StreamDecision{}
	for _, d := range got {
		byID[d.ID] = d
	}
	if len(got) != 2 || byID["stale"].ID != "" || byID["none"].ID != "" {
		t.Fatalf("want only the ok+moved decided suggestions, got %d: %+v", len(got), got)
	}
	if d := byID["ok"]; d.Verdict != verdictAccept || d.Original != "old" || d.Proposed != "new" || d.File != "a.md" || d.FromLine != 3 || d.AnchorStatus != anchorOK {
		t.Errorf("mapped ok decision wrong: %+v", d)
	}
	// The moved suggestion ships with its re-anchored line + moved status.
	if d := byID["moved"]; d.AnchorStatus != anchorMoved || d.FromLine != 9 {
		t.Errorf("moved suggestion should ship with re-anchored lines + moved status: %+v", d)
	}
}

func TestEventStream_AppendOnly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "events.jsonl")

	// First session writes one event.
	es1 := NewEventStream(&bytes.Buffer{}, file)
	if err := es1.EmitEnd(fixedTS); err != nil {
		t.Fatalf("first emit: %v", err)
	}
	// A second emitter pointed at the same file must append, not truncate.
	es2 := NewEventStream(&bytes.Buffer{}, file)
	if err := es2.EmitEnd(fixedTS); err != nil {
		t.Fatalf("second emit: %v", err)
	}

	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	if got := len(decodeEvents(t, b)); got != 2 {
		t.Fatalf("append-only: want 2 lines in file, got %d", got)
	}
}

func TestToStreamComment_OmitsAnchorNestsArea(t *testing.T) {
	region := Comment{
		ID:   "01REGION",
		Kind: commentKindRegion,
		URL:  "/pricing",
		Area: Area{X: 0.1, Y: 0.2, W: 0.3, H: 0.1},
		Anchor: CommentAnchor{
			Text:   "secret fingerprint that must not leak",
			Before: []string{"a"},
		},
		AnchorStatus: "",
		Created:      fixedTS,
	}
	b, err := json.Marshal(toStreamComment(region))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	if strings.Contains(js, `"anchor":`) || strings.Contains(js, "secret fingerprint") {
		t.Errorf("stream comment leaked the anchor fingerprint: %s", js)
	}
	if strings.Contains(js, "resolved") {
		t.Errorf("stream comment should not carry resolved: %s", js)
	}
	// area must be a nested object with the rectangle.
	var sc struct {
		Area *Area `json:"area"`
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sc.Area == nil || sc.Area.W != 0.3 {
		t.Errorf("area not nested as object: %s", js)
	}

	// A line comment has no rectangle → area must serialize as null.
	line := Comment{ID: "01LINE", Kind: commentKindLine, FromLine: 42, ToLine: 42, Side: "new", Created: fixedTS}
	lb, _ := json.Marshal(toStreamComment(line))
	if !strings.Contains(string(lb), `"area":null`) {
		t.Errorf("line comment area should be null, got %s", lb)
	}
}

func TestActionableComments_FiltersResolvedAndOutdated(t *testing.T) {
	comments := []Comment{
		{ID: "keep1", Kind: commentKindLine, FromLine: 1, ToLine: 1, Created: fixedTS},
		{ID: "drop-resolved", Kind: commentKindLine, FromLine: 2, ToLine: 2, Resolved: true, Created: fixedTS},
		{ID: "drop-outdated", Kind: commentKindLine, FromLine: 3, ToLine: 3, AnchorStatus: anchorOutdated, Created: fixedTS},
		{ID: "keep2", Kind: commentKindRegion, URL: "/x", Area: Area{W: 0.5, H: 0.5}, Created: fixedTS},
	}
	got := actionableComments(comments, nil)
	if len(got) != 2 {
		t.Fatalf("want 2 actionable, got %d: %+v", len(got), got)
	}
	ids := map[string]bool{}
	for _, sc := range got {
		ids[sc.ID] = true
	}
	if !ids["keep1"] || !ids["keep2"] {
		t.Errorf("wrong survivors: %+v", ids)
	}
	if ids["drop-resolved"] || ids["drop-outdated"] {
		t.Errorf("resolved/outdated leaked through: %+v", ids)
	}
}
