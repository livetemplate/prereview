package review

import (
	"fmt"
	"sort"

	"github.com/livetemplate/prereview/csv"
)

// state_quiz.go holds the quiz view's read helpers.
//
// Every one is ZERO-ARG on purpose: livetemplate pre-computes zero-arg state
// methods, and a method that takes an argument silently breaks rendering rather
// than failing loudly. So instead of `{{$.QuizItem .ID}}` the template ranges
// over a prepared slice — QuizItems() carries everything a question row needs.

// QuizItem is one question plus the reviewer's progress on it: a flattened view
// row, so the template needs no lookups and no per-row logic.
type QuizItem struct {
	Question
	// QuizID is carried on the row so the card partial is SELF-CONTAINED: it can
	// build its answer form without reaching for root state, which keeps it
	// renderable from the diff view, the file head and the overview alike.
	QuizID string
	// Num is the question's 1-based position, for the navigator badge. (An earlier
	// Index field was removed as dead code; the navigator gave it a purpose.)
	Num int
	// ThreadID is this question's global identity for the #149 conversation
	// machinery, and Thread the conversation so far. Carried on the row so the card
	// partial stays self-contained.
	ThreadID   string
	Thread     []ThreadEntry
	Replying   bool
	ReplyDraft string
	Answered   bool
	Choice     int // the option the reviewer picked; meaningless unless Answered
	Correct    bool
	// Settled: answered, but not in this visit — collapsed by default behind the
	// row's green badge, exactly like a resolved comment (#212).
	Settled bool
}

// HasQuiz reports the review has quiz questions to show (#115 A′5: the quiz is
// on the whole change, not the open file) — the gate for offering it at all.
func (s PrereviewState) HasQuiz() bool {
	for _, q := range s.Quizzes {
		for _, qu := range q.Questions {
			if s.inScope(q.fileOf(qu)) {
				return true
			}
		}
	}
	return false
}

// allQuizItems is every question the review shows, across files, with the
// reviewer's progress folded in. A file shows the questions of the LAST quiz in
// quiz.jsonl that asks about it (a re-appended quiz counts as the newest), so a
// quiz on the whole change and an older per-file one never both show for a file.
// Ordered and numbered like the drawer: by file, then the file head, then line.
// It is the one list the Quiz tab, the inline cards, the stepper and Retake read.
// The conversation is attached only by the views that render it (withThreads).
func (s PrereviewState) allQuizItems() []QuizItem {
	owner := map[string]int{} // file -> index of the quiz whose questions it shows
	for i, q := range s.Quizzes {
		for _, qu := range q.Questions {
			if f := q.fileOf(qu); s.inScope(f) {
				owner[f] = i
			}
		}
	}
	if len(owner) == 0 {
		return nil
	}
	rankOf := s.fileRank()
	var out []QuizItem
	for i, q := range s.Quizzes {
		for _, qu := range q.Questions {
			f := q.fileOf(qu)
			if !s.inScope(f) || owner[f] != i {
				continue
			}
			qu.File = f
			item := QuizItem{Question: qu, QuizID: q.ID, ThreadID: QuizThreadID(q.ID, qu.ID)}
			if a, ok := s.QuizAnswers[answerKey(q.ID, qu.ID)]; ok {
				item.Answered = true
				item.Choice = a.Choice
				item.Correct = a.Choice == qu.Answer
				item.Settled = !s.QuizJustAnswered[answerKey(q.ID, qu.ID)]
			}
			out = append(out, item)
		}
	}
	// Number by DOCUMENT position, not the agent's JSON order, so the navigator
	// badges read 1..N top to bottom: file by file, head questions (which render
	// above the diff) first, then line-anchored ones by line.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rankOf(a.File), rankOf(b.File); ra != rb {
			return ra < rb
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if ha, hb := !a.LineGrounded(), !b.LineGrounded(); ha != hb || ha {
			return ha && !hb // head questions first; two head questions keep their order
		}
		return a.FromLine < b.FromLine
	})
	for i := range out {
		out[i].Num = i + 1
	}
	return out
}

// withThreads attaches each question's conversation, and the open reply form,
// for a view that renders the cards.
func (s PrereviewState) withThreads(items []QuizItem) []QuizItem {
	if len(items) == 0 {
		return items
	}
	threads := s.Threads()
	for i := range items {
		items[i].Thread = threads[items[i].ThreadID]
		if s.ReplyingID == items[i].ThreadID {
			items[i].Replying, items[i].ReplyDraft = true, s.ReplyDraft
		}
	}
	return items
}

// QuizItems is the open file's questions: the ones its diff renders inline.
func (s PrereviewState) QuizItems() []QuizItem {
	if s.SelectedFile == "" {
		return nil
	}
	var out []QuizItem
	for _, it := range s.allQuizItems() {
		if it.File == s.SelectedFile {
			out = append(out, it)
		}
	}
	return s.withThreads(out)
}

// QuizFileGroup is one file's questions on the Quiz tab.
type QuizFileGroup struct {
	File  string
	Items []QuizItem
}

// QuizFiles is the Quiz tab's questions, grouped by file in drawer order. Only
// while the tab is on screen: livetemplate computes it on every render.
func (s PrereviewState) QuizFiles() []QuizFileGroup {
	if !s.ShowUnderstand || s.UnderstandTabOn() != understandTabQuiz {
		return nil
	}
	var out []QuizFileGroup
	for _, it := range s.withThreads(s.allQuizItems()) {
		if n := len(out); n == 0 || out[n-1].File != it.File {
			out = append(out, QuizFileGroup{File: it.File})
		}
		out[len(out)-1].Items = append(out[len(out)-1].Items, it)
	}
	return out
}

// quizTally counts the questions the review shows (allQuizItems' rule) and the
// reviewer's answers, in one pass with nothing built or sorted: the score shows
// in menus on every render.
func (s PrereviewState) quizTally() (total, answered, correct int) {
	owner := map[string]int{}
	for i, q := range s.Quizzes {
		for _, qu := range q.Questions {
			if f := q.fileOf(qu); s.inScope(f) {
				owner[f] = i
			}
		}
	}
	for i, q := range s.Quizzes {
		for _, qu := range q.Questions {
			if f := q.fileOf(qu); !s.inScope(f) || owner[f] != i {
				continue
			}
			total++
			if a, ok := s.QuizAnswers[answerKey(q.ID, qu.ID)]; ok {
				answered++
				if a.Choice == qu.Answer {
					correct++
				}
			}
		}
	}
	return total, answered, correct
}

// QuizAnsweredCount is how many questions have been answered.
func (s PrereviewState) QuizAnsweredCount() int {
	_, answered, _ := s.quizTally()
	return answered
}

// QuizComplete reports that every question has been answered.
func (s PrereviewState) QuizComplete() bool {
	total, answered, _ := s.quizTally()
	return total > 0 && answered == total
}

// QuizScoreLabel renders the running score for the quiz header, e.g. "2/5".
func (s PrereviewState) QuizScoreLabel() string {
	total, _, correct := s.quizTally()
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", correct, total)
}

// AnchorLabel is the badge for a question's line reference: "L42", "L42-L48", or
// empty when the question is deliberately about something absent.
func (q Question) AnchorLabel() string {
	if q.Anchorless() {
		return ""
	}
	if q.ToLine > q.FromLine {
		return fmt.Sprintf("L%d-L%d", q.FromLine, q.ToLine)
	}
	return fmt.Sprintf("L%d", q.FromLine)
}

// Jumpable reports whether this question offers a "jump to the code" link — only
// when it both claims an anchor AND that anchor resolved in its file.
// An ungrounded question must never offer a jump: the link would land on code
// that has nothing to do with the question.
func (q Question) Jumpable() bool {
	return !q.Anchorless() && q.AnchorStatus == quizAnchorOK
}

// QuizResults reports every quiz's outcome for the agent snapshot (#191).
//
// It is ADVISORY: it never gates a verdict, it just lets the agent tell "accepted
// after a comprehension check" from "accepted without one". That distinction is
// the whole point — an accept records that the reviewer clicked, and this is the
// only signal that says whether they also understood.
//
// It reports every quiz, replaced or not (an older quiz's answers still count),
// since the snapshot is not scoped to whatever file happens to be open — but it IS scoped
// to the review (inScope), the same as scopedComments and scopedSuggestions. Without
// that, a single-file review sharing a pre-#199 store reported the sibling file's
// quizzes as this file's comprehension record.
func (s PrereviewState) QuizResults() []StreamQuiz {
	var out []StreamQuiz
	threads := s.Threads()
	for _, q := range s.Quizzes {
		// File is "" for a quiz on the whole change; Total and Score count only the
		// questions in the review's scope.
		r := StreamQuiz{File: q.File, QuizID: q.ID}
		for _, qu := range q.Questions {
			if !s.inScope(q.fileOf(qu)) {
				continue
			}
			r.Total++
			if a, ok := s.QuizAnswers[answerKey(q.ID, qu.ID)]; ok {
				r.Taken = true
				if a.Choice == qu.Answer {
					r.Score++
				}
				if a.Choice >= 0 && a.Choice < len(qu.Options) {
					r.Answers = append(r.Answers, StreamQuizAnswer{Question: qu.ID, Picked: qu.Options[a.Choice], Correct: a.Choice == qu.Answer})
				}
			}
			if th := threads[QuizThreadID(q.ID, qu.ID)]; hasUnreadReviewerReply(th) {
				r.Threads = append(r.Threads, StreamQuizThread{Question: qu.ID, Thread: toStreamThread(th)})
			}
		}
		if r.Total > 0 {
			out = append(out, r)
		}
	}
	return out
}

// QuizPending: a quiz request is still open in the queue and no quiz has arrived
// since it was sent, so the control reads "Quiz requested…" instead of inviting a
// second tap. Derived from the request row, as ExplainPending is.
func (s PrereviewState) QuizPending() bool {
	asked := s.openRequest(csv.RequestQuiz, "")
	if asked.IsZero() {
		return false
	}
	n := len(s.Quizzes)
	return n == 0 || !answeredSince(s.Quizzes[n-1].CreatedAt, asked)
}

// QuizByEndLine groups the open file's LINE-anchored questions by the line
// they end on, so the diff view can render each one inline right under the code
// it asks about — the same shape CommentsByEndLine and SuggestionsByEndLine use.
//
// This is what makes a quiz question just another annotation. The first version
// put the quiz on its own screen and then had to reproduce the cited code inside
// it, which was a worse re-implementation of the diff view; anchoring the
// question to the line makes the code its own context.
func (s PrereviewState) QuizByEndLine() map[int][]QuizItem {
	if s.SelectedFile == "" {
		return nil
	}
	out := map[int][]QuizItem{}
	for _, it := range s.QuizItems() {
		// Only questions with a resolved line have a row to render under;
		// unanchored and ungrounded ones go to the file head (FileQuizItems).
		if !it.LineGrounded() {
			continue
		}
		end := it.EndLine()
		out[end] = append(out[end], it)
	}
	return out
}

// BlockQuizItems is the open file's line-grounded quiz questions grouped by
// rendered block (Markdown or the HTML preview), like BlockComments, so a quiz
// question renders under its block there too.
func (s PrereviewState) BlockQuizItems() map[string][]QuizItem {
	var items []QuizItem
	for _, it := range s.QuizItems() {
		if it.LineGrounded() {
			items = append(items, it)
		}
	}
	return groupByBlock(s, items, func(it QuizItem) int { return it.EndLine() })
}

// FileQuizItems are the questions that have no valid position inside the diff, so
// they render at the file head — exactly where a kind=file COMMENT renders.
//
// Two groups land here, for different reasons:
//
//   - kind=file: about the change as a whole, or about something absent from it.
//     It never had a line, by design.
//   - UNGROUNDED: it claims a line the diff does not contain, so there is nowhere
//     to anchor it. These MUST still render. Anchoring questions inline means an
//     unresolvable anchor has no home, and quietly dropping it would HIDE a
//     hallucinated question instead of surfacing it — undoing the entire point of
//     the grounding check. The reviewer needs to see that the agent asked about
//     code that isn't there.
func (s PrereviewState) FileQuizItems() []QuizItem {
	var out []QuizItem
	for _, it := range s.QuizItems() {
		// Exactly the complement of QuizByEndLine's inline set: anything not
		// line-grounded renders here. For v1 that is precisely kind=file and
		// ungrounded questions; stating it as !LineGrounded() keeps this the exact
		// inverse of the inline predicate, so a future area/region question can
		// never fall through the gap between the two and render nowhere.
		if !it.LineGrounded() {
			out = append(out, it)
		}
	}
	return out
}

// QuizCountLines counts the quiz questions anchored to each row, keyed
// "<line>-<side>" exactly like CommentCountLines — so a question contributes to
// the row's annotation badge and can be collapsed with everything else on that
// line. A quiz question is an annotation; it collapses like one.
func (s PrereviewState) QuizCountLines() map[string]int {
	out := map[string]int{}
	for end, items := range s.QuizByEndLine() {
		for _, it := range items {
			countRowSides(out, end, it.Side)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// QuizOpenLines marks rows carrying an UNANSWERED question — open work, so the
// badge reads open (yellow) rather than done, matching an unresolved comment.
func (s PrereviewState) QuizOpenLines() map[string]bool {
	return s.quizLinesWhere(func(it QuizItem) bool { return !it.Answered })
}

// quizShowingLines marks rows with a quiz card showing by default: unanswered, or
// answered THIS visit (#212). That is what rowState means by "open" — a badge click
// there records "hide", and after a reload (when the row's default becomes
// collapsed) the toggle expires instead of flipping into a reveal. The badge
// COLOUR uses QuizOpenLines instead: an answered question is done.
func (s PrereviewState) quizShowingLines() map[string]bool {
	return s.quizLinesWhere(func(it QuizItem) bool { return !it.Settled })
}

// quizLinesWhere keys every inline question matching pred to its row(s).
func (s PrereviewState) quizLinesWhere(pred func(QuizItem) bool) map[string]bool {
	out := map[string]bool{}
	for end, items := range s.QuizByEndLine() {
		for _, it := range items {
			if pred(it) {
				for _, k := range rowKeysFor(end, it.Side) {
					out[k] = true
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fileHeadQuizShowing: a file-head question card is showing by default (see
// quizShowingLines) — so for rowState the head group is open.
func (s PrereviewState) fileHeadQuizShowing() bool {
	for _, it := range s.FileQuizItems() {
		if !it.Settled {
			return true
		}
	}
	return false
}

// FileHeadAnnotationCount is how many annotations sit at the file head — the
// number the shared row badge shows there. Counts file-level and area comments
// alongside the head quiz questions, because they all collapse together.
func (s PrereviewState) FileHeadAnnotationCount() int {
	return len(s.FileLevelComments()) + len(s.AreaComments()) + len(s.FileQuizItems()) + len(s.FileQuestionCards())
}

// FileHeadHasOpen reports whether anything at the file head is still open work,
// which is what makes the shared badge read "open" rather than "done".
func (s PrereviewState) FileHeadHasOpen() bool {
	for _, c := range s.FileLevelComments() {
		if !c.Resolved {
			return true
		}
	}
	for _, c := range s.AreaComments() {
		if !c.Resolved {
			return true
		}
	}
	for _, it := range s.FileQuizItems() {
		if !it.Answered {
			return true
		}
	}
	for _, c := range s.FileQuestionCards() {
		if !c.Resolved {
			return true
		}
	}
	return false
}
