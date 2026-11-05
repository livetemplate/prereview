//go:build browser

// #212: a quiz that has already been taken must come back closed. The server
// already treated an answered row as collapsed (green badge), but no CSS hid an
// answered card, so every answered card showed on launch and clicking the green
// badge HID them instead of peeking. Answering must still leave the explanation
// in front of the reviewer for the rest of that visit.
//
// Run: go test -tags=browser -run 'TestE2E_Quiz(Answered|JustAnswered)' ./e2e/...

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const quizReopenFile = "edited.go"

// bootQuizInline opens edited.go with quizJSON delivered, diagnostics attached.
func bootQuizInline(t *testing.T) (*runningPrereview, func() string) {
	t.Helper()
	p := bootChromeAgainstPrereview(t, 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	p.clickFile(quizReopenFile)
	submitQuiz(t, p, quizJSON)
	waitForQuizEntry(t, p)
	return p, diag
}

func quizCardShownJS(id string) string {
	return fmt.Sprintf(`(()=>{const c=document.querySelector(".quiz-card[data-key='quiz-%s']"); return !!c&&c.offsetParent!==null})()`, id)
}

// quizCardVisible reports whether the inline card for question id renders a box.
func quizCardVisible(t *testing.T, p *runningPrereview, id string) bool {
	t.Helper()
	var shown bool
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(quizCardShownJS(id), &shown)); err != nil {
		t.Fatalf("eval %s visibility: %v", id, err)
	}
	return shown
}

// expectQuizCard waits up to 3s for card id to reach the wanted visibility (a
// badge click or reload is a server round-trip), then reports a miss.
func expectQuizCard(t *testing.T, p *runningPrereview, id string, want bool, why string, diag func() string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	defer cancel()
	cond := quizCardShownJS(id)
	if !want {
		cond = "!" + cond
	}
	if err := chromedp.Run(ctx, chromedp.Poll(cond, nil, chromedp.WithPollingInterval(50*time.Millisecond))); err != nil {
		t.Errorf("%s: card %s visible=%v, want %v%s", why, id, !want, want, diag())
	}
}

func answerInline(t *testing.T, p *runningPrereview, id string, diag func() string) {
	t.Helper()
	sel := fmt.Sprintf(`.quiz-card[data-key='quiz-%s'] .quiz-options li:nth-child(1) button[name='answerQuestion']`, id)
	why := fmt.Sprintf(`.quiz-card[data-key='quiz-%s'] .quiz-why`, id)
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Click(sel, chromedp.ByQuery),
		chromedp.WaitVisible(why, chromedp.ByQuery),
	); err != nil {
		t.Fatalf("answer %s inline: the explanation must appear in place: %v%s", id, err, diag())
	}
}

// clickRowBadge clicks the annotation badge on the row holding question id, or
// the file-head badge for id == "" .
func clickRowBadge(t *testing.T, p *runningPrereview, id string) {
	t.Helper()
	sel := `.file-head-marks .line-marks`
	if id != "" {
		sel = fmt.Sprintf(`.line-row:has(.quiz-card[data-key='quiz-%s']) .line-marks`, id)
	}
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).click()`, sel), nil)); err != nil {
		t.Fatalf("click badge %q: %v", sel, err)
	}
}

func quizNavCount(t *testing.T, p *runningPrereview) int {
	t.Helper()
	return evalInt(t, p, `document.querySelectorAll('.quiz-nav').length`)
}

// reopenFile reloads the page (a new visit: QuizJustAnswered is session-only)
// and reopens the quiz file once the client is wired.
func reopenFile(t *testing.T, p *runningPrereview) {
	t.Helper()
	p.waitReady()
	p.clickFile(quizReopenFile)
	// Cards are normally all hidden here, so wait for presence, not visibility.
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.WaitReady(`.quiz-card`, chromedp.ByQuery)); err != nil {
		t.Fatalf("reopen %s: no quiz cards rendered: %v", quizReopenFile, err)
	}
}

func TestE2E_QuizAnsweredCollapsedOnReopen(t *testing.T) {
	p, diag := bootQuizInline(t)

	// Take the whole quiz inline. Every explanation must stay in front of the
	// reviewer for the rest of this visit — including earlier ones once later
	// questions are answered.
	ids := []string{"q1", "q2", "q3", "q4"}
	for _, id := range ids {
		answerInline(t, p, id, diag)
	}
	for _, id := range ids {
		if !quizCardVisible(t, p, id) {
			t.Errorf("%s was answered this visit; its card (and explanation) must stay visible%s", id, diag())
		}
	}
	if n := quizNavCount(t, p); n != 1 {
		t.Errorf("the navigator must not vanish the moment the last question is answered, got %d", n)
	}

	// Reopen: the finished quiz comes back closed.
	reopenFile(t, p)
	for _, id := range ids {
		expectQuizCard(t, p, id, false, "a quiz that has been taken must reopen collapsed", diag)
	}
	if n := quizNavCount(t, p); n != 0 {
		t.Errorf("a finished quiz must not bring its navigator back on reopen, got %d", n)
	}
	if n := evalInt(t, p, `document.querySelectorAll('.line-mark.is-open').length`); n != 0 {
		t.Errorf("every answered row's badge must read done, %d read open", n)
	}

	// The green badge PEEKS the answered card (it used to hide it), and folds it back.
	clickRowBadge(t, p, "q1")
	expectQuizCard(t, p, "q1", true, "clicking an answered row's green badge must reveal the answered card", diag)
	clickRowBadge(t, p, "q1")
	expectQuizCard(t, p, "q1", false, "a second badge click must fold the answered card away again", diag)

	// A link to a collapsed question still brings it back (:target).
	if err := chromedp.Run(p.ctx, chromedp.Evaluate(`location.hash = '#qc-q2'`, nil)); err != nil {
		t.Fatalf("hash: %v", err)
	}
	expectQuizCard(t, p, "q2", true, "linking to a collapsed answered question (#qc-q2) must reveal it", diag)
}

// A quiz left half-done reopens with the unanswered work showing and the
// navigator back, while the answered card stays folded.
func TestE2E_QuizAnsweredPartialReopen(t *testing.T) {
	p, diag := bootQuizInline(t)
	answerInline(t, p, "q1", diag)
	reopenFile(t, p)

	expectQuizCard(t, p, "q1", false, "the answered question must reopen collapsed", diag)
	for _, id := range []string{"q2", "q3", "q4"} {
		expectQuizCard(t, p, id, true, "an unanswered question stays visible as open work", diag)
	}
	if n := quizNavCount(t, p); n != 1 {
		t.Errorf("an unfinished quiz keeps its navigator, got %d", n)
	}
}

// A card answered this visit is open by default, so its badge click HIDES it;
// after a reload that toggle must expire rather than flip into a reveal. Covers a
// line row and the file head (its own row key, "file").
func TestE2E_QuizJustAnsweredToggleExpires(t *testing.T) {
	p, diag := bootQuizInline(t)

	answerInline(t, p, "q1", diag)
	clickRowBadge(t, p, "q1")
	expectQuizCard(t, p, "q1", false, "the badge must fold away a just-answered card", diag)

	// Both head questions answered, so the head has no other open work.
	answerInline(t, p, "q3", diag)
	answerInline(t, p, "q4", diag)
	clickRowBadge(t, p, "")
	expectQuizCard(t, p, "q4", false, "the file-head badge must fold away just-answered head questions", diag)

	reopenFile(t, p)
	for _, id := range []string{"q1", "q3", "q4"} {
		expectQuizCard(t, p, id, false, "after a reload the stale toggle must expire, leaving the answered card collapsed — not flipped open", diag)
	}
}
