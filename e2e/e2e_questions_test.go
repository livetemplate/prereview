//go:build browser

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/livetemplate/prereview/internal/review"
)

// The agent's questions (`prereview ask`): a card with clickable options,
// answered by a tap and delivered to the agent in a snapshot.

// lastQuestions is the questions field of the newest snapshot in the store's
// event log: what the agent reads.
func lastQuestions(t *testing.T, p *runningPrereview) []review.StreamQuestion {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.repo, ".prereview", "events.jsonl"))
	if err != nil {
		return nil
	}
	var last []review.StreamQuestion
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var ev review.StreamEvent
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Event == "snapshot" {
			last = ev.Questions
		}
	}
	return last
}

// waitQuestions polls the event log until ok accepts the agent's view.
func waitQuestions(t *testing.T, p *runningPrereview, what string, ok func([]review.StreamQuestion) bool, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok(lastQuestions(t, p)) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	got, _ := json.Marshal(lastQuestions(t, p))
	t.Fatalf("%s: the agent's questions never matched; last: %s%s", what, got, diag())
}

// tapIn clicks sel, failing with the diagnostics.
func tapIn(t *testing.T, p *runningPrereview, sel, what string, diag func() string) {
	t.Helper()
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector(`+jsQuote(sel)+`).scrollIntoView({block: 'center'}), true`, nil),
		chromedp.Click(sel, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("%s: %v%s", what, err, diag())
	}
}

func jsQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

const oneTapSet = `{"id":"q1","file":"edited.go","from_line":3,"to_line":3,
 "steps":[{"id":"on-fail","prompt":"ASK-ONE What should happen?",
   "options":[{"label":"Retry twice","detail":"Covers brief drops."},{"label":"Sign out"},{"label":"Keep the token"}],
   "recommended":0}]}`

// TestE2E_QuestionOneTap: a one-question set renders under its line; one tap
// sends it, the agent's snapshot carries the label, Change takes it back, and
// once the agent marks it done the card offers no Change.
func TestE2E_QuestionOneTap(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)

	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector(".code `+card+`")`, "the question under its line", diag)
	if n := evalInt(t, p, `document.querySelectorAll("`+card+` .question-option.is-recommended .question-rec").length`); n != 1 {
		t.Errorf("one option should read Recommended, got %d%s", n, diag())
	}
	if n := evalInt(t, p, `[...document.querySelectorAll("`+card+` .question-option")].filter(b => b.getBoundingClientRect().height < 44).length`); n != 0 {
		t.Errorf("%d options are under 44px tall", n)
	}

	tapIn(t, p, card+` button[name='pickQuestionOption']`, "tap the first option", diag)
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+card+`"); return !!c && c.classList.contains('is-sent') && c.innerText.includes('Retry twice'); })()`, "one tap sends it", diag)
	waitQuestions(t, p, "after the tap", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].ID == "q1" && len(qs[0].Answers) == 1 && len(qs[0].Answers[0].Picked) == 1 && qs[0].Answers[0].Picked[0] == "Retry twice"
	}, diag)

	tapIn(t, p, card+` button[name='changeQuestionAnswers']`, "Change", diag)
	waitJSDiag(t, p, `!!document.querySelector("`+card+` button[name='sendQuestionAnswers']")`, "Change reopens the Review list", diag)
	waitQuestions(t, p, "after Change", func(qs []review.StreamQuestion) bool { return len(qs) == 0 }, diag)
	tapIn(t, p, card+` button[name='sendQuestionAnswers']`, "Send", diag)
	waitQuestions(t, p, "after Send", func(qs []review.StreamQuestion) bool { return len(qs) == 1 }, diag)

	mustCLI(t, p, "done", "--out", p.repo, "q1")
	// The agent's own done emits no snapshot (as for comments); the next one
	// leaves the set out (TestStream_QuestionLifecycle). The card shows it now.
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+card+`"); return !!c && c.classList.contains('is-done') && !c.querySelector("button[name='changeQuestionAnswers']"); })()`, "done leaves no Change", diag)
}

const stepsSet = `{"id":"q3","file":"edited.go","kind":"file",
 "steps":[
  {"id":"store","header":"Storage","prompt":"ASK-A Where should sessions live?","options":[{"label":"Redis"},{"label":"Postgres"}]},
  {"id":"expiry","header":"Expiry","prompt":"ASK-B How long should a session last?","options":[{"label":"30 minutes"},{"label":"8 hours"}],"recommended":1},
  {"id":"scope","header":"Scope","prompt":"ASK-C Which apps share the session?","multi":true,"options":[{"label":"Web"},{"label":"iOS"},{"label":"Admin"}]}]}`

// TestE2E_QuestionSteps: three questions on a phone. Picks move on, Back
// returns, a pick-any step toggles, picks survive a reload, nothing reaches the
// agent before Send, and Send delivers every answer.
func TestE2E_QuestionSteps(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", stepsSet)

	card := `.question-card[data-key='question-q3']`
	prompt := func() string { return evalStr(t, p, `(document.querySelector("`+card+` .question-prompt") || {}).innerText || ''`) }
	waitJSDiag(t, p, `!!document.querySelector(".file-head-cards `+card+`")`, "the file question at the file head", diag)
	pick := func(i int, what string) {
		t.Helper()
		tapIn(t, p, card+` .question-option-form:nth-of-type(`+string(rune('1'+i))+`) button[name='pickQuestionOption']`, what, diag)
	}

	pick(0, "Redis")
	waitJSDiag(t, p, `document.querySelector("`+card+` .question-prompt").innerText.includes('ASK-B')`, "step 2 after a pick", diag)
	tapIn(t, p, card+` .question-actions button[name='questionStepTo']`, "Back", diag)
	waitJSDiag(t, p, `!!document.querySelector("`+card+` .question-option.is-picked") && document.querySelector("`+card+` .question-prompt").innerText.includes('ASK-A')`, "Back shows step 1 with its pick", diag)
	pick(0, "Redis again")
	waitJSDiag(t, p, `document.querySelector("`+card+` .question-prompt").innerText.includes('ASK-B')`, "step 2", diag)
	pick(1, "8 hours")
	waitJSDiag(t, p, `document.querySelector("`+card+` .question-prompt").innerText.includes('ASK-C')`, "step 3", diag)
	pick(0, "Web")
	waitJSDiag(t, p, `document.querySelectorAll("`+card+` .question-option.is-picked").length === 1`, "Web toggled on", diag)
	pick(1, "iOS")
	waitJSDiag(t, p, `document.querySelectorAll("`+card+` .question-option.is-picked").length === 2`, "iOS toggled on", diag)
	if got := prompt(); !strings.Contains(got, "ASK-C") {
		t.Fatalf("a pick-any step must not move on by itself; prompt = %q", got)
	}
	if qs := lastQuestions(t, p); len(qs) != 0 {
		t.Fatalf("the agent got the set before Send: %+v", qs)
	}

	// The picks are on disk: a reload lands on the Review list.
	if err := chromedp.Run(p.ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	p.waitReadyAt(390, 844)
	waitJSDiag(t, p, `!!document.querySelector("`+card+` button[name='sendQuestionAnswers']") && document.querySelector("`+card+`").innerText.includes('Web, iOS')`, "the Review list after a reload", diag)
	if n := evalInt(t, p, `(() => { const r = document.querySelector("`+card+`").getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth ? 0 : 1; })()`); n != 0 {
		t.Errorf("the card does not fit a phone's width%s", diag())
	}

	tapIn(t, p, card+` button[name='sendQuestionAnswers']`, "Send", diag)
	waitQuestions(t, p, "after Send", func(qs []review.StreamQuestion) bool {
		if len(qs) != 1 || len(qs[0].Answers) != 3 {
			return false
		}
		a := qs[0].Answers
		return a[0].Picked[0] == "Redis" && a[1].Picked[0] == "8 hours" && strings.Join(a[2].Picked, ",") == "Web,iOS"
	}, diag)
}

// TestE2E_QuestionOther: "Something else" takes a typed answer, which reaches
// the agent as other.
func TestE2E_QuestionOther(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)

	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "the question", diag)
	tapIn(t, p, card+` button[name='openQuestionOther']`, "Something else", diag)
	waitJSDiag(t, p, `!!document.querySelector("`+card+` .question-other textarea")`, "the text box", diag)
	if err := chromedp.Run(p.ctx,
		chromedp.SendKeys(card+` .question-other textarea`, "Back off for a minute", chromedp.ByQuery),
		chromedp.Click(card+` button[name='answerQuestionOther']`, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("type and send: %v%s", err, diag())
	}
	waitQuestions(t, p, "after a typed answer", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].Answers[0].Other == "Back off for a minute" && len(qs[0].Answers[0].Picked) == 0
	}, diag)
	waitJSDiag(t, p, `document.querySelector("`+card+`").innerText.includes('Back off for a minute')`, "the card shows the typed answer", diag)
}
