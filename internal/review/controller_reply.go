package review

import (
	"strings"
	"time"

	"github.com/livetemplate/livetemplate"
)

// controller_reply.go — the reviewer→agent side of #149 threads: the reviewer posts a
// reply under a comment/suggestion card. It appends a reviewer ThreadEntry and re-emits
// the snapshot so the agent's `watch` picks it up. The inline form is armed by ReplyingID
// / ReplyDraft (mirrors EditingCommentID for the comment edit-form).

// OpenReply opens the inline reply form on the given comment/suggestion, draft empty.
func (c *PrereviewController) OpenReply(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ReplyingID = ctx.GetString("id")
	state.ReplyDraft = ""
	return state, nil
}

// SaveReplyDraft autosaves the reply textarea so it survives a reconnect.
func (c *PrereviewController) SaveReplyDraft(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ReplyDraft = ctx.GetString("body")
	return state, nil
}

// CancelReply closes the reply form without posting.
func (c *PrereviewController) CancelReply(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	state.ReplyingID = ""
	state.ReplyDraft = ""
	return state, nil
}

// PostReply records the reviewer's reply on a comment/suggestion (#149): appends a
// reviewer ThreadEntry to the server-owned reviewer-replies.jsonl, reloads the threads
// so the card shows it immediately, and re-arms the snapshot emit so the agent's watch
// sees it. It does NOT touch Resolved — a reply is a steer, not a reopen; the
// unread-reply overlay (threadActionable) re-surfaces a resolved comment on the wire.
func (c *PrereviewController) PostReply(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	id := ctx.GetString("id")
	body := strings.TrimSpace(ctx.GetString("body"))
	if id == "" {
		return state, nil
	}
	if body == "" {
		state.ReplyingID = id // keep the form open on an empty submit
		state.ReplyDraft = ctx.GetString("body")
		return state, nil
	}
	if err := appendReviewerReply(c.CSVPath, id, body, time.Now().UnixNano()); err != nil {
		return state, err
	}
	state.ThreadEntries = loadThreads(c.CSVPath)
	state.ReplyingID = ""
	state.ReplyDraft = ""
	c.scheduleEmit()
	return state, nil
}
