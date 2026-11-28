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

// The four agent-loop flows (checks, learning, understand, alternatives). The agent's
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
		clickIfPresent(`.flash-toast button[name="clearFlash"]`),
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
			clickIfPresent(`.ck-strip-sum[aria-expanded="false"]`), // the bar starts as one summary line
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
			chromedp.Click(`.checks-dropdown .checks-trigger`, chromedp.ByQuery), // #279: a menu once judges apply
			chromedp.Click(`.checks-panel button[name="openChecks"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.ckp-row[data-key="ckp-row-filler-intensifiers"]`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 180)) // the Checks page

		step(ctx,
			chromedp.Click(`.lib-back button`, chromedp.ByQuery),
			chromedp.Click(`.checks-dropdown .checks-trigger`, chromedp.ByQuery),
			// Run judges is always listed, greyed out until a judge applies (#291).
			chromedp.WaitVisible(`.checks-panel button[name="runJudges"]:not([disabled])`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 120)) // Run judges in the Checks menu

		step(ctx,
			chromedp.Click(`.checks-panel button[name="runJudges"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.checks-trigger.is-waiting`, chromedp.ByQuery),
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

// explainDemo is the stand-in agent's walkthrough of the demo change: the retry
// loop and its error under one concern (with a risk, the code it affects and a
// flow diagram), then the guide. It leaves architecture.png out on purpose, so
// the derived "Not explained" step shows the check catching it. The demo change
// has no mechanical part, so it shows no fold.
const explainDemo = `{"id":"walk1","summary":"Charge now retries the gateway, and the guide states a charge limit.",
"concerns":[{"id":"retry","title":"Retry the gateway"},{"id":"docs","title":"Document the limit"}],
"steps":[
 {"title":"Charge retries the gateway","concern":"retry","body":"Submit runs up to maxRetries times and returns on the first success. Check: every error is retried, card declines included.","anchors":[{"file":"payment.go","from_line":13,"to_line":17}],
  "risks":[{"kind":"error-handling","note":"a card decline is retried like a timeout"}],
  "impact":[{"file":"payment.go","from_line":22,"to_line":23,"contains":"gateway.Reverse","note":"Refund still calls the gateway once"}]},
 {"title":"One error for every failure","concern":"retry","body":"After the last attempt Charge returns a generic error, so the gateway's own error is lost.","anchors":[{"file":"payment.go","from_line":18,"to_line":18}]},
 {"title":"The guide states the limit","concern":"docs","body":"A new Limits section: 100,000 cents per order. Nothing in the code enforces it yet.","anchors":[{"file":"guide.md","from_line":18,"to_line":20}]}],
"diagrams":[{"title":"How Charge retries","mermaid":"graph LR\n  Charge --> Submit{gateway.Submit}\n  Submit -->|ok| Done[return nil]\n  Submit -->|error| More{attempts left?}\n  More -->|yes| Submit\n  More -->|no| Fail[charge failed]","anchors":[{"file":"payment.go","from_line":13,"to_line":18}]}]}`

// gifUnderstand — the Understand page (#115): Explain this change asks the agent
// for a walkthrough, its steps arrive under their concerns with every cited line
// checked and the left-out change listed last, the Diagrams tab draws the flow,
// and the step bar walks the change file to file.
func gifUnderstand(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:understand", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go", 1280, 800)...); err != nil {
			return err
		}
		step(ctx, settled())
		check(rec.capture(ctx, 120)) // the diff
		step(ctx,
			chromedp.Click(`.toolbar-inline button[name="toggleUnderstand"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`button[name="requestExplain"]`, chromedp.ByQuery),
			chromedp.Click(`button[name="requestExplain"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.understand .request-pending`, chromedp.ByQuery),
			chromedp.Sleep(200*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 140)) // walkthrough requested

		req, err := requestComment(bin, repo, "explain")
		if err != nil {
			return err
		}
		if _, err := agentOut(bin, explainDemo, "explain", "--out", repo, "--file", "-"); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "done", "--out", repo, req.ID); err != nil {
			return err
		}
		step(ctx,
			clearFlash(),
			chromedp.WaitVisible(`.walk-step-unexplained`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 260)) // the steps under their concerns, and the change no step explains

		step(ctx,
			chromedp.Click(`button[name="setUnderstandTab"][data-tab="diagrams"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.diagram .md-mermaid svg`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // the flow diagram, with the lines it cites

		step(ctx,
			chromedp.Click(`button[name="setUnderstandTab"][data-tab="walkthrough"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.walk-start button[name="startWalkthrough"]`, chromedp.ByQuery),
			chromedp.Click(`.walk-start button[name="startWalkthrough"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`.walk-bar`, chromedp.ByQuery),
			chromedp.WaitVisible(`.code .step-mark`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // step 1: the bar and the step's marker

		for _, want := range []string{"Step 3 of 4", "Step 4 of 4"} {
			var label string
			for range 4 { // a step can have more than one stop
				step(ctx,
					clickJS(`.walk-bar button[name="walkNext"]`),
					chromedp.Sleep(400*time.Millisecond),
					chromedp.Text(`.walk-bar .ann-nav-label`, &label, chromedp.ByQuery),
				)
				if strings.TrimSpace(label) == want {
					break
				}
			}
			if strings.TrimSpace(label) != want {
				return fmt.Errorf("the walk never reached %q (bar reads %q)", want, label)
			}
			step(ctx, settled())
			check(rec.capture(ctx, 220)) // into guide.md, then the left-out image
		}
		return rec.encode(filepath.Join(outDir, "understand.gif"))
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
		cid := saveComment(ctx)

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

// gifQuestions — the agent asks with options (#290): the reviewer flags the
// retry loop, the agent asks how it should wait between attempts, the reviewer
// taps an option, and the agent replies with what it built.
func gifQuestions(allocCtx context.Context, url, repo, bin, outDir string) {
	withCtx(allocCtx, "gif:questions", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go:L13-L16", 1280, 800)...); err != nil {
			return err
		}
		step(ctx,
			typeComposer("The retry loop doesn't wait between attempts, so a failing gateway gets three calls at once."),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 150)) // the reviewer's comment
		cid := saveComment(ctx)

		set, _ := json.Marshal(map[string]any{
			"id": cid + "-q", "for": cid, "file": "payment.go", "from_line": 13, "to_line": 16,
			"steps": []map[string]any{{
				"id": "wait", "prompt": "How should the retry loop wait between attempts?",
				"options": []map[string]string{
					{"label": "Back off: 100, 200, 400 ms", "detail": "A short outage recovers; a long one returns the error in under a second."},
					{"label": "Wait 200 ms each time", "detail": "Simpler, but a busy gateway gets the same pace of calls."},
				},
				"recommended": 0,
			}},
		})
		if _, err := agentOut(bin, string(set), "ask", "--out", repo); err != nil {
			return err
		}
		card := `.question-card[data-key="question-` + cid + `-q"]`
		step(ctx,
			chromedp.WaitVisible(card+` button[name="pickQuestionOption"]`, chromedp.ByQuery),
			scrollTo(card),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 260)) // the question, its pick marked Recommended

		step(ctx,
			chromedp.Click(card+` button[name="pickQuestionOption"]`, chromedp.ByQuery),
			chromedp.WaitReady(card+`.is-sent`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 150)) // one tap sends the answer

		const built = "Added the backoff: 100, 200 and 400 ms, then the error goes back to the caller."
		if _, err := agentOut(bin, "", "reply", "--out", repo, cid+"-q", "--body", built); err != nil {
			return err
		}
		if _, err := agentOut(bin, "", "done", "--out", repo, cid+"-q", cid); err != nil {
			return err
		}
		step(ctx,
			chromedp.Poll(fmt.Sprintf(`document.body.innerText.includes(%q)`, "Added the backoff"), nil, chromedp.WithPollingTimeout(10*time.Second)),
			scrollTo(card),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 300)) // the agent says what it built
		return rec.encode(filepath.Join(outDir, "questions.gif"))
	})
}

// gifAnnotations — every annotation in one list (#266/#291): comments and a
// suggestion across two files, the list's filter narrowed to suggestions, then
// Resolve all and its Undo.
func gifAnnotations(allocCtx context.Context, url, repo, bin, outDir string) {
	seedLineComment(allocCtx, url, "payment.go:L13-L16", "The retry loop doesn't wait between attempts.")
	seedLineComment(allocCtx, url, "payment.go:L18", "Return the gateway's error, not a generic string.")
	seedFileComment(allocCtx, url, "guide.md", "Open with what the reader has to do, then explain the flow.")
	if _, err := agentOut(bin, `{"id":"sg-guide","file":"guide.md","from_line":12,"to_line":12,"original":"Transient gateway errors are retried with backoff.","proposed":"Transient gateway errors are retried with exponential backoff, up to maxRetries attempts.","note":"be specific about the backoff strategy"}`, "suggest", "--out", repo); err != nil {
		fail("[gif:annotations] suggest: %v", err)
		return
	}
	withCtx(allocCtx, "gif:annotations", func(ctx context.Context) error {
		rec := &gifRec{}
		if err := chromedp.Run(ctx, base(url, "payment.go", 1280, 800)...); err != nil {
			return err
		}
		const list = `.ac-head`
		step(ctx,
			chromedp.Click(`.toolbar-inline button[name="toggleCommentList"]`, chromedp.ByQuery),
			chromedp.WaitVisible(list, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // every file's comments and suggestions

		// Kinds: turn comments off, leaving the suggestion.
		comments := list + ` .ann-filter form:has(input[name="kind"][value="comment"]) button[name="toggleAnnKind"]`
		step(ctx,
			chromedp.Click(list+` .ann-filter-trigger`, chromedp.ByQuery),
			chromedp.WaitVisible(comments, chromedp.ByQuery),
			chromedp.Sleep(250*time.Millisecond),
		)
		check(rec.capture(ctx, 130)) // the list's own filter
		step(ctx,
			chromedp.Click(comments, chromedp.ByQuery),
			chromedp.Sleep(400*time.Millisecond),
			closeMenus(),
			chromedp.Sleep(150*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 170)) // suggestions only

		step(ctx,
			clickJS(comments),
			chromedp.Sleep(400*time.Millisecond),
			closeMenus(),
			chromedp.Click(list+` button[name="resolveAll"]`, chromedp.ByQuery),
			chromedp.WaitVisible(`button[name="undoResolveAll"]`, chromedp.ByQuery),
			chromedp.Sleep(300*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 220)) // Resolve all, with its Undo

		step(ctx,
			chromedp.Click(`button[name="undoResolveAll"]`, chromedp.ByQuery),
			chromedp.WaitNotPresent(`button[name="undoResolveAll"]`, chromedp.ByQuery),
			chromedp.Sleep(400*time.Millisecond),
		)
		step(ctx, settled())
		check(rec.capture(ctx, 200)) // undone: the comments are open again
		return rec.encode(filepath.Join(outDir, "annotations.gif"))
	})
}
