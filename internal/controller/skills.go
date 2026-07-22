package controller

import (
	"fmt"

	"github.com/aleksanaa/hyphae/internal/agent"
)

// Skills returns the global skills discovered at startup.
func (c *Controller) Skills() []agent.Skill { return c.skills }

// QueueSkill force-loads the named skill's full body onto the active session, so
// it rides the next user message (via the one-shot reminder queue). Returns an
// error if the skill is unknown, its file cannot be read, or there is no active
// session.
func (c *Controller) QueueSkill(name string) error {
	var found *agent.Skill
	for i := range c.skills {
		if c.skills[i].Name == name {
			found = &c.skills[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("unknown skill %q", name)
	}
	body, err := agent.ReadSkillBody(found.Path)
	if err != nil {
		return fmt.Errorf("read skill: %w", err)
	}
	sess, ok := c.mgr.Active()
	if !ok {
		return fmt.Errorf("no active session")
	}
	sess.AddReminder(agent.SkillReminder(*found, body))
	return nil
}
