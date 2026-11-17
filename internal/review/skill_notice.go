package review

import (
	"github.com/livetemplate/livetemplate"
	"github.com/livetemplate/prereview/internal/skills"
)

// SkillReload reports whether this launch refreshed any agent's prereview
// skill, so the notice asks you to reload it in that agent.
func (s PrereviewState) SkillReload() bool { return skills.AnyRefreshed(s.SkillNotice, "") }

// DismissSkillNotice hides the #208 skill notice for the rest of this launch,
// on every tab that mounts after it.
func (c *PrereviewController) DismissSkillNotice(state PrereviewState, ctx *livetemplate.Context) (PrereviewState, error) {
	c.skillNoticeOff.Store(true)
	state.SkillNotice = nil
	return state, nil
}
