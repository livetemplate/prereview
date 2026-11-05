package review

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/livetemplate/livetemplate"
)

// #212: one file-head question (kind=file renders at the head, keyed FileHeadRowKey).
func headQuizState() PrereviewState {
	return PrereviewState{
		SelectedFile: "a.go",
		Quizzes: []Quiz{{ID: "z1", File: "a.go", Questions: []Question{
			{ID: "q1", Kind: QuestionKindFile, Options: []string{"a", "b"}, Answer: 1},
		}}},
	}
}

func answerCtx(quizID, questionID, choice string) *livetemplate.Context {
	return livetemplate.NewContext(context.TODO(), "answerQuestion",
		map[string]interface{}{"quizId": quizID, "questionId": questionID, "choice": choice})
}

// A question answered in an earlier visit is settled (folded by default); one
// answered this visit is not, so its explanation stays in front of the reviewer.
func TestQuizItems_SettledOnlyWhenAnsweredBeforeThisVisit(t *testing.T) {
	st := headQuizState()
	if st.QuizItems()[0].Settled {
		t.Fatal("an unanswered question is open work, never settled")
	}
	st.QuizAnswers = map[string]QuizAnswer{answerKey("z1", "q1"): {QuizID: "z1", QuestionID: "q1", Choice: 1}}
	if !st.QuizItems()[0].Settled {
		t.Error("answered before this visit ⇒ settled (collapsed by default)")
	}
	st.QuizJustAnswered = map[string]bool{answerKey("z1", "q1"): true}
	if st.QuizItems()[0].Settled {
		t.Error("answered this visit ⇒ not settled, so the explanation stays visible")
	}
}

// A finished quiz reopens without its navigator, but answering the last question
// doesn't pull the bar away mid-visit.
func TestShowQuizNav_HiddenOnceTakenUnlessAnsweredThisVisit(t *testing.T) {
	st := headQuizState()
	if !st.ShowQuizNav() {
		t.Fatal("an untaken quiz shows its navigator")
	}
	st.QuizAnswers = map[string]QuizAnswer{answerKey("z1", "q1"): {QuizID: "z1", QuestionID: "q1", Choice: 0}}
	if st.ShowQuizNav() {
		t.Error("a quiz taken in an earlier visit must not bring its navigator back (#212)")
	}
	st.QuizJustAnswered = map[string]bool{answerKey("z1", "q1"): true}
	if !st.ShowQuizNav() {
		t.Error("finishing the quiz this visit must not make the navigator vanish")
	}

	// "Answered this visit" is per quiz: answering in another file must not bring
	// back this file's finished quiz navigator.
	st.QuizJustAnswered = map[string]bool{answerKey("z2", "q1"): true}
	if st.ShowQuizNav() {
		t.Error("an answer in a different file's quiz must not resurrect this navigator")
	}
}

// The file head has no line, so rowState must ask the head group directly — and
// a just-answered head question keeps it open, so a badge click there records
// "hide" and expires on reload instead of flipping into a reveal.
func TestRowState_FileHeadTracksOpenAndJustAnswered(t *testing.T) {
	st := headQuizState()
	if got := st.rowState(FileHeadRowKey); got != rowStateOpen {
		t.Errorf("an unanswered head question ⇒ head open, got %q", got)
	}
	st.QuizAnswers = map[string]QuizAnswer{answerKey("z1", "q1"): {QuizID: "z1", QuestionID: "q1", Choice: 1}}
	if got := st.rowState(FileHeadRowKey); got != rowStateCollapsed {
		t.Errorf("all head work done before this visit ⇒ collapsed, got %q", got)
	}
	st.QuizJustAnswered = map[string]bool{answerKey("z1", "q1"): true}
	if got := st.rowState(FileHeadRowKey); got != rowStateOpen {
		t.Errorf("a head question answered this visit is still showing ⇒ open, got %q", got)
	}

	// A "hide" recorded now expires once the visit ends (reload drops QuizJustAnswered).
	st.ToggledRows = map[string]string{FileHeadRowKey: rowStateOpen}
	if !st.RowToggled()[FileHeadRowKey] {
		t.Fatal("the toggle is live during the visit")
	}
	st.QuizJustAnswered = nil
	if st.RowToggled()[FileHeadRowKey] {
		t.Error("after a reload the stale 'hide' must expire, not flip into a reveal")
	}
}

func TestAnswerAndRetake_TrackJustAnswered(t *testing.T) {
	c := &PrereviewController{CSVPath: filepath.Join(t.TempDir(), "comments.csv")}
	st, err := c.AnswerQuestion(headQuizState(), answerCtx("z1", "q1", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if !st.QuizJustAnswered[answerKey("z1", "q1")] {
		t.Fatal("answering must mark the question as answered this visit")
	}
	st, err = c.RetakeQuiz(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.QuizJustAnswered) != 0 {
		t.Errorf("a retake starts the quiz over, so nothing is 'just answered', got %v", st.QuizJustAnswered)
	}
}
