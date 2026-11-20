package library

// AgentText is a kind of text the agent writes into a review: its thread
// replies, its suggested edits and its quiz questions. Lessons about it take
// Scope, so they get a skill of their own from the first one (never landing in
// the Writing skill every doc uses), and checks see the text as File, so a
// check's `files:` picks one kind.
type AgentText struct {
	Kind  string // the snapshot's write_with key
	Scope string
	File  string
	Noun  string // what the agent wrote, as its findings name it: "your <Noun>"
	Use   string // when an agent should load the kind's skill
}

// The kinds of agent text.
var (
	ReplyText      = AgentText{Kind: "reply", Scope: "Agent replies", File: "reply.md", Noun: "reply", Use: "the thread replies you post with `prereview reply`"}
	SuggestionText = AgentText{Kind: "suggestion", Scope: "Suggestions", File: "suggestion.md", Noun: "suggestions", Use: "the suggested edits you propose with `prereview suggest`"}
	QuizText       = AgentText{Kind: "quiz", Scope: "Quiz questions", File: "quiz.md", Noun: "quiz questions", Use: "the quiz questions you write with `prereview quiz`"}
)

// AgentTexts are every kind, in the order the prompt and the docs list them.
var AgentTexts = []AgentText{ReplyText, SuggestionText, QuizText}

// AgentTextFor is the kind a lesson scoped to scope is about, if any.
func AgentTextFor(scope string) (AgentText, bool) {
	for _, a := range AgentTexts {
		if a.Scope == scope {
			return a, true
		}
	}
	return AgentText{}, false
}
