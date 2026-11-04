//go:build browser

// End-to-end for #194: the Queue shows who is consuming it, observed from
// <STORE>/agent-cursor.json (written by `prereview watch` / `comments --json`),
// not from anything the agent has to volunteer.
//
// Run: go test -tags=browser -run 'TestE2E_AgentCursor' ./e2e/...

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cdpnetwork "github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/livetemplate/prereview/internal/review"
)

// cursorHarness boots an --agent server on the line-comment fixture with the
// captureDiag dump. Set any PREREVIEW_AGENT_* timing env BEFORE calling it.
func cursorHarness(t *testing.T) (*runningPrereview, func() string) {
	t.Helper()
	p := bootChromeAgainstRepo(t, setupFixtureRepo(t), 1200, 800, "--agent")
	diag := captureDiag(t, p)
	p.waitReady()
	return p, diag
}

func storeOf(p *runningPrereview) string { return filepath.Join(p.repo, ".prereview") }

func stampCursor(t *testing.T, p *runningPrereview, c review.AgentCursor) {
	t.Helper()
	if err := review.WriteAgentCursor(storeOf(p), c); err != nil {
		t.Fatalf("write agent cursor: %v", err)
	}
}

// heartbeat re-stamps a waiting watch every interval until the returned stop is
// called — a genuine beat (a fresh mtime each time), as a blocked watch does.
func heartbeat(t *testing.T, p *runningPrereview, seq int, interval time.Duration) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	exited := make(chan struct{})
	beat := review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorWaiting, DeliveredSeq: seq}
	stampCursor(t, p, beat)
	go func() {
		defer close(exited)
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-done:
				return
			case <-tk.C:
				_ = review.WriteAgentCursor(storeOf(p), beat)
			}
		}
	}()
	// Waiting for exit matters: a late beat must not overwrite the next stamp.
	stop = sync.OnceFunc(func() { close(done); <-exited })
	t.Cleanup(stop)
	return stop
}

// agentPhaseShown returns the phase class on the Queue trigger's dot and the
// panel's status line text ("" when absent).
func agentPhaseShown(p *runningPrereview) (phase, label string) {
	var out []string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const d = document.querySelector('.queue-trigger .agent-dot');
		const l = document.querySelector('.queue-agent');
		const ph = d ? [...d.classList].find(c => c.startsWith('is-')) : '';
		return [ph ? ph.slice(3) : '', l ? l.textContent.trim() : ''];
	})()`, &out))
	if len(out) != 2 {
		return "", ""
	}
	return out[0], out[1]
}

// agentDotFilled reports whether the Queue trigger's dot paints a solid
// background (vs. a hollow ring).
func agentDotFilled(p *runningPrereview) bool {
	var bg string
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`(() => {
		const d = document.querySelector('.queue-trigger .agent-dot');
		return d ? getComputedStyle(d).backgroundColor : '';
	})()`, &bg))
	return bg != "" && bg != "transparent" && bg != "rgba(0, 0, 0, 0)"
}

func waitPhase(t *testing.T, p *runningPrereview, want, labelHas string, within time.Duration, diag func() string) time.Duration {
	t.Helper()
	start := time.Now()
	var phase, label string
	for time.Since(start) < within {
		if phase, label = agentPhaseShown(p); phase == want && strings.Contains(label, labelHas) {
			took := time.Since(start)
			// Fill means connected: only a watching dot is solid, every other
			// phase is a ring, so attachment never rests on hue alone.
			if filled := agentDotFilled(p); filled != (want == "watching") {
				t.Fatalf("phase %q: trigger dot filled=%v, want %v%s", want, filled, want == "watching", diag())
			}
			return took
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("phase = %q / %q after %v; want %q containing %q%s", phase, label, within, want, labelHas, diag())
	return 0
}

func stallPromptShown(p *runningPrereview) bool {
	var n int
	_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.querySelectorAll('.agent-stall-prompt').length`, &n))
	return n > 0
}

// TestE2E_AgentCursorPhases walks idle → watching → working → stalled → watching
// from the sidecar alone, and checks the stall prompt appears only with work
// outstanding and clears on the next stamp.
func TestE2E_AgentCursorPhases(t *testing.T) {
	t.Setenv("PREREVIEW_AGENT_STALE", "2s")
	t.Setenv("PREREVIEW_AGENT_STALL_AFTER", "4s")
	p, diag := cursorHarness(t)

	waitPhase(t, p, "idle", "No agent has picked this queue up yet", 5*time.Second, diag)

	stop := heartbeat(t, p, -1, 300*time.Millisecond)
	waitPhase(t, p, "watching", "Agent connected", 5*time.Second, diag)

	stop()
	stampCursor(t, p, review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorDelivered, DeliveredSeq: 1000})
	waitPhase(t, p, "working", "Agent picked up your queue at", 5*time.Second, diag)
	if stallPromptShown(p) {
		t.Fatalf("stall prompt shown with no outstanding work%s", diag())
	}

	// The agent's own echo wins the line while it works…
	writeLLMStatusFile(t, p.repo, `{"state":"working","message":"Applying your review"}`)
	waitPhase(t, p, "working", "Applying your review", 5*time.Second, diag)

	// …but once work is queued and it stays silent past AgentStallAfter, that
	// never-closed echo must not hide the stall (#203's shape: turn ended, no done).
	p.clickFile("edited.go")
	addLineComment(t, p, 3, 3, "rename this")
	waitPhase(t, p, "stalled", "No word from the agent since", 10*time.Second, diag)
	if !stallPromptShown(p) {
		t.Fatalf("stalled phase without the stall prompt%s", diag())
	}

	// The next stamp clears it (the line is the agent's echo again — it is attached).
	heartbeat(t, p, 1000, 300*time.Millisecond)
	waitPhase(t, p, "watching", "Applying your review", 5*time.Second, diag)
	if stallPromptShown(p) {
		t.Fatalf("stall prompt survived a fresh heartbeat%s", diag())
	}
}

// TestE2E_AgentCursorHeartbeatsAreSilent is the "no periodic fan-out" guard: a
// blocked watch re-stamps the sidecar every few seconds, and none of those beats
// may push anything to the page. (The audit found morphdom keeps an open Queue
// panel open across a fan-out, so the cost being guarded is a re-render of every
// tab per beat, not a closing panel — count the frames rather than the panel.)
// The control at the end proves the counter does see a real transition.
func TestE2E_AgentCursorHeartbeatsAreSilent(t *testing.T) {
	p, diag := cursorHarness(t)
	var frames atomic.Int64
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if _, ok := ev.(*cdpnetwork.EventWebSocketFrameReceived); ok {
			frames.Add(1)
		}
	})
	stop := heartbeat(t, p, -1, 250*time.Millisecond)
	waitPhase(t, p, "watching", "Agent connected", 5*time.Second, diag)
	time.Sleep(time.Second) // let the transition's own render land

	before := frames.Load()
	time.Sleep(4 * time.Second) // ≥5 watcher polls, ~16 genuine beats
	if n := frames.Load() - before; n != 0 {
		t.Fatalf("%d WebSocket frames pushed during heartbeats — a periodic fan-out%s", n, diag())
	}

	stop()
	stampCursor(t, p, review.AgentCursor{Mode: review.AgentCursorWatch, State: review.AgentCursorDelivered, DeliveredSeq: 5})
	waitPhase(t, p, "working", "Agent picked up your queue at", 5*time.Second, diag)
	if frames.Load() == before {
		t.Fatalf("control: a delivery pushed no frame, so the counter proves nothing%s", diag())
	}
}

// startWatch runs the real `prereview watch` and returns what it delivered.
func startWatch(t *testing.T, p *runningPrereview, since int) <-chan []review.StreamEvent {
	t.Helper()
	res := make(chan []review.StreamEvent, 1)
	go func() {
		out, errb, exit := runCLI(t, 30*time.Second, p.binary, "watch", "--out", p.repo, "--since", fmt.Sprint(since))
		if exit != 0 {
			t.Errorf("watch exit=%d: %s", exit, errb)
		}
		res <- parseStreamEvents(out)
	}()
	return res
}

// TestE2E_AgentCursorRealWatch drives the real binary: a blocked `watch` shows
// as watching, a saved comment is delivered and flips the Queue to working, and
// the sidecar records the delivered seq. The save → working latency is checked
// as the median of three rounds (one round this tight is a CI flake shape).
func TestE2E_AgentCursorRealWatch(t *testing.T) {
	p, diag := cursorHarness(t)
	p.clickFile("edited.go")

	since := 0 // past ready@0
	var lat []time.Duration
	for i := range 3 {
		got := startWatch(t, p, since)
		waitPhase(t, p, "watching", "Agent connected", 5*time.Second, diag)

		start := time.Now()
		addLineComment(t, p, 3+i, 3+i, fmt.Sprintf("round %d", i))
		var evs []review.StreamEvent
		select {
		case evs = <-got:
		case <-time.After(15 * time.Second):
			t.Fatalf("round %d: watch never delivered%s", i, diag())
		}
		waitPhase(t, p, "working", "Agent picked up your queue at", 5*time.Second, diag)
		lat = append(lat, time.Since(start))

		if len(evs) == 0 {
			t.Fatalf("round %d: watch delivered nothing%s", i, diag())
		}
		since = evs[len(evs)-1].Seq
		b, err := os.ReadFile(filepath.Join(storeOf(p), review.AgentCursorFileName))
		if err != nil {
			t.Fatalf("round %d: no agent-cursor.json: %v%s", i, err, diag())
		}
		var c review.AgentCursor
		if err := json.Unmarshal(b, &c); err != nil || c.State != review.AgentCursorDelivered || c.DeliveredSeq != since {
			t.Fatalf("round %d: cursor = %s, want delivered@%d%s", i, b, since, diag())
		}
	}
	slices.Sort(lat)
	if med := lat[1]; med > 2*time.Second {
		t.Errorf("save → working median = %v (rounds %v), want ≤ 2s", med, lat)
	}

	// A one-shot `comments --json` client gets pickup without claiming attachment.
	if _, errb, exit := runCLI(t, 5*time.Second, p.binary, "comments", "--out", p.repo, "--json"); exit != 0 {
		t.Fatalf("comments --json exit=%d: %s", exit, errb)
	}
	waitPhase(t, p, "working", "Agent last read your queue at", 5*time.Second, diag)
}
