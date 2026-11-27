//go:build browser

package e2e

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

var updateWireBudgets = flag.Bool("update-wire-budgets", false,
	"rewrite e2e/testdata/wire-budgets.json from this run's measured sizes")

// wireBudgetsPath holds each step's measured raw and deflated bytes.
const wireBudgetsPath = "testdata/wire-budgets.json"

// wireSizes is one step's bytes: raw is what the browser parses and applies;
// deflated is each frame compressed on its own at flate level 1 (gorilla's
// per-message default), roughly what crosses the network.
type wireSizes struct {
	Raw      int `json:"raw"`
	Deflated int `json:"deflated"`
}

// wireBudget is how far a step may grow past its measured size: half again,
// with at least 1 KB of slack so small frames don't flake.
func wireBudget(measured int) int {
	return max(measured*3/2, measured+1024)
}

// wireCap is a hard raw limit, whatever the measured sizes say, on the
// wireCapped steps: the comment actions that each resent about 105 KB before
// livetemplate v0.26.1.
const wireCap = 12 << 10

var wireCapped = []string{
	"edit a comment", "Escape the edit", "open a reply", "post the reply",
	"close it (Escape)", "agent replies on a comment",
}

// wireFrame is one WebSocket reply: what action it answered, its size, and the
// two shapes that usually mean over-sending.
type wireFrame struct {
	Action     string `json:"action"`
	Size       int    `json:"size"`
	Deflated   int    `json:"deflated"`
	Statics    int    `json:"statics"`     // "s" arrays anywhere in the tree
	RangeItems int    `json:"range_items"` // largest "d" array (a range sent whole)
	Ops        int    `json:"ops"`         // range-op tuples (granular updates)
	raw        string
}

type wireStep struct {
	Name   string      `json:"name"`
	Frames []wireFrame `json:"frames"`
	Note   string      `json:"note,omitempty"`
}

// sizes totals the step's frames.
func (st wireStep) sizes() wireSizes {
	var sz wireSizes
	for _, f := range st.Frames {
		sz.Raw += f.Size
		sz.Deflated += f.Deflated
	}
	return sz
}

// setupWireAuditRepo is a review a reader might open: a long modified Go file, a
// long new Go file, a long modified Markdown guide and a small edited file.
func setupWireAuditRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runCmd(t, dir, "git", "init", "-q", "-b", "main")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test")
	runCmd(t, dir, "git", "config", "commit.gpgsign", "false")

	goFile := func(edited bool) string {
		var b strings.Builder
		b.WriteString("package big\n\nimport \"fmt\"\n\n")
		for i := range 60 {
			fmt.Fprintf(&b, "// Step%d does step %d of the pipeline.\nfunc Step%d(n int) int {\n", i, i, i)
			if edited && i%7 == 3 {
				fmt.Fprintf(&b, "\tif n < 0 {\n\t\treturn -%d // edited\n\t}\n", i)
			} else {
				fmt.Fprintf(&b, "\tif n < 0 {\n\t\treturn 0\n\t}\n")
			}
			fmt.Fprintf(&b, "\treturn n + %d\n}\n\n", i)
		}
		b.WriteString("func main() { fmt.Println(Step1(1)) }\n")
		return b.String()
	}
	md := func(edited bool) string {
		var b strings.Builder
		b.WriteString("# Payment guide\n\nHow charges and refunds flow through the service.\n\n")
		for i := range 25 {
			fmt.Fprintf(&b, "## Section %d\n\n", i)
			text := "A charge validates the amount and order, then submits to the gateway."
			if edited && i%6 == 2 {
				text = "A charge validates the amount, the order and the currency, then submits to the gateway (edited)."
			}
			fmt.Fprintf(&b, "%s See the notes for section %d.\n\n- first point %d\n- second point %d\n\n", text, i, i, i)
			if i%5 == 0 {
				fmt.Fprintf(&b, "```go\nfunc Example%d() {}\n```\n\n", i)
			}
		}
		return b.String()
	}
	mustWrite(t, dir, "big.go", goFile(false))
	mustWrite(t, dir, "guide.md", md(false))
	mustWrite(t, dir, "small.go", "package small\n\nfunc Hello() string {\n\treturn \"hi\"\n}\n")
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "commit", "-q", "-m", "seed")

	mustWrite(t, dir, "big.go", goFile(true))
	mustWrite(t, dir, "guide.md", md(true))
	mustWrite(t, dir, "small.go", "package small\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n")
	var nb strings.Builder
	nb.WriteString("package fresh\n\n")
	for i := range 100 {
		fmt.Fprintf(&nb, "func F%d() int {\n\treturn %d\n}\n", i, i)
	}
	mustWrite(t, dir, "fresh.go", nb.String())
	return dir
}

// TestE2E_WireAudit drives a review through the interactions a reader uses and
// records every WebSocket reply frame, then holds each step to its budget: the
// raw and deflated sizes in testdata/wire-budgets.json plus headroom
// (wireBudget), and wireCap for the comment actions. After an intended change,
// regenerate the sizes with -update-wire-budgets and read the diff. Set
// WIRE_AUDIT_DIR to also write report.json and each frame over 4 KB there.
func TestE2E_WireAudit(t *testing.T) {
	p := bootChromeAgainstRepo(t, setupWireAuditRepo(t), 1280, 900, "--agent")
	diag := captureDiag(t, p)

	var mu sync.Mutex
	var raws []string
	chromedp.ListenTarget(p.ctx, func(ev any) {
		if e, ok := ev.(*network.EventWebSocketFrameReceived); ok {
			mu.Lock()
			raws = append(raws, e.Response.PayloadData)
			mu.Unlock()
		}
	})
	p.waitReady()

	var steps []wireStep
	mark := func() int { mu.Lock(); defer mu.Unlock(); return len(raws) }
	collect := func(from int) []wireFrame {
		mu.Lock()
		defer mu.Unlock()
		var out []wireFrame
		for _, r := range raws[from:] {
			out = append(out, parseWireFrame(r))
		}
		return out
	}
	js := func(expr string) bool {
		var ok bool
		_ = chromedp.Run(p.ctx, chromedp.Evaluate(expr, &ok))
		return ok
	}
	// step runs one interaction, lets its replies (and any viewport report it
	// causes) settle, and records them. act returns "" or why it was skipped.
	step := func(name string, act func() string) {
		from := mark()
		note := act()
		time.Sleep(900 * time.Millisecond)
		steps = append(steps, wireStep{Name: name, Frames: collect(from), Note: note})
	}
	key := func(k string) func() string {
		return func() string {
			_ = chromedp.Run(p.ctx, chromedp.Evaluate(`document.activeElement && document.activeElement.blur && document.activeElement.blur()`, nil))
			ctx, cancel := within(p, 5*time.Second)
			defer cancel()
			if err := chromedp.Run(ctx, chromedp.KeyEvent(k)); err != nil {
				return "key failed: " + err.Error()
			}
			return ""
		}
	}
	button := func(name string) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const b = document.querySelector('button[name=%q]'); if (!b) return false; b.click(); return true })()`, name)) {
				return "no button " + name
			}
			return ""
		}
	}
	// annShow picks a Show row of the open file's Annotations filter.
	annShow := func(value string) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const i = document.querySelector('.ann-filter input[name="show"][value=%q]'); if (!i) return false; i.form.querySelector('button').click(); return true })()`, value)) {
				return "no filter row " + value
			}
			return ""
		}
	}
	buttonIn := func(scope, name string) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const b = document.querySelector(%q + ' button[name=%q]'); if (!b) return false; b.click(); return true })()`, scope, name)) {
				return "no button " + scope + " " + name
			}
			return ""
		}
	}
	// viewBtn clicks a file-header view button; with value, the one whose form
	// carries view=value (Preview | Raw).
	viewBtn := func(action, value string) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const b = [...document.querySelectorAll('.file-head-view button[name=%q]')].find(b => !%q || (b.form.querySelector('input[name="view"]')||{}).value === %q); if (!b || b.disabled) return false; b.click(); return true })()`, action, value, value)) {
				return "no view button " + action + "=" + value
			}
			return ""
		}
	}
	libTab := func(tab string) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const i = document.querySelector('input[name="tab"][value=%q]'); const b = i && i.form && i.form.querySelector('button[name="setLibraryTab"]'); if (!b) return false; b.click(); return true })()`, tab)) {
				return "no library tab " + tab
			}
			return ""
		}
	}
	typeInto := func(sel, text string) func() string {
		return func() string {
			ctx, cancel := within(p, 5*time.Second)
			defer cancel()
			if err := chromedp.Run(ctx, chromedp.Focus(sel, chromedp.ByQuery), chromedp.SendKeys(sel, text, chromedp.ByQuery)); err != nil {
				return "type failed: " + err.Error()
			}
			return ""
		}
	}
	// nthLine clicks the nth rendered code line (in DOM order), which always exists
	// whatever hunks the diff has.
	nthLine := func(n int) func() string {
		return func() string {
			if !js(fmt.Sprintf(`(() => { const l = document.querySelectorAll('.code .line[data-line]')[%d]; if (!l) return false; l.click(); return true })()`, n)) {
				return fmt.Sprintf("no code line #%d", n)
			}
			return ""
		}
	}
	cli := func(args ...string) func() string {
		return func() string {
			cmd := exec.Command(p.binary, args...)
			cmd.Env = prefsIsolatedEnv(p.repo)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Sprintf("cli %v: %v %s", args, err, out)
			}
			return ""
		}
	}
	lastCommentID := func() string {
		out, err := exec.Command(p.binary, "comments", "--json", "--out", p.store).Output()
		if err != nil {
			return ""
		}
		var cs []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(out, &cs) != nil || len(cs) == 0 {
			return ""
		}
		return cs[len(cs)-1].ID
	}

	// --- the walk ---
	step("open big.go", func() string { p.clickFile("big.go"); return "" })
	step("scroll down (viewport report)", func() string {
		if !js(`(() => { const l = document.querySelector('.code [data-line="200"]'); if (!l) return false; l.scrollIntoView(); return true })()`) {
			return "no line 200"
		}
		return ""
	})
	step("scroll back up", func() string {
		js(`(() => { const l = document.querySelector('.code [data-line="1"]'); if (l) l.scrollIntoView(); return true })()`)
		return ""
	})
	step("ArrowDown", key(kb.ArrowDown))
	step("ArrowDown again", key(kb.ArrowDown))
	step("ArrowUp", key(kb.ArrowUp))
	step("click a line", nthLine(5))
	step("click a nearby line (extend)", nthLine(8))
	step("type in the composer", typeInto(`.composer textarea`, "this looks wrong"))
	step("save the comment", buttonIn(".composer", "addComment"))
	step("click another line", nthLine(40))
	step("type a second comment", typeInto(`.composer textarea`, "why is this here"))
	step("save the second comment", buttonIn(".composer", "addComment"))
	step("Escape", key(kb.Escape))
	step("edit a comment", button("editComment"))
	step("Escape the edit", key(kb.Escape))
	step("resolve a comment", button("toggleResolved"))
	step("unresolve it", button("toggleResolved"))
	step("open a reply", button("openReply"))
	step("type a reply", typeInto(`textarea[lvt-on\:input="saveReplyDraft"]`, "agreed"))
	step("post the reply", button("postReply"))
	step("open the file-comment box (c)", key("c"))
	step("close it (Escape)", key(kb.Escape))
	step("agent status working", func() string {
		writeLLMStatusFile(t, p.repo, `{"state":"working","message":"Applying your review"}`)
		return ""
	})
	step("agent status done", func() string {
		writeLLMStatusFile(t, p.repo, `{"state":"done","message":"Fixed the first step"}`)
		return ""
	})
	step("agent replies on a comment", func() string {
		id := lastCommentID()
		if id == "" {
			return "no comment id"
		}
		return cli("reply", "--out", p.store, id, "--body", "Done: tightened it.")()
	})
	step("agent edits big.go on disk", func() string {
		b, err := os.ReadFile(filepath.Join(p.repo, "big.go"))
		if err != nil {
			return err.Error()
		}
		mustWrite(t, p.repo, "big.go", strings.Replace(string(b), "return n + 10\n", "return n + 10 // agent\n", 1))
		return ""
	})
	step("Diff → File view", viewBtn("toggleFileView", ""))
	step("File → Diff view", viewBtn("toggleFileView", ""))
	step("next file (j)", key("j"))
	step("prev file (k)", key("k"))
	step("switch to guide.md", func() string { p.clickFile("guide.md"); return "" })
	step("agent suggests an edit on guide.md", func() string {
		f := filepath.Join(t.TempDir(), "s.json")
		_ = os.WriteFile(f, []byte(`{"id":"s1","file":"guide.md","from_line":3,"to_line":3,"original":"How charges and refunds flow through the service.","proposed":"How charges and refunds move through the service.","note":"plainer verb"}`), 0o644)
		return cli("suggest", "--out", p.store, "--file", f)()
	})
	step("click a Markdown block", func() string {
		if !js(`(() => { const b = document.querySelectorAll('.md-view [data-from][lvt-on\\:click]')[6]; if (!b) return false; b.click(); return true })()`) {
			return "no clickable Markdown block"
		}
		return ""
	})
	step("type a block comment", typeInto(`.composer textarea`, "tighten this"))
	step("save the block comment", buttonIn(".composer", "addComment"))
	step("Preview → Raw", viewBtn("setMarkdownView", "raw"))
	step("Raw → Preview", viewBtn("setMarkdownView", "rendered"))
	step("switch to small.go", func() string { p.clickFile("small.go"); return "" })
	step("switch back to big.go", func() string { p.clickFile("big.go"); return "" })
	step("agent inserts 6 lines mid-file in big.go", func() string {
		b, err := os.ReadFile(filepath.Join(p.repo, "big.go"))
		if err != nil {
			return err.Error()
		}
		block := "\t// added by the agent\n\t_ = 1\n\t_ = 2\n\t_ = 3\n\t_ = 4\n\t_ = 5\n"
		mustWrite(t, p.repo, "big.go", strings.Replace(string(b), "func Step3(n int) int {\n", "func Step3(n int) int {\n"+block, 1))
		return ""
	})
	step("hide whitespace on", button("toggleHideWhitespace"))
	step("hide whitespace off", button("toggleHideWhitespace"))
	step("hide annotations", annShow("none"))
	step("show annotations", annShow("open"))
	step("cycle theme mode", button("cycleTheme"))
	step("cycle scheme", button("cycleScheme"))
	step("toggle file drawer", button("toggleFiles"))
	step("toggle file drawer back", button("toggleFiles"))
	step("open search", button("openSearch"))
	step("type a search query", typeInto(`input[name="q"]`, "Step4"))
	step("close search", button("closeSearch"))
	step("keyboard help on (?)", key("?"))
	step("keyboard help off (?)", key("?"))
	step("open Queue dropdown (client-only)", func() string {
		js(`(() => { const b = document.querySelector('.queue-trigger'); if (b) b.click(); return true })()`)
		return ""
	})
	step("close Queue dropdown", func() string { js(`document.body.click(), true`); return "" })
	step("all comments on", button("toggleCommentList"))
	step("all comments off", button("toggleCommentList"))
	step("open Library", button("toggleLibrary"))
	step("Library: shortcuts tab", libTab("shortcuts"))
	step("Library: skills tab", libTab("skills"))
	step("Library: checks tab", libTab("checks"))
	step("Library: learnings tab", libTab("learnings"))
	step("close Library", button("toggleLibrary"))
	step("open Understand", button("toggleUnderstand"))
	step("close Understand", button("toggleUnderstand"))
	step("delete a comment", button("deleteComment"))

	// --- report ---
	var b strings.Builder
	fmt.Fprintf(&b, "\n%-40s %8s %8s  %s\n", "interaction", "bytes", "deflated", "frames (action:size [s=statics d=range-items ops])")
	var suspicious []string
	for _, st := range steps {
		var parts []string
		for _, f := range st.Frames {
			parts = append(parts, fmt.Sprintf("%s:%d[s%d d%d o%d]", f.Action, f.Size, f.Statics, f.RangeItems, f.Ops))
			if f.Size > 4096 || f.RangeItems > 20 {
				suspicious = append(suspicious, fmt.Sprintf("%s → %s %d bytes (s%d d%d)", st.Name, f.Action, f.Size, f.Statics, f.RangeItems))
			}
		}
		note := ""
		if st.Note != "" {
			note = "  (" + st.Note + ")"
		}
		sz := st.sizes()
		fmt.Fprintf(&b, "%-40s %8d %8d  %s%s\n", st.Name, sz.Raw, sz.Deflated, strings.Join(parts, " "), note)
	}
	fmt.Fprintf(&b, "\nSUSPICIOUS (>4 KB or a range sent with >20 items):\n  %s\n", strings.Join(suspicious, "\n  "))
	t.Log(b.String())

	if dir := os.Getenv("WIRE_AUDIT_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		if j, err := json.MarshalIndent(steps, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(dir, "report.json"), j, 0o644)
		}
		for i, st := range steps {
			for k, f := range st.Frames {
				if f.Size > 4096 {
					name := fmt.Sprintf("%02d-%d-%s.json", i, k, strings.NewReplacer(" ", "_", "/", "_", "(", "", ")", "", "→", "to", "?", "q", ":", "").Replace(st.Name))
					_ = os.WriteFile(filepath.Join(dir, name), []byte(f.raw), 0o644)
				}
			}
		}
	}
	checkWireBudgets(t, steps, diag)
}

// checkWireBudgets fails any step that was skipped or grew past its budget, or
// rewrites the budgets file under -update-wire-budgets.
func checkWireBudgets(t *testing.T, steps []wireStep, diag func() string) {
	t.Helper()
	measured := map[string]wireSizes{}
	for _, st := range steps {
		if st.Note != "" {
			t.Errorf("step %q did not run: %s%s", st.Name, st.Note, diag())
		}
		if _, dup := measured[st.Name]; dup {
			t.Fatalf("two steps are named %q; budgets are keyed by name", st.Name)
		}
		measured[st.Name] = st.sizes()
	}
	if *updateWireBudgets {
		j, err := json.MarshalIndent(measured, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wireBudgetsPath, append(j, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(wireBudgetsPath)
	if err != nil {
		t.Fatalf("read %s: %v (create it with -update-wire-budgets)", wireBudgetsPath, err)
	}
	var budgets map[string]wireSizes
	if err := json.Unmarshal(raw, &budgets); err != nil {
		t.Fatalf("parse %s: %v", wireBudgetsPath, err)
	}
	for _, st := range steps {
		got := measured[st.Name]
		want, ok := budgets[st.Name]
		if !ok {
			t.Errorf("step %q has no budget; regenerate with -update-wire-budgets", st.Name)
			continue
		}
		if limit := wireBudget(want.Raw); got.Raw > limit {
			t.Errorf("%q sent %d raw bytes, over its budget of %d (measured %d)", st.Name, got.Raw, limit, want.Raw)
		}
		if limit := wireBudget(want.Deflated); got.Deflated > limit {
			t.Errorf("%q sent %d deflated bytes, over its budget of %d (measured %d)", st.Name, got.Deflated, limit, want.Deflated)
		}
	}
	for _, name := range wireCapped {
		got, ok := measured[name]
		if !ok {
			t.Errorf("capped step %q is not in the walk", name)
		} else if got.Raw > wireCap {
			t.Errorf("%q sent %d raw bytes, over its cap of %d", name, got.Raw, wireCap)
		}
	}
}

// deflater is reused across frames: a flate writer allocates large tables.
// parseWireFrame only runs under the walk's frame lock, so one is enough.
var deflater, _ = flate.NewWriter(io.Discard, flate.BestSpeed)

// deflatedSize is raw compressed on its own at flate level 1.
func deflatedSize(raw string) int {
	var b bytes.Buffer
	deflater.Reset(&b)
	_, _ = deflater.Write([]byte(raw))
	_ = deflater.Close()
	return b.Len()
}

// parseWireFrame reads a reply frame's action, size, statics count, the largest
// whole-range "d" array, and the number of range-op tuples.
func parseWireFrame(raw string) wireFrame {
	f := wireFrame{Size: len(raw), Deflated: deflatedSize(raw), raw: raw}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return f
	}
	if meta, ok := m["meta"].(map[string]any); ok {
		f.Action, _ = meta["action"].(string)
	}
	if f.Action == "" {
		f.Action = "(push)"
	}
	tree, ok := m["tree"]
	if !ok {
		tree = m
	}
	var walk func(v any)
	walk = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			for k, c := range val {
				switch {
				case k == "s":
					f.Statics++
				case k == "d":
					if arr, ok := c.([]any); ok && len(arr) > f.RangeItems {
						f.RangeItems = len(arr)
					}
				}
				walk(c)
			}
		case []any:
			for _, e := range val {
				if op, ok := e.([]any); ok && len(op) > 0 {
					if s, ok := op[0].(string); ok && len(s) == 1 {
						f.Ops++
					}
				}
				walk(e)
			}
		}
	}
	walk(tree)
	return f
}
