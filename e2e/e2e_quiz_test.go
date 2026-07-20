//go:build browser

// End-to-end coverage for issue #191: the comprehension quiz. The coding agent
// calls `prereview quiz` with a generated quiz; the running review server (agent
// mode) watches .prereview/quiz.jsonl and pushes it to every open tab — no reload
// needed. The reviewer answers, sees the explanation, and can jump to the code a
// question cites.
//
// The load-bearing assertion is the GROUNDING distinction: a question citing a
// line that does not exist in the diff renders as a warning and offers NO jump,
// while a `decision` question that is deliberately anchorless renders as a
// neutral marker. Those two must never look alike — collapsing them would let a
// hallucinated anchor hide behind a legitimate label.
//
// Run with: go test -tags=browser -run TestE2E_Quiz ./e2e/...

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// quizJSON is a quiz over edited.go covering every question state the view can
// render: two grounded questions, one ungrounded (line 900 is not in the diff),
// and one anchorless decision.
const quizJSON = `{
  "id": "z1",
  "file": "edited.go",
  "questions": [
    {"id":"q1","probe":"consequence","prompt":"QUESTION-ONE what breaks?",
     "options":["nothing at all","the atomic write"],"answer":1,
     "why":"EXPLAIN-ONE it is a write-back buffer","from_line":3,"to_line":3,"side":"new"},
    {"id":"q2","probe":"localization","prompt":"QUESTION-TWO where does it live?",
     "options":["the top","the bottom"],"answer":0,
     "why":"EXPLAIN-TWO near the imports","from_line":1,"to_line":1,"side":"new"},
    {"id":"q3","probe":"rationale","prompt":"QUESTION-THREE why this way?",
     "options":["speed","clarity"],"answer":1,
     "why":"EXPLAIN-THREE it reads better","from_line":900,"to_line":900,"side":"new"},
    {"id":"q4","probe":"decision","prompt":"QUESTION-FOUR what did you decide unasked?",
     "options":["added a dependency","skipped the error-path test"],"answer":1,
     "why":"EXPLAIN-FOUR the request never mentioned tests","from_line":0}
  ]
}`

// submitQuiz writes the quiz through the real binary, out of process, exactly as
// the agent does in production.
func submitQuiz(t *testing.T, p *runningPrereview, payload string) {
	t.Helper()
	cmd := exec.Command(p.binary, "quiz", "--out", p.repo, "--file", "-")
	cmd.Stdin = strings.NewReader(payload)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("prereview quiz: %v\n%s", err, out)
	}
}

// evalInt/evalStr read a value out of the page, failing the test with the server
// log attached (the repo convention — a bare chromedp error is rarely enough).
func evalInt(t *testing.T, p *runningPrereview, js string) int {
	t.Helper()
	var n int
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &n)); err != nil {
		t.Fatalf("eval %s: %v\nstderr: %s", js, err, p.stderr.String())
	}
	return n
}

func evalStr(t *testing.T, p *runningPrereview, js string) string {
	t.Helper()
	var s string
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(js, &s)); err != nil {
		t.Fatalf("eval %s: %v\nstderr: %s", js, err, p.stderr.String())
	}
	return s
}

// waitForQuizEntry polls for the quiz menu entry to exist in the DOM.
//
// It deliberately does NOT use WaitVisible: the entry lives inside the "View ▾"
// dropdown panel, which is closed (and therefore not visible) until clicked, so
// WaitVisible would hang forever on an element that is present and working.
func waitForQuizEntry(t *testing.T, p *runningPrereview) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if evalInt(t, p, `document.querySelectorAll("button[name='toggleQuiz']").length`) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("quiz entry never appeared after `prereview quiz` (watcher fan-out)\nstderr: %s", p.stderr.String())
}

// openQuiz opens the View dropdown and clicks the quiz entry.
func openQuiz(t *testing.T, p *runningPrereview) {
	t.Helper()
	p.openViewItem("toggleQuiz")
	if err := chromedp.Run(p.ctx, chromedp.WaitVisible(`.quiz .quiz-list`, chromedp.ByQuery)); err != nil {
		t.Fatalf("quiz view never rendered: %v\nstderr: %s", err, p.stderr.String())
	}
}

func TestE2E_QuizAppearsLiveAndAnswers(t *testing.T) {
	// --agent so the server runs WatchLLMStatus — the live push path under test.
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")

	// Before the agent submits anything there is no quiz, so no menu entry.
	if n := evalInt(t, p, `document.querySelectorAll("button[name='toggleQuiz']").length`); n != 0 {
		t.Fatalf("the quiz entry must not appear before a quiz exists, found %d", n)
	}

	submitQuiz(t, p, quizJSON)

	// The entry appears LIVE via the watcher fan-out — no reload.
	waitForQuizEntry(t, p)
	openQuiz(t, p)

	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-q').length`); n != 4 {
		t.Fatalf("want 4 questions rendered, got %d", n)
	}

	// GROUNDING — the anti-slop check, and the reason this whole feature is more
	// than a toy. q3 cites line 900, which is not in the diff.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-ungrounded').length`); n != 1 {
		t.Fatalf("the question citing a line outside the diff must be flagged ungrounded; got %d flags", n)
	}
	// ...and an ungrounded question must offer NO jump: the link would land on
	// code that has nothing to do with the question.
	ungroundedJumps := evalInt(t, p,
		`[...document.querySelectorAll('.quiz .quiz-q')].filter(q=>q.querySelector('.quiz-ungrounded')&&q.querySelector("button[name='jumpToQuizLine']")).length`)
	if ungroundedJumps != 0 {
		t.Fatalf("an ungrounded question must not offer a jump link, %d did", ungroundedJumps)
	}
	// The anchorless `decision` is a DIFFERENT state: legitimate, not a warning.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-absent').length`); n != 1 {
		t.Fatalf("the anchorless decision question must render its own marker; got %d", n)
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-jump').length`); n != 2 {
		t.Fatalf("the 2 grounded questions must each offer a jump; got %d", n)
	}

	// The cited code is shown INLINE. Without it the quiz replaces the diff and the
	// reviewer has to leave, read, and come back — reported from real use as
	// "I need to switch to the code and read it once more, then come back".
	//
	// Exactly the two GROUNDED questions get an excerpt: the ungrounded one has no
	// resolvable lines to show, and the anchorless decision is about something that
	// is not in the diff at all.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-code').length`); n != 2 {
		t.Fatalf("each grounded question must show its cited code inline; expected 2, got %d", n)
	}
	// The cited lines must be distinguishable from the context lines around them,
	// or the excerpt shows code without showing which part is being asked about.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-code-line.is-cited').length`); n < 2 {
		t.Errorf("cited lines must be marked apart from context lines, got %d", n)
	}
	// The excerpt shows the real line content, not a placeholder.
	if txt := evalStr(t, p, `document.querySelector('.quiz .quiz-code').innerText`); !strings.Contains(txt, "func") && !strings.Contains(txt, "package") {
		t.Errorf("the excerpt must contain the actual source line, got %q", txt)
	}

	// The explanation is hidden until you answer — otherwise there is no retrieval
	// practice, just reading.
	if body := evalStr(t, p, `document.querySelector('.quiz').textContent`); strings.Contains(body, "EXPLAIN-ONE") {
		t.Fatal("the explanation must stay hidden until the question is answered")
	}

	// Answer q1 correctly (option index 1).
	answer := func(questionID string, choice int) {
		sel := fmt.Sprintf(`.quiz-q[data-key='%s'] .quiz-options li:nth-child(%d) button[name='answerQuestion']`, questionID, choice+1)
		if err := chromedp.Run(p.ctx,
			chromedp.Click(sel, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		); err != nil {
			t.Fatalf("answer %s: %v\nstderr: %s", questionID, err, p.stderr.String())
		}
	}
	answer("q1", 1)

	body := evalStr(t, p, `document.querySelector('.quiz').textContent`)
	if !strings.Contains(body, "EXPLAIN-ONE") {
		t.Fatalf("answering must reveal the explanation; quiz text was:\n%s", body)
	}
	if !strings.Contains(body, "1/4") {
		t.Fatalf("the running score must show 1/4 after one correct answer; quiz text was:\n%s", body)
	}
	// Only the answered question reveals — the others stay unanswered.
	if strings.Contains(body, "EXPLAIN-TWO") {
		t.Fatal("answering one question must not reveal the others' explanations")
	}

	// Answer q2 WRONG (option 1; the correct answer is 0) — the score must not move.
	answer("q2", 1)
	body = evalStr(t, p, `document.querySelector('.quiz').textContent`)
	if !strings.Contains(body, "1/4") {
		t.Fatalf("a wrong answer must not raise the score; quiz text was:\n%s", body)
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-option.is-wrong').length`); n != 1 {
		t.Fatalf("the wrong choice must be marked; got %d", n)
	}

	// Durable: answers live in the server-owned file and are re-derived on Mount.
	if err := chromedp.Run(p.ctx,
		chromedp.Navigate(p.url),
		chromedp.WaitVisible(`#files-drawer button.file-btn`, chromedp.ByQuery),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		t.Fatalf("reload: %v\nstderr: %s", err, p.stderr.String())
	}
	p.clickFile("edited.go")
	openQuiz(t, p)
	body = evalStr(t, p, `document.querySelector('.quiz').textContent`)
	if !strings.Contains(body, "1/4") || !strings.Contains(body, "EXPLAIN-ONE") {
		t.Fatalf("answers must survive a reload (they are on disk); quiz text was:\n%s", body)
	}
}

// Jumping to the code a question cites leaves the quiz and parks the line cursor
// on that line — the same mechanism the search palette uses to land on a hit.
func TestE2E_QuizJumpToCitedLine(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)

	waitForQuizEntry(t, p)
	openQuiz(t, p)
	if err := chromedp.Run(p.ctx,
		// The first grounded question cites line 3.
		chromedp.Click(`.quiz-q[data-key='q1'] button[name='jumpToQuizLine']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.code .line`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("jump to cited line: %v\nstderr: %s", err, p.stderr.String())
	}

	// We left the quiz for the diff...
	if n := evalInt(t, p, `document.querySelectorAll('.quiz .quiz-list').length`); n != 0 {
		t.Fatal("jumping to the code must leave the quiz view")
	}
	// ...and the cursor is parked on the cited line, so the reviewer sees exactly
	// what the question was about rather than the top of the file.
	if n := evalInt(t, p, `document.querySelectorAll('.line.is-cursor').length`); n != 1 {
		t.Fatalf("the cited line must be the line cursor; found %d cursor rows", n)
	}
}

// Retaking clears the answers so the quiz can be run again — retrieval practice
// works by repetition, and a one-shot quiz is a test, not a study tool.
func TestE2E_QuizRetakeClearsAnswers(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)

	waitForQuizEntry(t, p)
	openQuiz(t, p)
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.quiz-q[data-key='q1'] .quiz-options li:nth-child(2) button[name='answerQuestion']`, chromedp.ByQuery),
		chromedp.WaitVisible(`button[name='retakeQuiz']`, chromedp.ByQuery),
		chromedp.Click(`button[name='retakeQuiz']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("retake: %v\nstderr: %s", err, p.stderr.String())
	}

	body := evalStr(t, p, `document.querySelector('.quiz').textContent`)
	if strings.Contains(body, "EXPLAIN-ONE") {
		t.Fatalf("a retake must hide the explanations again; quiz text was:\n%s", body)
	}
	if n := evalInt(t, p, `document.querySelectorAll(".quiz button[name='answerQuestion']").length`); n != 8 {
		t.Fatalf("after a retake every option must be answerable again (4 questions x 2 options); got %d", n)
	}
}

// The request half of the loop: tapping "Quiz me" in the file header saves a
// file-level comment carrying the quiz prompt, which reaches the agent through
// the ordinary comment queue. No new comment kind, no second channel.
//
// The request is deliberately VISIBLE in the reviewer's own queue — Comment.Hidden
// only applies to resolved comments, so there is no way to hide it and no reason
// to invent one. A saved Prompt comment already behaves this way.
func TestE2E_QuizMeRequestsAQuiz(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")

	// REGRESSION GUARD. "Quiz me" and "Ask for suggestions" sit side by side in the
	// file header. They looked alike, so the quiz control first reused the
	// `.prompts-trigger` class — which made that selector ambiguous and silently
	// re-pointed TestE2E_PromptPicker's click at the wrong button. It did not fail:
	// it HUNG, waiting forever for a dropdown that never opened, and took the whole
	// suite past its timeout. Assert the selector still resolves to exactly one
	// element so the next person gets an instant, explained failure instead.
	if n := evalInt(t, p, `document.querySelectorAll('.prompts-trigger').length`); n != 1 {
		t.Fatalf(".prompts-trigger must match exactly the suggestions trigger, got %d —\n"+
			"a second element sharing that class hangs TestE2E_PromptPicker rather than failing it", n)
	}

	if err := chromedp.Run(p.ctx,
		chromedp.WaitVisible(`button[name='requestQuiz']`, chromedp.ByQuery),
		chromedp.Click(`button[name='requestQuiz']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("tap Quiz me: %v\nstderr: %s", err, p.stderr.String())
	}

	// One file-level comment, whose body is the quiz prompt the agent will act on.
	rows := p.readCSV()
	if len(rows) != 2 { // header + 1
		t.Fatalf("Quiz me must save exactly one request comment, got %d row(s): %v", len(rows), rows)
	}
	const fileCol, bodyCol, kindCol = 1, 5, 10
	if got := rows[1][fileCol]; got != "edited.go" {
		t.Errorf("the request must anchor to the selected file, got %q", got)
	}
	if got := rows[1][kindCol]; got != "file" {
		t.Errorf("the request must be a file-level comment (kind=file), got %q", got)
	}
	body := rows[1][bodyCol]
	// The body IS the contract the agent follows, so the two instructions that keep
	// it from doing the wrong thing must survive into the queue.
	if !strings.Contains(body, "prereview quiz") {
		t.Errorf("the request body must name the verb to answer with; got:\n%s", body)
	}
	if !strings.Contains(body, "prereview suggest") {
		t.Errorf("the request body must say NOT to use `prereview suggest` — otherwise the\n"+
			"agent treats a quiz request like a prompt and proposes edits; got:\n%s", body)
	}
}

// Reported from a real phone: tapping "Quiz me" changed nothing visible, so the
// reviewer tapped again and queued a DUPLICATE request. Saving the comment is
// silent by nature — on a narrow viewport the queue is behind a menu — so the
// absence of feedback read as "it didn't work".
//
// Two things must now be true: the tap is confirmed, and a second tap cannot
// queue a duplicate while the first is unanswered.
func TestE2E_QuizMeConfirmsAndBlocksDoubleTap(t *testing.T) {
	// A phone-sized viewport, because that is where this was found.
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")

	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='requestQuiz']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("tap Quiz me: %v\nstderr: %s", err, p.stderr.String())
	}

	// 1. The tap is acknowledged.
	if n := evalInt(t, p, `document.querySelectorAll('.toast').length`); n == 0 {
		t.Error("tapping Quiz me must confirm the request — silence is what caused the double-tap")
	}

	// 2. The control no longer invites a second tap while the request is pending.
	if n := evalInt(t, p, `document.querySelectorAll("button[name='requestQuiz']").length`); n != 0 {
		t.Errorf("while a request is unanswered the button must be replaced by a pending\n"+
			"state, so a second tap cannot queue a duplicate; found %d tappable button(s)", n)
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-trigger.is-pending').length`); n != 1 {
		t.Errorf("expected the pending marker to be shown, got %d", n)
	}

	// Exactly one request reached the queue.
	if rows := p.readCSV(); len(rows) != 2 { // header + 1
		t.Fatalf("exactly one quiz request must be queued, got %d row(s)", len(rows)-1)
	}

	// 3. Once the agent answers, the control comes back so another quiz can be asked.
	submitQuiz(t, p, quizJSON)
	for i := 0; i < 60; i++ {
		if evalInt(t, p, `document.querySelectorAll("button[name='requestQuiz']").length`) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("after the quiz arrives the control must be tappable again\nstderr: %s", p.stderr.String())
}
