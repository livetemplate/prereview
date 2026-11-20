package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/library"
	"github.com/livetemplate/prereview/internal/review"
)

// The four agent-loop flows (checks, learning, quiz, alternatives). The agent's
// side is the real binary, played by agentOut, as in the versions/thread flows.

// agentOut runs the prereview binary as the coding agent, with optional stdin,
// and returns its stdout.
func agentOut(bin, stdin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "PREREVIEW_NO_UPDATE=1")
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %v: %w\n%s%s", filepath.Base(bin), args, err, out.String(), errb.String())
	}
	log.Printf("[gif] %s %v → %s", filepath.Base(bin), args, bytes.TrimSpace(out.Bytes()))
	return out.String(), nil
}

// agentComments is the review's actionable comments, as the agent reads them.
func agentComments(bin, repo string) ([]review.StreamComment, error) {
	out, err := agentOut(bin, "", "comments", "--json", "--out", repo)
	if err != nil {
		return nil, err
	}
	var list []review.StreamComment
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("comments --json: %w", err)
	}
	return list, nil
}

// requestComment is the open request prereview queued for the agent.
func requestComment(bin, repo, kind string) (review.StreamComment, error) {
	cs, err := agentComments(bin, repo)
	if err != nil {
		return review.StreamComment{}, err
	}
	for _, c := range cs {
		if c.Request == kind {
			return c, nil
		}
	}
	return review.StreamComment{}, fmt.Errorf("no %s request among %d comments", kind, len(cs))
}

// clearFlash dismisses the confirmation toast (it clears itself after 3s, but
// the stand-in agent answers sooner), so it doesn't cover the next frame.
func clearFlash() chromedp.Action {
	return chromedp.Tasks{
		clickJS(`.flash-toast button[name="clearFlash"]`),
		chromedp.WaitNotPresent(`.flash-toast`, chromedp.ByQuery),
	}
}

// savedCommentID is the id of the comment card just saved.
func savedCommentID(ctx context.Context) string {
	var id string
	step(ctx, chromedp.Value(`.inline-comment input[name="id"]`, &id, chromedp.ByQuery))
	return id
}

var judgeHashes = regexp.MustCompile(`--content-hash (\S+) --rubric-hash (\S+)`)

// readerFirstJudge is the judge the checks flow runs.
const readerFirstJudge = "---\nkind: judge\nname: Reader first\ncategory: Reader-first\n---\nDoes the text lead with what the reader must do?\n"

// gifChecks — prose checks and judges (#234/#237): a filler word is flagged on
// its line, the Checks page lists the check, Run judges asks the agent, and its
// verdict lands on the judge's card.
func gifChecks(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:checks", func(ctx context.Context) error {
		rec := &gifRec{}
		// The judge, in the checks folder (read when the page needs it). The
		// filler word in guide.md is added before the server starts
		// (capture-gifs.sh): a reviewed file changing under the server would
		// show the "changing on disk" notice in every frame.
		dir := os.Getenv("PREREVIEW_CHECKS_DIR")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "reader-first.md"), []byte(readerFirstJudge), 0o644); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, base(url, "guide.md", 1280, 800)...); err != nil {
			return err
		}
		step(ctx, chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond))
		step(ctx, settled())
		check(rec.capture(ctx, 150)) // the doc, with the checks bar

		step(ctx,
			clickJS(`.ck-strip-sum[aria-expanded="false"]`), // the bar starts as one summary line
			chromedp.WaitVisible(`.ck-strip-list`, chromedp.ByQuery),
			clickByText(`.ck-strip.is-idle .ck-strip-pick button`, "Filler"),
			chromedp.WaitReady(`.code mark.check-finding`, chromedp.ByQuery),
			scrollTo(`.code mark.check-finding`),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 200)) // the finding on its line

		step(ctx,
			chromedp.Click(`.ck-strip-close button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery),
			chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ckp-row[data-key="ckp-row-filler-intensifiers"]`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 180)) // the Checks page

		step(ctx,
			chromedp.Click(`.lib-back button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip.is-idle button[name="runJudges"]`, chromedp.ByQuery),
			chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip .ck-waiting`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 140)) // asked the agent

		req, err := requestComment(bin, repo, "judge")
		if err != nil {
			return err
		}
		m := judgeHashes.FindStringSubmatch(req.Body)
		if m == nil {
			return fmt.Errorf("the judge request has no hashes")
		}
		if _, err := agentOut(bin, "", "judge", "--out", repo, "--content-hash", m[1], "--rubric-hash", m[2],
			"--file", "guide.md", "--judge", "reader-first", "--verdict", "fail",
			"--critique", "Opens with how charges flow, not with what the reader must do.",
			"--quote", "How charges and refunds flow through the service.", "--model", "demo-model"); err != nil {
			return err
		}
		step(ctx,
			clearFlash(),
			chromedp.WaitVisible(`.ck-strip .ck-strip-pick button.is-judge`, chromedp.ByQuery),
			clickByText(`.ck-strip .ck-strip-pick button.is-judge`, "Reader first"),
			chromedp.WaitVisible(`.jv-card`, chromedp.ByQuery),
			scrollTo(`.jv-card`),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 280)) // the judge's verdict and critique
		return rec.encode(filepath.Join(outDir, "checks.gif"))
	})
}

// gifLearning — learning from your reviews (#238/#260): a comment, a background
// learning job the helper agent answers, the notice in the review, and the
// skill it became, with the agents told to use it. The job is queued directly:
// the server queues one when the agent goes idle after five or more new
// reviewer signals (comments, replies, decisions), which no frame shows.
func gifLearning(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:learning", func(ctx context.Context) error {
		rec := &gifRec{}
		store := filepath.Join(repo, ".prereview")
		// A skills folder, so Claude Code counts as set up on the Skills tab.
		if err := os.MkdirAll(filepath.Join(os.Getenv("PREREVIEW_HOME"), ".claude", "skills"), 0o755); err != nil {
			return err
		}
		if err := chromedp.Run(ctx, base(url, "guide.md:L12", 1280, 800)...); err != nil {
			return err
		}
		step(ctx,
			chromedp.WaitVisible(`.composer textarea`, chromedp.ByQuery),
			chromedp.SendKeys(`.composer textarea`, "Cut the filler: say what is retried and when.", chromedp.ByQuery),
			chromedp.Sleep(150*time.Millisecond),
			chromedp.Click(saveBtn, chromedp.ByQuery),
		)
		cid := savedCommentID(ctx)
		step(ctx, chromedp.Sleep(300*time.Millisecond))
		step(ctx, settled())
		check(rec.capture(ctx, 150)) // the reviewer's comment

		if _, _, err := library.QueueJob(library.Dir(), library.Job{Stores: []string{store}, AutoAccept: true, Server: store}, false); err != nil {
			return err
		}
		out, err := agentOut(bin, "", "learn", "wait", "--timeout", "20s")
		if err != nil {
			return err
		}
		job := strings.TrimPrefix(strings.SplitN(out, "\n", 2)[0], "job ")
		if _, err := agentOut(bin, "", "learn", "propose", "--job", job,
			"--text", "Say what happens and when; cut words that add no meaning.",
			"--evidence", store+":"+cid); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "learn", "finish", job); err != nil {
			return err
		}
		step(ctx, chromedp.WaitVisible(`.learn-toast .toast-msg`, chromedp.ByQuery), chromedp.Sleep(300*time.Millisecond))
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // the notice, with Undo

		step(ctx,
			chromedp.Click(`.library-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-card`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 180)) // the learning, accepted

		step(ctx,
			chromedp.Click(`.lib-tabs form:has(input[value="skills"]) button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-card[data-key="skill-learned-writing"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-pointers .lc-agents`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 280)) // the skill, and the agents told to use it
		return rec.encode(filepath.Join(outDir, "learning.gif"))
	})
}

// quizDemo is the stand-in agent's quiz on payment.go's diff: two questions on
// the changed lines (13–18) and one decision question about the whole file.
const quizDemo = `{"id":"pay1","file":"payment.go","questions":[
 {"id":"q1","probe":"consequence","prompt":"If the gateway keeps failing, what does Charge return?","options":["The gateway's last error","A generic \"charge failed after retries\" error","nil"],"answer":1,"why":"Line 18 returns errors.New(...): the gateway's own error is dropped.","from_line":18,"to_line":18,"side":"new"},
 {"id":"q2","probe":"rationale","prompt":"How long does the loop wait between attempts?","options":["Exponential backoff","A fixed 50ms","It doesn't wait"],"answer":2,"why":"Lines 13–17 call Submit back to back with no delay.","from_line":13,"to_line":17,"side":"new"},
 {"id":"q3","kind":"file","probe":"decision","prompt":"What did this change decide that nobody asked for?","options":["It added a dependency","It retries every error, declines included"],"answer":1,"why":"Nothing tells a transient failure from a card decline."}]}`

// gifQuiz — the comprehension quiz (#191): Quiz me asks the agent, its
// questions appear on the lines they ask about, and answering one shows why.
func gifQuiz(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:quiz", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go", 1280, 800)...); err != nil {
			return err
		}
		step(ctx, settled())
		check(rec.capture(ctx, 120)) // the diff
		step(ctx,
			chromedp.Click(`button[name="requestQuiz"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.quiz-trigger.is-pending`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 130)) // quiz requested

		req, err := requestComment(bin, repo, "quiz")
		if err != nil {
			return err
		}
		if _, err := agentOut(bin, quizDemo, "quiz", "--out", repo, "--file", "-"); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "done", "--out", repo, req.ID); err != nil {
			return err
		}
		q1 := `.quiz-card[data-key='quiz-q1']`
		step(ctx,
			clearFlash(),
			chromedp.WaitReady(q1, chromedp.ByQuery),
			scrollTo(q1),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // questions on the lines they ask about

		step(ctx,
			clickJS(q1+` .quiz-options li:nth-child(2) button[name='answerQuestion']`),
			chromedp.WaitReady(q1+` .quiz-why`, chromedp.ByQuery),
			scrollTo(q1),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 280)) // answered, with the explanation
		return rec.encode(filepath.Join(outDir, "quiz.gif"))
	})
}

// selectPhraseJS selects a phrase inside the rendered Markdown, the way a
// reader's drag does: a native Range fires selectionchange, which the
// text-select directive listens for.
const selectPhraseJS = `(() => {
	const phrase = %q;
	const walker = document.createTreeWalker(document.querySelector('.md-view'), NodeFilter.SHOW_TEXT);
	for (let n = walker.nextNode(); n; n = walker.nextNode()) {
		const i = n.textContent.indexOf(phrase);
		if (i < 0) continue;
		n.parentElement.scrollIntoView({block: 'center'});
		const r = document.createRange();
		r.setStart(n, i);
		r.setEnd(n, i + phrase.length);
		const sel = window.getSelection();
		sel.removeAllRanges();
		sel.addRange(r);
		return true;
	}
	return false;
})()`

// gifAlternatives — a shortcut chip on a selected phrase (#204), and the agent
// answering with two alternatives the reviewer picks between (#206).
func gifAlternatives(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:alternatives", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "guide.md", 1280, 800)...); err != nil {
			return err
		}
		var found bool
		step(ctx,
			chromedp.WaitVisible(`.md-rendered`, chromedp.ByQuery),
			chromedp.Evaluate(fmt.Sprintf(selectPhraseJS, "retried with backoff"), &found),
			chromedp.WaitVisible(`[data-lvt-text-select-button]`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		if !found {
			return fmt.Errorf("the phrase isn't in guide.md")
		}
		step(ctx, settled())
		check(rec.capture(ctx, 130)) // a phrase selected
		step(ctx,
			clickJS(`[data-lvt-text-select-button]`),
			chromedp.WaitVisible(`.composer .sc-chip`, chromedp.ByQuery),
			clickByText(`.composer .sc-chip`, "Make this shorter"),
			chromedp.Poll(`(document.querySelector('.composer textarea')||{}).value?.length > 0`, nil, chromedp.WithPollingTimeout(10*time.Second)),
			chromedp.Sleep(250*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 160)) // a shortcut chip filled the comment
		step(ctx, clickJS(saveBtn))
		cid := savedCommentID(ctx)
		step(ctx, chromedp.Sleep(300*time.Millisecond))

		const orig = "Transient gateway errors are retried with backoff."
		note := "Shorter: keep the backoff detail or not?"
		alts, _ := json.Marshal([]map[string]any{
			{"id": cid + "-a", "file": "guide.md", "from_line": 12, "to_line": 12, "original": orig, "proposed": "Transient gateway errors are retried.", "note": note},
			{"id": cid + "-b", "file": "guide.md", "from_line": 12, "to_line": 12, "original": orig, "proposed": "Gateway errors are retried with backoff.", "note": note},
		})
		if _, err := agentOut(bin, string(alts), "suggest", "--out", repo); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "reply", "--answered", "--body", "Two ways to say it: pick the card you prefer.", "--out", repo, cid); err != nil {
			return err
		}
		a, b := `.inline-suggestion[data-key="sg-`+cid+`-a"]`, `.inline-suggestion[data-key="sg-`+cid+`-b"]`
		step(ctx,
			chromedp.WaitReady(a, chromedp.ByQuery),
			chromedp.WaitReady(b, chromedp.ByQuery),
			scrollTo(a),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 240)) // two alternatives, one to pick

		// The decided cards fold away, so the pick is shown where it now waits:
		// the Queue, accepted and awaiting the agent.
		step(ctx,
			clickJS(b+` button[name="acceptSuggestion"]`),
			chromedp.WaitReady(b+`.is-decided.sg-accept`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Click(`.queue-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.queue-awaiting`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 280)) // picked one: accepted, waiting for the agent
		return rec.encode(filepath.Join(outDir, "alternatives.gif"))
	})
}
