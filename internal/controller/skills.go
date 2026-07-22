package controller

import (
	"fmt"

	"github.com/aleksanaa/hyphae/internal/agent"
)

// Skills returns the global skills discovered at startup.
func (c *Controller) Skills() []agent.Skill { return c.skills }

// ActiveSkills returns the set of skill names currently loaded on the active
// session (in-memory, process lifetime). Used by the palette to show which
// skills are loaded and to decide whether a click loads or unloads.
func (c *Controller) ActiveSkills() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.activeSkills[c.mgr.ActiveID()]
	out := make(map[string]bool, len(set))
	for name := range set {
		out[name] = true
	}
	return out
}

// LoadSkill force-loads the named skill's full body onto the active session, so
// it rides the next user message (via the one-shot reminder queue), and marks it
// active. Returns an error if the skill is unknown, its file cannot be read, or
// there is no active session.
func (c *Controller) LoadSkill(name string) error {
	found, err := c.skillByName(name)
	if err != nil {
		return err
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
	c.markSkill(sess.ID, name, true)
	return nil
}

// UnloadSkill marks the named skill inactive. If its load reminder is still
// pending (never sent to the model), it is simply cancelled — no notice is
// needed. Otherwise the skill was already communicated in a prior turn, so a
// one-shot "unloaded, do not use it" reminder rides the next user message.
func (c *Controller) UnloadSkill(name string) error {
	found, err := c.skillByName(name)
	if err != nil {
		return err
	}
	sess, ok := c.mgr.Active()
	if !ok {
		return fmt.Errorf("no active session")
	}
	c.markSkill(sess.ID, name, false)

	// If the load body is still queued, drop it: the model never saw the skill.
	if body, err := agent.ReadSkillBody(found.Path); err == nil {
		if sess.RemoveReminder(agent.SkillReminder(*found, body)) {
			return nil
		}
	}
	sess.AddReminder(agent.SkillUnloadReminder(*found))
	return nil
}

func (c *Controller) skillByName(name string) (*agent.Skill, error) {
	for i := range c.skills {
		if c.skills[i].Name == name {
			return &c.skills[i], nil
		}
	}
	return nil, fmt.Errorf("unknown skill %q", name)
}

func (c *Controller) markSkill(sessionID, name string, active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if active {
		if c.activeSkills[sessionID] == nil {
			c.activeSkills[sessionID] = make(map[string]bool)
		}
		c.activeSkills[sessionID][name] = true
	} else {
		delete(c.activeSkills[sessionID], name)
	}
}
