package controller

import (
	"encoding/json"

	"github.com/aleksanaa/hyphae/internal/agent"
)

// encodePermissions serializes a session's grants for storage. Empty grants
// serialize to "" (not "null") so the DB column stays clean.
func encodePermissions(grants []agent.Grant) string {
	if len(grants) == 0 {
		return ""
	}
	b, err := json.Marshal(grants)
	if err != nil {
		return ""
	}
	return string(b)
}

// decodePermissions parses a stored permissions blob back into grants. A blank
// or malformed value yields no grants.
func decodePermissions(s string) []agent.Grant {
	if s == "" {
		return nil
	}
	var grants []agent.Grant
	if err := json.Unmarshal([]byte(s), &grants); err != nil {
		return nil
	}
	return grants
}

// Permissions returns the access grants of the active session, for display in the
// palette. Nil when there is no active session.
func (c *Controller) Permissions() []agent.Grant {
	id := c.mgr.ActiveID()
	if id == "" {
		return nil
	}
	return c.agentFor(id).Grants()
}

// AddPermission grants a permission on the active session directly (the user is
// the authority, so no approval prompt) and persists it. gtype is "readonly",
// "readwrite", or "web_fetch"; path is a directory (readonly/readwrite) or URL
// prefix (web_fetch). It returns the normalized scope that was stored.
func (c *Controller) AddPermission(gtype, path string) string {
	id := c.mgr.ActiveID()
	if id == "" {
		return ""
	}
	workDir := ""
	if sess, ok := c.mgr.Get(id); ok {
		workDir = sess.WorkDir
	}
	ag := c.agentFor(id)
	scope := ag.AddGrant(gtype, path, workDir)
	if c.st != nil {
		c.st.UpdateSessionPermissions(id, encodePermissions(ag.Grants())) //nolint:errcheck
	}
	return scope
}

// RevokePermission removes one grant from the active session and persists the
// change. gtype/scope identify the grant exactly (as returned by Permissions).
func (c *Controller) RevokePermission(gtype, scope string) {
	id := c.mgr.ActiveID()
	if id == "" {
		return
	}
	ag := c.agentFor(id)
	if !ag.RevokeGrant(agent.Grant{Type: gtype, Scope: scope}) {
		return
	}
	if c.st != nil {
		c.st.UpdateSessionPermissions(id, encodePermissions(ag.Grants())) //nolint:errcheck
	}
	// Tell the model on its next turn that this access is gone.
	if sess, ok := c.mgr.Get(id); ok {
		sess.AddReminder(agent.PermissionRevokedReminder(gtype, scope))
	}
}

// persistPermissions writes the current grants of a session's agent to storage.
// Called at turn end so grants added via request_access survive a restart.
func (c *Controller) persistPermissions(sessionID string) {
	if c.st == nil {
		return
	}
	c.mu.Lock()
	ag := c.sessionAgents[sessionID]
	c.mu.Unlock()
	if ag == nil {
		return
	}
	c.st.UpdateSessionPermissions(sessionID, encodePermissions(ag.Grants())) //nolint:errcheck
}
