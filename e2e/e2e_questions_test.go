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
	// Change sits on the answer's line, not under it (`.code form` is block).
	if n := evalInt(t, p, `(() => { const b = document.querySelector("`+card+` .question-answer b").getBoundingClientRect(), c = document.querySelector("`+card+` button[name='changeQuestionAnswers']").getBoundingClientRect(); return Math.abs(b.top - c.top) < 8 ? 0 : 1; })()`); n != 0 {
		t.Errorf("Change is not on the answer's line%s", diag())
	}
	waitQuestions(t, p, "after the tap", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].ID == "q1" && len(qs[0].Answers) == 1 && len(qs[0].Answers[0].Picked) == 1 && qs[0].Answers[0].Picked[0] == "Retry twice"
	}, diag)

	tapIn(t, p, card+` button[name='changeQuestionAnswers']`, "Change", diag)
	waitJSDiag(t, p, `!!document.querySelector("`+card+` button[name='pickQuestionOption']")`, "Change reopens the question", diag)
	waitQuestions(t, p, "after Change", func(qs []review.StreamQuestion) bool { return len(qs) == 0 }, diag)
	tapIn(t, p, card+` button[name='pickQuestionOption']`, "pick again", diag)
	waitQuestions(t, p, "after picking again", func(qs []review.StreamQuestion) bool { return len(qs) == 1 }, diag)

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

// TestE2E_QuestionSteps: three questions on a phone, as tabs. A pick moves to
// the next unanswered question, a tab shows any question, a pick-any question
// toggles, picks survive a reload, Submit waits for every answer, and nothing
// reaches the agent before it.
func TestE2E_QuestionSteps(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", stepsSet)

	card := `.question-card[data-key='question-q3']`
	showing := func(marker string) string {
		return `document.querySelector("` + card + ` .question-prompt").innerText.includes('` + marker + `')`
	}
	submitOff := `document.querySelector("` + card + ` button[name='sendQuestionAnswers']").disabled`
	waitJSDiag(t, p, `!!document.querySelector(".file-head-cards `+card+`")`, "the file question at the file head", diag)
	pick := func(i int, what string) {
		t.Helper()
		tapIn(t, p, card+` .question-option-form:nth-of-type(`+string(rune('1'+i))+`) button[name='pickQuestionOption']`, what, diag)
	}
	tab := func(i int, what string) {
		t.Helper()
		tapIn(t, p, card+` .question-steps form:nth-of-type(`+string(rune('1'+i))+`) button`, what, diag)
	}

	if evalInt(t, p, `document.querySelectorAll("`+card+` .question-steps [role=tab]").length`) != 3 || evalStr(t, p, `String(`+submitOff+`)`) != "true" {
		t.Fatalf("want three tabs and Submit off before any answer%s", diag())
	}
	pick(0, "Redis")
	waitJSDiag(t, p, showing("ASK-B"), "a pick moves to the next question", diag)
	tab(0, "the Storage tab")
	waitJSDiag(t, p, showing("ASK-A")+` && !!document.querySelector("`+card+` .question-option.is-picked")`, "the tab shows question 1 with its pick", diag)
	tab(2, "the Scope tab")
	waitJSDiag(t, p, showing("ASK-C"), "the Scope tab", diag)
	pick(0, "Web")
	waitJSDiag(t, p, `document.querySelectorAll("`+card+` .question-option.is-picked").length === 1`, "Web toggled on", diag)
	pick(1, "iOS")
	waitJSDiag(t, p, `document.querySelectorAll("`+card+` .question-option.is-picked").length === 2 && `+showing("ASK-C"), "iOS toggled on, still on Scope", diag)
	if evalStr(t, p, `String(`+submitOff+`)`) != "true" {
		t.Errorf("Submit is on with Expiry unanswered%s", diag())
	}
	tab(1, "the Expiry tab")
	waitJSDiag(t, p, showing("ASK-B"), "the Expiry tab", diag)
	pick(1, "8 hours")
	waitJSDiag(t, p, `!`+submitOff, "Submit on once all three are answered", diag)
	if qs := lastQuestions(t, p); len(qs) != 0 {
		t.Fatalf("the agent got the set before Submit: %+v", qs)
	}

	// The picks are on disk: after a reload every tab reads answered.
	if err := chromedp.Run(p.ctx, chromedp.Reload()); err != nil {
		t.Fatal(err)
	}
	p.waitReadyAt(390, 844)
	waitJSDiag(t, p, `document.querySelectorAll("`+card+` .question-step.is-answered").length === 3 && !`+submitOff, "every answer survives a reload", diag)
	// An answered tab's label must be readable: a class check passed once while
	// Pico's button --pico-color painted it white on a pale chip. (The current
	// tab is the accent colour, as links are.)
	if n := evalInt(t, p, `(() => {
		const lum = c => { const v = c.match(/[\d.]+/g).slice(0, 3).map(x => { x /= 255; return x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4; }); return 0.2126 * v[0] + 0.7152 * v[1] + 0.0722 * v[2]; };
		const tab = document.querySelector("`+card+` .question-step.is-answered:not(.is-current)"), cs = getComputedStyle(tab);
		const a = lum(cs.color), b = lum(cs.backgroundColor);
		return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05) >= 4.5 ? 0 : 1; })()`); n != 0 {
		t.Errorf("an answered tab's label is unreadable (contrast under 4.5)%s", diag())
	}
	if n := evalInt(t, p, `(() => { const r = document.querySelector("`+card+`").getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth ? 0 : 1; })()`); n != 0 {
		t.Errorf("the card does not fit a phone's width%s", diag())
	}

	tapIn(t, p, card+` button[name='sendQuestionAnswers']`, "Submit", diag)
	waitQuestions(t, p, "after Submit", func(qs []review.StreamQuestion) bool {
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

// TestE2E_QuestionQueued: a submitted set is queue work like a comment. It
// reads queued on its card and in the Queue, the row jumps to the card, the
// agent's done turns both to done, and while the queue is paused a submitted
// set waits for Resume.
func TestE2E_QuestionQueued(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)
	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "the question", diag)
	if n := evalInt(t, p, `document.querySelectorAll("`+card+` .queue-badge").length`); n != 0 {
		t.Fatalf("an unanswered set has a queue badge%s", diag())
	}

	// Paused: the submit is held, like a comment queued while paused.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.activeElement&&document.activeElement.blur&&document.activeElement.blur()`, nil),
		chromedp.KeyEvent("q"),
	); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `!!document.querySelector('.queue-dropdown.is-paused')`, "paused", diag)
	tapIn(t, p, card+` button[name='pickQuestionOption']`, "tap an option", diag)
	waitJSDiag(t, p, `(document.querySelector("`+card+` .queue-badge") || {}).textContent === 'queued'`, "the card reads queued", diag)
	time.Sleep(800 * time.Millisecond) // past the emit debounce
	if qs := lastQuestions(t, p); len(qs) != 0 {
		t.Fatalf("a paused queue handed the agent the set: %+v", qs)
	}
	if err := chromedp.Run(p.ctx, chromedp.KeyEvent("q")); err != nil {
		t.Fatal(err)
	}
	waitQuestions(t, p, "after Resume", func(qs []review.StreamQuestion) bool { return len(qs) == 1 && qs[0].ID == "q1" }, diag)

	// The Queue lists it, and its row jumps to the card.
	tapIn(t, p, `.queue-dropdown .queue-trigger`, "open the Queue", diag)
	row := `.queue-row.queue-kind-question`
	waitJSDiag(t, p, `(() => { const r = document.querySelector("`+row+`"); return !!r && r.classList.contains('queue-queued') && r.innerText.includes('ASK-ONE'); })()`, "a queued question row", diag)
	tapIn(t, p, row+` button[name='jumpToQuestion']`, "jump to the question", diag)
	waitJSDiag(t, p, `(() => { const r = document.querySelector("`+card+`").getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0; })()`, "the jump shows the card", diag)

	mustCLI(t, p, "done", "--out", p.repo, "q1")
	waitJSDiag(t, p, `(document.querySelector("`+card+` .queue-badge") || {}).textContent === 'done'`, "done on the card", diag)
}

// TestE2E_QuestionResolveHide: a question card has a comment's Resolve and
// Reopen and a suggestion's Hide. Resolving an unanswered set tells the agent
// (resolved, no answers) and hides the card unless "Show resolved" is on;
// Hide leaves a marker whose Show brings the card back.
func TestE2E_QuestionResolveHide(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)
	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "the question", diag)

	// Hide → marker → Show.
	tapIn(t, p, card+` button[name='hideQuestion']`, "Hide", diag)
	waitJSDiag(t, p, `!document.querySelector("`+card+`") && !!document.querySelector(".question-hidden-marker button[name='showQuestion']")`, "Hide leaves a marker", diag)
	if qs := lastQuestions(t, p); len(qs) != 0 {
		t.Fatalf("hiding told the agent: %+v", qs)
	}
	tapIn(t, p, `.question-hidden-marker button[name='showQuestion']`, "Show", diag)
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "Show brings the card back", diag)

	// Resolve: the agent is told; the card goes, unless Show resolved is on.
	tapIn(t, p, card+` button[name='toggleQuestionResolved']`, "Resolve", diag)
	waitQuestions(t, p, "after Resolve", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].ID == "q1" && qs[0].Resolved && len(qs[0].Answers) == 0
	}, diag)
	waitJSDiag(t, p, `!document.querySelector("`+card+`")`, "a resolved card is hidden like a resolved comment", diag)
	p.openViewItem("toggleShowResolved")
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+card+`"); return !!c && c.classList.contains('is-resolved') && c.querySelector("button[name='toggleQuestionResolved']").textContent === 'Reopen'; })()`, "Show resolved shows it, with Reopen", diag)
	tapIn(t, p, card+` button[name='toggleQuestionResolved']`, "Reopen", diag)
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+card+`"); return !!c && !c.classList.contains('is-resolved') && !!c.querySelector("button[name='pickQuestionOption']"); })()`, "Reopen brings the options back", diag)
	waitQuestions(t, p, "after Reopen", func(qs []review.StreamQuestion) bool { return len(qs) == 0 }, diag)
}

// TestE2E_QuestionInThread: a set asked `for` a comment renders inside that
// comment's card, after the thread, as the agent's reply, and nowhere else.
// Answering it there reaches the agent with the comment's id.
func TestE2E_QuestionInThread(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	addLineComment(t, p, 3, 3, "make the limit configurable")
	cid := evalStr(t, p, `document.querySelector('.inline-comment').dataset.key`)
	runAgentVerb(t, p, "ask", `{"id":"q-cfg","for":"`+cid+`","file":"edited.go","from_line":3,"to_line":3,
	 "steps":[{"id":"where","prompt":"ASK-CFG Configurable from where?",
	   "options":[{"label":"An environment variable"},{"label":"The config file"}],"recommended":0}]}`)

	inThread := `.inline-comment[data-key='` + cid + `'] .question-card[data-key='question-q-cfg']`
	waitJSDiag(t, p, `!!document.querySelector("`+inThread+`")`, "the question inside the comment's card", diag)
	if n := evalInt(t, p, `document.querySelectorAll(".question-card[data-key='question-q-cfg']").length`); n != 1 {
		t.Fatalf("the question renders %d times, want once (in the thread)%s", n, diag())
	}
	// In the thread the comment's own Resolve, Reply and Hide stand in.
	if n := evalInt(t, p, `document.querySelectorAll("`+inThread+` button[name='toggleQuestionResolved'], `+inThread+` button[name='hideQuestion'], `+inThread+` button[name='openReply']").length`); n != 0 {
		t.Errorf("the in-thread card repeats the comment's actions (%d buttons)%s", n, diag())
	}
	tapIn(t, p, inThread+` button[name='pickQuestionOption']`, "pick in the thread", diag)
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+inThread+`"); return !!c && c.innerText.includes('An environment variable'); })()`, "the answer folds in the thread", diag)
	waitQuestions(t, p, "after the pick", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].For == cid && qs[0].Answers[0].Picked[0] == "An environment variable"
	}, diag)

	// Resolving the comment resolves its question: the agent is told.
	tapIn(t, p, `.inline-comment[data-key='`+cid+`'] > .ic-actions button[name='toggleResolved']`, "resolve the comment", diag)
	waitQuestions(t, p, "after resolving the comment", func(qs []review.StreamQuestion) bool {
		return len(qs) == 1 && qs[0].ID == "q-cfg" && qs[0].Resolved
	}, diag)
}

// TestE2E_QuestionNav: an unanswered question is an open item in the ‹ › bar,
// like an undecided suggestion; stepping lands on its card, and once it is
// submitted it no longer counts.
func TestE2E_QuestionNav(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)
	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "the question", diag)
	waitJSDiag(t, p, `/\b1 open\b/.test((document.querySelector('.ann-nav-label') || {}).textContent || '')`, "the bar counts the question as open", diag)
	tapIn(t, p, `button[name='navNext']`, "step", diag)
	waitJSDiag(t, p, `(() => { const r = document.querySelector("`+card+`").getBoundingClientRect(); return r.top < innerHeight && r.bottom > 0; })()`, "the step lands on the card", diag)
	tapIn(t, p, card+` button[name='pickQuestionOption']`, "answer it", diag)
	waitJSDiag(t, p, `!document.querySelector('.ann-nav-label') || !/\b1 open\b/.test(document.querySelector('.ann-nav-label').textContent)`, "a submitted question leaves the count", diag)
}

// TestE2E_QuestionFold: a question is a row annotation like a comment. On an
// unchanged line far from the change it is pinned out of the diff's folds,
// counts in its row's badge, and the badge collapses and reopens it.
func TestE2E_QuestionFold(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupFixtureRepoBigDiff(t), 1200, 800, "--agent") // big.go: line 20 changed of 40
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("big.go")
	runAgentVerb(t, p, "ask", `{"id":"qf","file":"big.go","from_line":35,"to_line":35,
	 "steps":[{"id":"s","prompt":"ASK-FAR Keep this line?","options":[{"label":"Yes"},{"label":"No"}]}]}`)
	card := `.code .question-card[data-key='question-qf']`
	waitJSDiag(t, p, `(() => { const c = document.querySelector("`+card+`"); return !!c && c.offsetParent !== null; })()`, "the question on folded line 35 has a visible card", diag)
	if n := countSel(t, p, `.code .fold-row`); n < 1 {
		t.Errorf("the rest of the file must still fold; got %d fold rows%s", n, diag())
	}
	badge := `document.querySelector("` + card + `").closest('.line-row').querySelector('.line-marks')`
	if got := evalStr(t, p, `((`+badge+` || {}).textContent || '').trim()`); got != "1" {
		t.Errorf("the row badge reads %q, want 1%s", got, diag())
	}
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(badge+`.click(), true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `document.querySelector("`+card+`").offsetParent === null`, "the badge collapses the card", diag)
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelector("`+card+`").closest('.line-row').querySelector('.line-marks').click(), true`, nil)); err != nil {
		t.Fatal(err)
	}
	waitJSDiag(t, p, `document.querySelector("`+card+`").offsetParent !== null`, "and opens it again", diag)
}

// TestE2E_QuestionPlacements: a question shows with its block's comments in the
// rendered Markdown view, and in the HTML preview's card behind its block's
// marker.
func TestE2E_QuestionPlacements(t *testing.T) {
	t.Run("markdown", func(t *testing.T) {
		p := bootChromeAgainstRepo(t, setupFixtureRepoMarkdown(t), 1200, 800, "--agent") // docs.md: prose line 4 is its own block
		p.waitReady()
		diag := captureDiag(t, p)
		p.clickFile("docs.md")
		runAgentVerb(t, p, "ask", `{"id":"qm","file":"docs.md","from_line":4,"to_line":4,
		 "steps":[{"id":"s","prompt":"ASK-MD Keep the edit?","options":[{"label":"Yes"},{"label":"No"}]}]}`)
		waitJSDiag(t, p, `(() => { const c = document.querySelector(".md-view .question-card[data-key='question-qm']"); return !!c && c.offsetParent !== null; })()`, "the question under its rendered block", diag)
	})
	t.Run("html", func(t *testing.T) {
		p, diag := bootHTMLPreview(t, setupFixtureHTMLRegionRepo(t), 1200, 800) // region.html: line 4 is the top section
		runAgentVerb(t, p, "ask", `{"id":"qh","file":"region.html","from_line":4,"to_line":4,
		 "steps":[{"id":"s","prompt":"ASK-HTML Keep the top section?","options":[{"label":"Yes"},{"label":"No"}]}]}`)
		marker := `.preview-pins .area-overlay[data-id="MB-4-4"] .pin-btn`
		waitJSDiag(t, p, `(() => { const b = document.querySelector('`+marker+`'); return !!b && b.textContent.trim() === '1'; })()`, "a marker counting the question", diag)
		tapCentred(t, p, marker, "tap the marker", diag)
		waitJSDiag(t, p, `!!document.querySelector(".preview-card .question-card[data-key='question-qh'] button[name='pickQuestionOption']")`, "the question in the preview's card", diag)
	})
}

// TestE2E_QuestionWaiting: an unanswered question is the reviewer's work: the
// Queue panel says it is waiting and End session warns, until it is answered.
func TestE2E_QuestionWaiting(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	diag := captureDiag(t, p)
	p.clickFile("edited.go")
	runAgentVerb(t, p, "ask", oneTapSet)
	card := `.question-card[data-key='question-q1']`
	waitJSDiag(t, p, `!!document.querySelector("`+card+`")`, "the question", diag)
	waitJSDiag(t, p, `((document.querySelector('.queue-awaiting-answer') || {}).textContent || '').includes('1 question waiting for your answer')`, "the Queue's waiting line", diag)
	waitJSDiag(t, p, `!!document.querySelector('#confirm-end-session .end-warn-answer')`, "End session warns", diag)
	tapIn(t, p, card+` button[name='pickQuestionOption']`, "answer it", diag)
	waitJSDiag(t, p, `!document.querySelector('.queue-awaiting-answer') && !document.querySelector('#confirm-end-session .end-warn-answer')`, "answered: no line, no warning", diag)
}
