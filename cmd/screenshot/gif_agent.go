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
)

// The four agent-loop flows (checks, learning, quiz, alternatives). Each runs on
// its own demo repo, server and per-user folders (capture-gifs.sh), because
// each writes state a later capture would show: a judge file, accepted
// learnings, shortcut taps, installed skills. The agent's side is the real
// binary, played by agentOut, as in the versions/thread flows.

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
	log.Printf("[gif] %s %v", filepath.Base(bin), args)
	return out.String(), nil
}

// agentComments is the review's actionable comments, as the agent reads them.
func agentComments(bin, repo string) ([]map[string]any, error) {
	out, err := agentOut(bin, "", "comments", "--json", "--out", repo)
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	if json.Unmarshal([]byte(out), &list) == nil {
		return list, nil
	}
	var wrapped struct {
		Comments []map[string]any `json:"comments"`
	}
	if err := json.Unmarshal([]byte(out), &wrapped); err != nil {
		return nil, fmt.Errorf("comments --json: %w", err)
	}
	return wrapped.Comments, nil
}

// requestComment is the open request prereview queued for the agent.
func requestComment(bin, repo, kind string) (map[string]any, error) {
	cs, err := agentComments(bin, repo)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		if c["request"] == kind {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no %s request among %d comments", kind, len(cs))
}

// waitFor polls a JS expression until it is truthy.
func waitFor(js string, timeout time.Duration) chromedp.Action {
	return chromedp.Poll(js, nil, chromedp.WithPollingTimeout(timeout), chromedp.WithPollingInterval(150*time.Millisecond))
}

// scrollTo centres the first element matching sel.
func scrollTo(sel string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q)?.scrollIntoView({block:'center'})`, sel), nil)
}

// clickByText clicks the first element matching sel whose text contains text.
func clickByText(sel, text string) chromedp.Action {
	return chromedp.Evaluate(fmt.Sprintf(`(()=>{const b=[...document.querySelectorAll(%q)].find(e=>e.textContent.includes(%q));if(b)b.click();return !!b;})()`, sel, text), nil)
}

// clearFlash dismisses the confirmation toast (it clears itself after 3s, but
// the stand-in agent answers sooner), so it doesn't cover the next frame.
func clearFlash() chromedp.Action {
	return chromedp.Tasks{
		clickJS(`.flash-toast button[name="clearFlash"]`),
		waitFor(`!document.querySelector('.flash-toast')`, 5*time.Second),
	}
}

// savedCommentID is the id of the comment card just saved.
func savedCommentID(ctx context.Context) string {
	var id string
	step(ctx, waitFor(`!!document.querySelector('.inline-comment input[name="id"]')`, 10*time.Second),
		chromedp.Evaluate(`document.querySelector('.inline-comment input[name="id"]').value`, &id))
	return id
}

var judgeHashes = regexp.MustCompile(`--content-hash (\S+) --rubric-hash (\S+)`)

// gifChecks — prose checks and judges (#234/#237): a filler word is flagged on
// its line, the Checks page lists the check, Run judges asks the agent, and its
// verdict lands on the judge's card. The repo's guide.md carries one appended
// "very", and the reader-first judge sits in the checks folder (capture-gifs.sh).
func gifChecks(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:checks", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "guide.md", 1280, 800)...); err != nil {
			return err
		}
		step(ctx, chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery), chromedp.Sleep(200*time.Millisecond))
		check(rec.capture(ctx, 150)) // the doc, with the checks bar

		step(ctx,
			clickByText(`.ck-strip.is-idle .ck-strip-pick button`, "Filler"),
			waitFor(`!!document.querySelector('.code mark.check-finding')`, 10*time.Second),
			scrollTo(`.code mark.check-finding`),
			chromedp.Sleep(300*time.Millisecond),
		)
		check(rec.capture(ctx, 200)) // the finding on its line

		step(ctx,
			chromedp.Click(`.ck-strip-close button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery),
			chromedp.Click(`.checks-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ckp-row[data-key="ckp-row-filler-intensifiers"]`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		check(rec.capture(ctx, 180)) // the Checks page

		step(ctx,
			chromedp.Click(`.lib-back button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip.is-idle button[name="runJudges"]`, chromedp.ByQuery),
			chromedp.Click(`.ck-strip button[name="runJudges"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ck-strip .ck-waiting`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		check(rec.capture(ctx, 140)) // asked the agent

		req, err := requestComment(bin, repo, "judge")
		if err != nil {
			return err
		}
		m := judgeHashes.FindStringSubmatch(fmt.Sprint(req["body"]))
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
		check(rec.capture(ctx, 280)) // the judge's verdict and critique
		return rec.encode(filepath.Join(outDir, "checks.gif"))
	})
}

// gifLearning — learning from your reviews (#238/#260): a comment, a background
// learning job the helper agent answers, the notice in the review, and the
// skill it became, with the agents told to use it. The job is queued directly,
// as the server does after a review with enough comments in it.
func gifLearning(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:learning", func(ctx context.Context) error {
		rec := &gifRec{}
		store := filepath.Join(repo, ".prereview")
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
		check(rec.capture(ctx, 220)) // the notice, with Undo

		step(ctx,
			chromedp.Click(`.library-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-card`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		check(rec.capture(ctx, 180)) // the learning, accepted

		step(ctx,
			chromedp.Click(`.lib-tabs form:has(input[value="skills"]) button`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-card[data-key="skill-learned-writing"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.lib-pointers .lc-agents`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
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
		check(rec.capture(ctx, 120)) // the diff
		step(ctx,
			chromedp.Click(`button[name="requestQuiz"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.quiz-trigger.is-pending`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		check(rec.capture(ctx, 130)) // quiz requested

		req, err := requestComment(bin, repo, "quiz")
		if err != nil {
			return err
		}
		if _, err := agentOut(bin, quizDemo, "quiz", "--out", repo, "--file", "-"); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "done", "--out", repo, fmt.Sprint(req["id"])); err != nil {
			return err
		}
		q1 := `.quiz-card[data-key='quiz-q1']`
		step(ctx,
			clearFlash(),
			waitFor(`!!document.querySelector("`+q1+`")`, 15*time.Second),
			scrollTo(q1),
			chromedp.Sleep(300*time.Millisecond),
		)
		check(rec.capture(ctx, 220)) // questions on the lines they ask about

		step(ctx,
			clickJS(q1+` .quiz-options li:nth-child(2) button[name='answerQuestion']`),
			waitFor(`!!document.querySelector("`+q1+` .quiz-why")`, 10*time.Second),
			scrollTo(q1),
			chromedp.Sleep(300*time.Millisecond),
		)
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
		check(rec.capture(ctx, 130)) // a phrase selected
		step(ctx,
			clickJS(`[data-lvt-text-select-button]`),
			chromedp.WaitVisible(`.composer .sc-chip`, chromedp.ByQuery),
			clickByText(`.composer .sc-chip`, "Make this shorter"),
			waitFor(`(document.querySelector('.composer textarea')||{}).value?.length > 0`, 10*time.Second),
			chromedp.Sleep(250*time.Millisecond),
		)
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
			waitFor(`!!document.querySelector('`+a+`') && !!document.querySelector('`+b+`')`, 15*time.Second),
			scrollTo(a),
			chromedp.Sleep(300*time.Millisecond),
		)
		check(rec.capture(ctx, 240)) // two alternatives, one to pick

		step(ctx,
			clickJS(b+` button[name="acceptSuggestion"]`),
			waitFor(`!!document.querySelector('`+b+`.is-decided.sg-accept')`, 10*time.Second),
			chromedp.Sleep(300*time.Millisecond),
			scrollTo(b),
			chromedp.Sleep(200*time.Millisecond),
		)
		check(rec.capture(ctx, 280)) // picked one; the other is set aside
		return rec.encode(filepath.Join(outDir, "alternatives.gif"))
	})
}
