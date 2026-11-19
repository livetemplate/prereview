//go:build browser

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// longSentence is one 60-word sentence: the Sentence length check (over 35
// words) flags it, and it is far wider than any viewport.
var longSentence = strings.TrimSpace(strings.Repeat("The reviewer reads this sentence on a small screen and ", 6)) + " stops."

// setupFixtureRepoLongSentence is setupFixtureRepoChecks with line 40 replaced
// by longSentence, so the only failing check is Sentence length.
func setupFixtureRepoLongSentence(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")
	body := func(title string) string {
		var b strings.Builder
		b.WriteString("# " + title + "\n")
		for i := 2; i <= 60; i++ {
			if i == 40 {
				b.WriteString(longSentence + "\n")
				continue
			}
			fmt.Fprintf(&b, "Plain line %d.\n", i)
		}
		return b.String()
	}
	mustWrite(t, dir, "notes.md", body("Notes"))
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")
	mustWrite(t, dir, "notes.md", body("Notes, edited"))
	return dir
}

// TestE2E_FindingOnLongSentence: picking a check that flags a long line keeps
// the finding card inside the visible code area and puts its buttons on its
// first row at its right edge, so "Queue as comment" is in the same place
// whatever the line or message length. A prose line wraps (desktop included);
// a code line still scrolls sideways on desktop, without dragging the card.
func TestE2E_FindingOnLongSentence(t *testing.T) {
	codeRepo := func(t *testing.T) string {
		repo := setupFixtureRepoLongLine(t)
		writeRule(t, repo, "logger.yml", "extends: existence\nname: Named logger\nmessage: \"'%s': pass the logger in instead.\"\nlevel: warning\nfiles: ['*.go']\ntokens:\n  - WithLogger\n")
		return repo
	}
	for _, vp := range []struct {
		name, check, tail string
		repo              func(*testing.T) string
		wraps             bool
		w, h              int
	}{
		{"prose-desktop", "Sentence length", "stops.", setupFixtureRepoLongSentence, true, 1200, 800},
		{"prose-phone", "Sentence length", "stops.", setupFixtureRepoLongSentence, true, 390, 844},
		{"code-desktop", "Named logger", "metrics.Default))", codeRepo, false, 1200, 800},
	} {
		t.Run(vp.name, func(t *testing.T) {
			p := bootChromeAgainstRepo(t, vp.repo(t), vp.w, vp.h)
			diag := captureDiag(t, p)
			p.waitReadyAt(vp.w, vp.h)
			ctx, cancel := within(p, 20*time.Second)
			defer cancel()

			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.ck-strip.is-idle`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no failing-checks bar: %v%s", err, diag())
			}
			clickStripPick(t, p, vp.check, diag)
			if err := chromedp.Run(ctx, chromedp.WaitVisible(`.code .finding-card button[name="queueFinding"]`, chromedp.ByQuery)); err != nil {
				t.Fatalf("no finding card: %v%s", err, diag())
			}

			var m struct {
				ContentH, LineH                      float64
				CodeLeft, CodeRight                  float64
				CardLeft, CardRight, CardTop         float64
				BtnLeft, BtnRight, BtnTop, ActsRight float64
			}
			js := `(() => {
				const row = document.querySelector('.code .line-row:has(.finding-card)');
				const content = row.querySelector('.content');
				const code = document.querySelector('.code').getBoundingClientRect();
				const card = row.querySelector('.finding-card').getBoundingClientRect();
				const btn = row.querySelector('.finding-card button[name="queueFinding"]').getBoundingClientRect();
				const acts = row.querySelector('.finding-actions').getBoundingClientRect();
				return {
					ContentH: content.getBoundingClientRect().height,
					LineH: parseFloat(getComputedStyle(content).lineHeight),
					CodeLeft: code.left, CodeRight: code.left + document.querySelector('.code').clientWidth,
					CardLeft: card.left, CardRight: card.right, CardTop: card.top,
					BtnLeft: btn.left, BtnRight: btn.right, BtnTop: btn.top, ActsRight: acts.right,
				};
			})()`
			if err := chromedp.Run(ctx, chromedp.Evaluate(js, &m)); err != nil {
				t.Fatalf("measure: %v%s", err, diag())
			}
			t.Logf("%s geometry: %+v", vp.name, m)

			if !strings.Contains(textOf(t, p, `.code .line-row:has(.finding-card) .content`), vp.tail) {
				t.Fatalf("the card is not on the long line's row%s", diag())
			}
			if wrapped := m.ContentH >= m.LineH*1.5; wrapped != vp.wraps {
				t.Errorf("the flagged line wrapped=%v, want %v: content height %.1f, line height %.1f%s", wrapped, vp.wraps, m.ContentH, m.LineH, diag())
			}
			if m.CardLeft < m.CodeLeft-1 || m.CardRight > m.CodeRight+1 {
				t.Errorf("the card [%.0f, %.0f] runs outside the visible code area [%.0f, %.0f]%s",
					m.CardLeft, m.CardRight, m.CodeLeft, m.CodeRight, diag())
			}
			if m.BtnLeft < m.CodeLeft || m.BtnRight > m.CodeRight {
				t.Errorf("Queue as comment [%.0f, %.0f] is outside the visible code area [%.0f, %.0f]%s",
					m.BtnLeft, m.BtnRight, m.CodeLeft, m.CodeRight, diag())
			}
			// First row of the card, at its right edge (inside the card's padding).
			if m.BtnTop-m.CardTop > 16 {
				t.Errorf("Queue as comment is %.0fpx below the card's top: it should sit on the first row%s", m.BtnTop-m.CardTop, diag())
			}
			if m.CardRight-m.ActsRight > 16 {
				t.Errorf("the card's buttons end %.0fpx short of its right edge: they should be pinned right%s", m.CardRight-m.ActsRight, diag())
			}
			noHorizontalScroll(t, p, diag)
		})
	}
}
