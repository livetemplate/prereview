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
    {"id":"q4","kind":"file","probe":"decision","prompt":"QUESTION-FOUR what did you decide unasked?",
     "options":["added a dependency","skipped the error-path test"],"answer":1,
     "why":"EXPLAIN-FOUR the request never mentioned tests"}
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

	// PRIMARY SURFACE: the questions are annotations in the diff, like comments and
	// suggestions — not a separate screen. Assert that BEFORE opening the overview.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-card').length`); n != 4 {
		t.Fatalf("every question must render as an inline annotation; expected 4, got %d", n)
	}
	// A line-anchored question renders inside the row it asks about, so the code is
	// its own context — that is the whole reason for anchoring them.
	//
	// These next two assertions also guard an ORDERING bug that was latent for two
	// phases: Mount loaded the quizzes ~100 lines before it loaded CurrentDiff, so
	// grounding compared every cited line against a nil diff and condemned the whole
	// quiz. It stayed invisible while the quiz was a separate screen, because
	// ToggleQuiz re-grounded on open, by which point the diff was in hand. If
	// grounding regresses that way again, every question lands at the file head
	// flagged ungrounded — so inRow drops to 0 and the ungrounded count jumps.
	inRow := evalInt(t, p, `document.querySelectorAll('.line-row .quiz-card, .code .quiz-card').length`)
	if inRow < 2 {
		t.Errorf("line-anchored questions must render within the diff rows, got %d", inRow)
	}
	// The UNGROUNDED question cites a line the diff does not have, so it has no row
	// to live in. It must still be VISIBLE (at the file head) — dropping it would
	// hide a hallucinated question instead of surfacing it, which would quietly
	// undo the grounding check.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-card .quiz-ungrounded').length`); n != 1 {
		t.Errorf("the ungrounded question must still render, with its warning; got %d", n)
	}

	// The explanation stays hidden until you answer — otherwise there is no
	// retrieval practice, just reading.
	if txt := evalStr(t, p, `document.body.innerText`); strings.Contains(txt, "EXPLAIN-ONE") {
		t.Fatal("the explanation must stay hidden until the question is answered")
	}

	// Answer one INLINE, where the reviewer actually meets it.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.quiz-card[data-key='quiz-q1'] .quiz-options li:nth-child(2) button[name='answerQuestion']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("answer inline: %v\nstderr: %s", err, p.stderr.String())
	}
	if txt := evalStr(t, p, `document.querySelector(".quiz-card[data-key='quiz-q1']").innerText`); !strings.Contains(txt, "EXPLAIN-ONE") {
		t.Errorf("answering inline must reveal the explanation in place, got %q", txt)
	}

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

// A quiz question is a conversation, not a verdict. The reviewer can push back on
// one — "this option is ambiguous", "I think this is wrong" — and the agent can
// answer or revise. Reported as: "we should also be able to reply to a quiz
// question to have a conversation with the LLM to clarify or update things".
//
// This reuses the #149 thread machinery wholesale. The only new part is the
// target id: a question id is unique only WITHIN its quiz, so the thread target
// is the composite "<quizID>:<questionID>".
func TestE2E_QuizQuestionThread(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	// Reviewer replies on the question, in place.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.quiz-card[data-key='quiz-q1'] button[name='openReply']`, chromedp.ByQuery),
		chromedp.WaitVisible(`.quiz-card[data-key='quiz-q1'] .reply-form textarea`, chromedp.ByQuery),
		chromedp.SendKeys(`.quiz-card[data-key='quiz-q1'] .reply-form textarea`, "REVIEWER-ASKS why is option 2 right?", chromedp.ByQuery),
		chromedp.Click(`.quiz-card[data-key='quiz-q1'] button[name='postReply']`, chromedp.ByQuery),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("reply on a question: %v\nstderr: %s", err, p.stderr.String())
	}
	if txt := evalStr(t, p, `document.querySelector(".quiz-card[data-key='quiz-q1']").innerText`); !strings.Contains(txt, "REVIEWER-ASKS") {
		t.Fatalf("the reviewer's reply must appear under the question, got %q", txt)
	}

	// The agent answers out of process, addressing the question by its composite id
	// — exactly as it would reply to a comment or a suggestion.
	out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", "AGENT-ANSWERS because rename is atomic", "z1:q1").CombinedOutput()
	if err != nil {
		t.Fatalf("prereview reply on a quiz question: %v\n%s", err, out)
	}
	// Poll for the CONTENT, not just for a thread element: the reviewer's own reply
	// already created one, so waiting on `.thread` would pass instantly and prove
	// nothing about the agent's message arriving.
	var txt string
	for i := 0; i < 60; i++ {
		txt = evalStr(t, p, `document.querySelector(".quiz-card[data-key='quiz-q1']").innerText`)
		if strings.Contains(txt, "AGENT-ANSWERS") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(txt, "AGENT-ANSWERS") {
		t.Errorf("the agent's reply must appear live under the question (watcher fan-out), got %q\nstderr: %s", txt, p.stderr.String())
	}
	// The conversation belongs to THIS question only.
	if other := evalStr(t, p, `document.querySelector(".quiz-card[data-key='quiz-q2']").innerText`); strings.Contains(other, "AGENT-ANSWERS") {
		t.Error("a reply must attach to its own question, not leak onto the next one")
	}

	// An unknown question id fails loudly rather than recording a dangling thread.
	if out, err := exec.Command(p.binary, "reply", "--out", p.repo, "--body", "x", "z1:nope").CombinedOutput(); err == nil {
		t.Errorf("replying to a non-existent question must fail; got success: %s", out)
	}
}

// Anchoring questions to lines recovered the code as context but lost the "take
// the quiz" shape: there was no way from question 2 to question 3 except
// scrolling and hoping. The navigator is a strip of one badge per question —
// tap to jump, colour tells you where you are.
func TestE2E_QuizNavigator(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent") // the phone case
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")

	// No quiz yet, so nothing to navigate.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav').length`); n != 0 {
		t.Fatalf("the navigator must not appear before a quiz exists, got %d", n)
	}

	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	// It appears on its own — not behind a menu. Discoverability has been this
	// feature's repeated failure, so the bar is not something to go looking for.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav').length`); n != 1 {
		t.Fatalf("the navigator must appear once the file has a quiz, got %d", n)
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav-dot').length`); n != 4 {
		t.Fatalf("one badge per question; expected 4, got %d", n)
	}
	if lbl := evalStr(t, p, `[...document.querySelectorAll('.quiz-nav-dot')].map(b=>b.textContent).join("")`); lbl != "1234" {
		t.Errorf("badges must be numbered 1..N so they read as positions, got %q", lbl)
	}

	// Every badge starts open; answering one flips just that badge.
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav-dot.is-open').length`); n != 4 {
		t.Errorf("all questions start unanswered, got %d open", n)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`.quiz-card[data-key='quiz-q1'] .quiz-options li:nth-child(2) button[name='answerQuestion']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("answer: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav-dot.is-correct').length`); n != 1 {
		t.Errorf("answering correctly must show on its badge; got %d correct", n)
	}

	// The "you are here" highlight is CLIENT-SIDE (lvt-spy toggles lvt-active on the
	// badge whose card is on screen) — no server round-trip, so it never lags. The
	// badge is an <a href="#qc-…">, so a tap scrolls to the card natively and the
	// spy lights that badge. (Scroll-following is exercised thoroughly in
	// TestE2E_QuizNavigatorFollowsScroll; here we just confirm a tap lands a
	// highlight at all.)
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelectorAll('.quiz-nav-dot')[2].click()`, nil),
		chromedp.Sleep(700*time.Millisecond),
	); err != nil {
		t.Fatalf("tap badge 3: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav-dot.lvt-active').length`); n < 1 {
		t.Errorf("after tapping a badge, the spy must light a badge; got %d active", n)
	}

	// Dismiss puts it away.
	if err := chromedp.Run(p.ctx,
		chromedp.Click(`button[name='dismissQuizNav']`, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("dismiss: %v\nstderr: %s", err, p.stderr.String())
	}
	if n := evalInt(t, p, `document.querySelectorAll('.quiz-nav').length`); n != 0 {
		t.Errorf("the navigator must be dismissible while reading the diff, got %d", n)
	}
}

// A quiz question is an annotation, so it collapses like one: it counts toward
// the row's annotation badge, and the badge folds it away with the comments and
// suggestions on that line.
func TestE2E_QuizQuestionCollapsesWithTheRow(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	// A row carrying only a question still gets a badge — otherwise there would be
	// no way to collapse it, and no marker that the line carries anything.
	if n := evalInt(t, p, `document.querySelectorAll('.line-row.has-line-marks .line-marks').length`); n < 1 {
		t.Fatalf("a line with a quiz question must show the annotation badge, got %d", n)
	}

	visible := func() int {
		return evalInt(t, p, `[...document.querySelectorAll('.line-row .quiz-card')].filter(c=>c.offsetParent!==null).length`)
	}
	before := visible()
	if before < 1 {
		t.Fatalf("expected at least one inline question visible, got %d", before)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.line-row.has-line-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("toggle row: %v\nstderr: %s", err, p.stderr.String())
	}
	if after := visible(); after >= before {
		t.Errorf("the row badge must fold the question away like any other annotation; %d visible before, %d after", before, after)
	}
}

// A stray list marker is a PAINT bug: the text is present, so every DOM
// assertion passes while a square sits next to (or on top of) it. It slipped
// through twice — first beside the answer options, then over the navigator's
// question numbers. computed list-style-type is the one machine-checkable
// property that distinguishes "rendered correctly" from "text exists".
func TestE2E_QuizListsHaveNoStrayMarkers(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	for _, sel := range []string{".quiz-options", ".quiz-options > li", ".quiz-nav-dots", ".quiz-nav-dots > li"} {
		js := `[...document.querySelectorAll('` + sel + `')].map(e=>getComputedStyle(e).listStyleType).filter(v=>v!=='none').length`
		if n := evalInt(t, p, js); n != 0 {
			t.Errorf("%s must not paint a list marker — %d element(s) still do; the number or\n"+
				"option text is legible in the DOM either way, which is why this needs asserting", sel, n)
		}
	}
}

// The highlight follows the SCROLL, client-side. lvt-spy (the same primitive the
// TOC uses) toggles lvt-active on the badge whose question card is on screen, with
// no server round-trip — which is what makes it track without the lag the old
// server-side version had on a phone.
func TestE2E_QuizNavigatorFollowsScroll(t *testing.T) {
	// Reuses the read-progress suite's tall fixture: long.txt is 150 all-new lines,
	// so only one question can be on screen at a time. The standard fixture's
	// edited.go is five lines — every question is visible at once there, which
	// would make this assertion vacuous rather than passing.
	p := bootChromeAgainstRepo(t, setupLongFileRepo(t), 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("long.txt")

	// Questions spread far enough apart that only one can be on screen at a time.
	submitQuiz(t, p, `{"id":"zz","file":"long.txt","questions":[
	  {"id":"a","kind":"line","probe":"consequence","prompt":"Q-A","options":["x","y"],"answer":0,"why":"wa","from_line":10,"to_line":10,"side":"new"},
	  {"id":"b","kind":"line","probe":"rationale","prompt":"Q-B","options":["x","y"],"answer":0,"why":"wb","from_line":60,"to_line":60,"side":"new"},
	  {"id":"c","kind":"line","probe":"localization","prompt":"Q-C","options":["x","y"],"answer":0,"why":"wc","from_line":110,"to_line":110,"side":"new"}]}`)
	waitForQuizEntry(t, p)

	currentBadge := func() string {
		return evalStr(t, p, `(()=>{const b=document.querySelector('.quiz-nav-dot.lvt-active'); return b? b.textContent : "none"})()`)
	}
	// A card activates once its TOP scrolls above the spy trigger line, so scroll
	// each card to the top of the viewport (block:"start") to make it the active
	// one — matching how a reader scrolling down meets each question in turn.
	scrollTo := func(key string) string {
		if err := chromedp.Run(p.ctx,
			chromedp.Evaluate(`(()=>{const c=document.querySelector(".quiz-card[data-key='`+key+`']"); if(c) c.scrollIntoView({block:'start'});})()`, nil),
			chromedp.Sleep(700*time.Millisecond),
		); err != nil {
			t.Fatalf("scroll to %s: %v\nstderr: %s", key, err, p.stderr.String())
		}
		return currentBadge()
	}

	// Scrolling down through the questions must advance the highlight, and scrolling
	// back up must retreat it — a position indicator, not a history of clicks.
	atA, atB, atC := scrollTo("quiz-a"), scrollTo("quiz-b"), scrollTo("quiz-c")
	if atA == "none" || atB == "none" || atC == "none" {
		t.Fatalf("each question scrolled to the top must light a badge; got a=%q b=%q c=%q\nstderr: %s", atA, atB, atC, p.stderr.String())
	}
	if atA == atB || atB == atC {
		t.Errorf("the highlight must ADVANCE as each question passes the top; got a=%q b=%q c=%q — that is not tracking position", atA, atB, atC)
	}
	if back := scrollTo("quiz-a"); back != atA {
		t.Errorf("scrolling back up must retreat the highlight to the first question; got %q, expected %q", back, atA)
	}
}

// Annotations at the FILE HEAD collapse with the same badge a diff row uses.
//
// This was reported as "not able to collapse a quiz question", and the first fix
// was a quiz-only fold control — a second collapse mechanism beside the one that
// already worked. The real gap was general: the file head had no badge at all, so
// a file-level COMMENT could never be folded either. One shared row key ("file")
// fixes both, and quiz cards need nothing of their own.
func TestE2E_FileHeadAnnotationsCollapse(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	p.waitReady()
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	// The head badge exists, counts what is there, and is a right-aligned in-flow row
	// above the cards — NOT the diff row's absolute gutter badge, which collapsed to
	// zero height and hid under the sticky nav when the group was folded.
	if n := evalInt(t, p, `document.querySelectorAll('.file-head-marks > .file-head-badge-row > .line-marks').length`); n != 1 {
		t.Fatalf("the file-head badge must be an in-flow header row, got %d", n)
	}
	if side := evalStr(t, p, `(()=>{const b=document.querySelector('.file-head-marks .line-marks'), c=document.querySelector('.file-head-cards .quiz-card'); if(!b||!c) return "missing"; return b.getBoundingClientRect().right >= c.getBoundingClientRect().right - 40 ? "right" : "left";})()`); side != "right" {
		t.Errorf("the file-head badge must be right-aligned, was %s", side)
	}
	// And it must STAY visible when the group is collapsed — the whole point of the
	// in-flow row. An absolute badge pinned to the article top vanished under the nav.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("collapse: %v\nstderr: %s", err, p.stderr.String())
	}
	if v := evalInt(t, p, `(()=>{const b=document.querySelector('.file-head-marks .line-marks'); if(!b||b.offsetParent===null) return 0; const r=b.getBoundingClientRect(); return r.width>0 && r.height>0 ? 1 : 0})()`); v != 1 {
		t.Error("the file-head badge must stay laid out when the group is collapsed, so the toggle back is reachable")
	}
	// Expand again so the fold/unfold flow below starts from a known open state.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("re-expand: %v\nstderr: %s", err, p.stderr.String())
	}
	// It is the SAME control the diff rows use — not a quiz-specific one.
	if n := evalInt(t, p, `document.querySelectorAll(".file-head-marks button[name='toggleQuizCard'], .quiz-card button[name='toggleQuizCard']").length`); n != 0 {
		t.Errorf("there must be no quiz-only fold control; the row badge already does this, "+
			"and a second mechanism is exactly the inconsistency this feature kept being pulled toward (%d found)", n)
	}

	visible := func() int {
		return evalInt(t, p, `[...document.querySelectorAll('.file-head-cards .quiz-card')].filter(c=>c.offsetParent!==null).length`)
	}
	before := visible()
	if before < 1 {
		t.Fatalf("expected at least one unanchored question at the file head, got %d", before)
	}
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("toggle file head: %v\nstderr: %s", err, p.stderr.String())
	}
	if after := visible(); after != 0 {
		t.Errorf("the head badge must fold its annotations away; %d visible before, %d after", before, after)
	}
	// ...and back.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("untoggle: %v\nstderr: %s", err, p.stderr.String())
	}
	if after := visible(); after != before {
		t.Errorf("toggling back must restore them; %d before, %d after", before, after)
	}
}

// Tapping a question's breadcrumb EXPANDS it if collapsed, not just scrolls.
//
// Reported together: "collapsed badges are not visible. clicking breadcrumbs on
// top doesn't expand the collapsed question." Both are one flow — you collapse a
// question, its gutter badge is small (and for the file-head question, occluded
// by the sticky navigator), so the reliable way back is the breadcrumb, which
// lists every question and is always on screen. Before this it only scrolled,
// landing you on a hidden card.
func TestE2E_BreadcrumbExpandsCollapsedQuestion(t *testing.T) {
	p := bootChromeAgainstPrereview(t, 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	p.clickFile("edited.go")
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)

	// q4 is the kind=file question; q3 (ungrounded) also renders at the head, so
	// target q4 specifically rather than "the head card".
	headCard := `.file-head-cards .quiz-card[data-key="quiz-q4"]`
	if n := evalInt(t, p, `document.querySelectorAll('`+headCard+`').length`); n != 1 {
		t.Fatalf("expected the kind=file question at the head, got %d", n)
	}
	visible := func() bool {
		return evalInt(t, p, `(()=>{const c=document.querySelector('`+headCard+`'); return c&&c.offsetParent!==null?1:0})()`) == 1
	}
	if !visible() {
		t.Fatal("the file-head question should start visible")
	}

	// Collapse it via its badge.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.file-head-marks .line-marks').click()`, nil),
		chromedp.Sleep(400*time.Millisecond),
	); err != nil {
		t.Fatalf("collapse: %v\nstderr: %s", err, p.stderr.String())
	}
	if visible() {
		t.Fatal("clicking the badge must collapse the file-head question")
	}

	// Tap q4's breadcrumb (an <a href="#qc-q4">). Native anchor navigation sets the
	// URL hash, and the :target CSS force-shows the addressed card even though its
	// row is collapsed — the expand is pure client-side, no server round-trip.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`document.querySelector('.quiz-nav-dot[href="#qc-q4"]').click()`, nil),
		chromedp.Sleep(500*time.Millisecond),
	); err != nil {
		t.Fatalf("tap breadcrumb: %v\nstderr: %s", err, p.stderr.String())
	}
	if !visible() {
		t.Errorf("tapping a collapsed question's breadcrumb must reveal it (via :target); it stayed hidden\nstderr: %s", p.stderr.String())
	}
}

// The navigator badges are numbered by DOCUMENT position — top to bottom — not by
// the agent's JSON order. Reported: "Are the questions not sequential from top to
// bottom? I'm at the bottom and the highlighted question number is three." So at
// the bottom of the page the active badge must be the HIGHEST number, and the
// badge numbers must increase with the cards' vertical position.
func TestE2E_QuizNavigatorNumbersAreTopToBottom(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupLongFileRepo(t), 390, 844, "--agent")
	p.waitReadyAt(390, 844)
	p.clickFile("long.txt")
	submitQuiz(t, p, `{"id":"zn","file":"long.txt","questions":[
	  {"id":"a","kind":"line","probe":"consequence","prompt":"QA","options":["x","y"],"answer":0,"why":"wa","from_line":20,"to_line":20,"side":"new"},
	  {"id":"b","kind":"line","probe":"rationale","prompt":"QB","options":["x","y"],"answer":0,"why":"wb","from_line":80,"to_line":80,"side":"new"},
	  {"id":"c","kind":"line","probe":"localization","prompt":"QC","options":["x","y"],"answer":0,"why":"wc","from_line":140,"to_line":140,"side":"new"}]}`)
	waitForQuizEntry(t, p)

	// Badge number must increase with the target card's vertical position.
	monotonic := evalInt(t, p, `(()=>{
		const dots=[...document.querySelectorAll('.quiz-nav-dot')];
		const rows=dots.map(a=>{
			const c=document.getElementById(a.getAttribute('href').slice(1));
			return {num:parseInt(a.textContent,10), y:c?c.getBoundingClientRect().top+document.querySelector('main.viewer').scrollTop:0};
		});
		for(let i=1;i<rows.length;i++){ if(rows[i].num<rows[i-1].num || rows[i].y<rows[i-1].y) return 0; }
		return 1;
	})()`)
	if monotonic != 1 {
		t.Error("badge numbers must increase with the question's vertical position (top-to-bottom)")
	}

	// At the very bottom, the LAST question's badge is the active one — the
	// scroll-beyond-last-line padding lets it reach the spy trigger.
	if err := chromedp.Run(p.ctx,
		chromedp.Evaluate(`const m=document.querySelector('main.viewer'); m.scrollTop=m.scrollHeight; m.dispatchEvent(new Event('scroll'));`, nil),
		chromedp.Sleep(900*time.Millisecond),
	); err != nil {
		t.Fatalf("scroll to bottom: %v\nstderr: %s", err, p.stderr.String())
	}
	active := evalStr(t, p, `(()=>{const b=document.querySelector('.quiz-nav-dot.lvt-active'); return b? b.textContent : "none"})()`)
	last := evalStr(t, p, `(()=>{const d=[...document.querySelectorAll('.quiz-nav-dot')]; return d.length? d[d.length-1].textContent : "none"})()`)
	if active != last {
		t.Errorf("at the bottom of the page the active badge must be the last one (%s), got %s", last, active)
	}
}
