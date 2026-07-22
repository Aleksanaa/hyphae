package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/session"
	"github.com/aleksanaa/hyphae/internal/store"
	"github.com/aleksanaa/hyphae/internal/strutil"
)

// SessionInfo carries the billing and display metadata for a resumed session.
type SessionInfo struct {
	Model        Model // identity, context window, and pricing for the session
	TotalCost    float64
	PromptTokens int64
	PlanMode     bool
}

// CloseSession persists a session and removes it from memory. Choosing which tab
// to focus next is the UI's job (tabs are a UI concern), so this only clears the
// active ID when the closed session was active; it does not pick a replacement.
func (c *Controller) CloseSession(id string) {
	sess, ok := c.mgr.Get(id)
	if !ok {
		return
	}
	c.mu.Lock()
	cost := c.sessionCosts[id]
	pt := c.lastPromptTokens[id]
	c.mu.Unlock()
	go c.PersistSession(sess, cost, pt)

	if c.mgr.ActiveID() == id {
		c.mgr.SetActive("")
	}
	c.mgr.Remove(id)
}

// SwitchSession activates an already-open in-memory session. Returns false if not found.
func (c *Controller) SwitchSession(id string) bool {
	if _, ok := c.mgr.Get(id); !ok {
		return false
	}
	c.mgr.SetActive(id)
	return true
}

// SaveSessionPlanMode persists the plan mode flag to the store.
func (c *Controller) SaveSessionPlanMode(id string, on bool) {
	if c.st != nil {
		go c.st.UpdateSessionPlanMode(id, on) //nolint:errcheck
	}
}

// NewSession creates a fresh session, registers it in storage, and makes it active.
func (c *Controller) NewSession() *session.Session {
	sess := c.mgr.New()
	c.mgr.SetActive(sess.ID)
	m := c.defaultModel()
	c.mu.Lock()
	c.sessionModels[sess.ID] = m
	c.sessionAgents[sess.ID] = c.agentForModel(m)
	c.mu.Unlock()
	go c.EnrichSessionAsync(sess.ID)
	return sess
}

// ResumeSession loads a session from persistent storage (if not already in memory),
// activates it, and restores billing state. Returns the session and its billing info.
// If the session is already in memory, it is just activated.
func (c *Controller) ResumeSession(id string) (*session.Session, SessionInfo, error) {
	if _, ok := c.mgr.Get(id); ok {
		c.mgr.SetActive(id)
		sess, _ := c.mgr.Get(id)
		c.mu.Lock()
		info := SessionInfo{
			TotalCost:    c.sessionCosts[id],
			PromptTokens: c.lastPromptTokens[id],
		}
		c.mu.Unlock()
		return sess, info, nil
	}

	if c.st == nil {
		return nil, SessionInfo{}, fmt.Errorf("no store available")
	}

	loaded, err := c.st.LoadSessionMessages(id)
	if err != nil {
		return nil, SessionInfo{}, err
	}

	// Default to the current directory; the stored work dir (restored from the
	// session row below) overrides it so a resumed session runs where it was
	// created, not where hyphae happens to be launched from.
	workDir, _ := os.Getwd()
	sess := session.NewSession(id, workDir)

	// Each row becomes one peer item (user, thinking, tool, or assistant). seqToMemIdx
	// maps a DB seq to its primary memory index so compact points can be remapped.
	seqToMemIdx := make(map[int]int, len(loaded))
	var msgs []session.Message
	for _, lm := range loaded {
		switch lm.Role {
		case string(session.RoleUser):
			seqToMemIdx[lm.Seq] = len(msgs)
			msgs = append(msgs, session.Message{Role: session.RoleUser, Content: lm.Content, SentLabel: lm.SentLabel})

		case string(session.RoleThinking):
			seqToMemIdx[lm.Seq] = len(msgs)
			msgs = append(msgs, session.Message{Role: session.RoleThinking, Thinking: lm.Thinking, ThinkingSecs: lm.ThinkingSecs})

		case string(session.RoleTool):
			// New tool status rows carry their own tool_calls. Legacy bare result
			// rows (result folded under the assistant) have none — drop them.
			if len(lm.ToolCalls) == 0 {
				continue
			}
			seqToMemIdx[lm.Seq] = len(msgs)
			msgs = append(msgs, toolStatusFromLoaded(lm))

		case string(session.RoleAssistant):
			// New assistant rows are plain text. Legacy rows fold a round's thinking
			// + tool calls onto the assistant; split those into preceding peers.
			split := lm.Thinking != "" || lm.ThinkingSecs > 0 || len(lm.ToolCalls) > 0
			if lm.Thinking != "" || lm.ThinkingSecs > 0 {
				msgs = append(msgs, session.Message{Role: session.RoleThinking, Thinking: lm.Thinking, ThinkingSecs: lm.ThinkingSecs})
			}
			if len(lm.ToolCalls) > 0 {
				msgs = append(msgs, toolStatusFromLoaded(lm))
			}
			if lm.Content != "" || !split {
				// Emit the message for real text, or a genuinely empty new-format row.
				seqToMemIdx[lm.Seq] = len(msgs)
				msgs = append(msgs, session.Message{Role: session.RoleAssistant, Content: lm.Content})
			} else {
				// Legacy tool-only round with no final text: seq maps to the last status.
				seqToMemIdx[lm.Seq] = len(msgs) - 1
			}
		}
	}
	sess.BulkLoad(msgs)
	sess.SetColdResumed()

	// Fallback placeholder from the first user message; the persisted title (which
	// may be model-generated) overrides it below once the session row is loaded.
	userRounds := 0
	for _, m := range msgs {
		if m.Role != session.RoleUser {
			continue
		}
		userRounds++
		if sess.Title == "" && m.Content != "" {
			sess.Title = strutil.Truncate(m.Content, 40)
		}
	}
	// Only settle the title if the session already crossed the generation
	// threshold — its title was (or should have been) generated. A session
	// resumed with fewer rounds keeps its placeholder and can still generate a
	// title once it accumulates enough rounds.
	if userRounds >= titleMinRounds {
		sess.MarkTitleFinal()
	}

	c.mgr.AddExisting(sess)
	c.mgr.SetActive(id)

	var info SessionInfo
	if row, err := c.st.GetSession(id); err == nil {
		if row.Title != "" {
			sess.Title = row.Title
		}
		info.TotalCost = row.TotalCost
		info.PromptTokens = row.LastPromptTokens
		info.PlanMode = row.PlanMode
		if row.WorkDir != "" {
			sess.WorkDir = row.WorkDir
		}
		info.Model = Model{
			Endpoint:      row.ActiveEndpoint,
			ID:            row.Model,
			ContextWindow: row.ContextWindow,
			InputPrice:    row.InputPrice,
			OutputPrice:   row.OutputPrice,
		}
		if row.PlanMode {
			sess.SetPlanMode(true)
		}

		c.mu.Lock()
		c.sessionCosts[id] = row.TotalCost
		c.lastPromptTokens[id] = row.LastPromptTokens
		if info.Model.ID == "" {
			// Legacy session with no stored model: run it on the default identity.
			info.Model = c.defaultModel()
		}
		c.sessionModels[id] = info.Model
		c.sessionAgents[id] = c.agentForModel(info.Model)
		c.mu.Unlock()

		// Fill any missing context window / pricing for this session's model.
		go c.EnrichSessionAsync(id)

		if row.CompactedSummary != "" {
			dbSeqs := store.ParseCompactSeqs(row.CompactSeqs)
			if len(dbSeqs) == 0 && row.CompactAtSeq >= 0 {
				dbSeqs = []int{int(row.CompactAtSeq)}
			}
			memSeqs := make([]int, len(dbSeqs))
			for j, dbS := range dbSeqs {
				idx, ok := seqToMemIdx[dbS]
				if !ok {
					idx = len(msgs) - 1
				}
				memSeqs[j] = idx
			}
			sess.LoadCompact(row.CompactedSummary, memSeqs)
		}
	}

	return sess, info, nil
}

// SetSessionWorkDir changes a session's working directory in memory and persists
// it, so subsequent tool/shell execution (which runs in sess.WorkDir) and future
// resumes use the new location. No-ops for an unknown session.
func (c *Controller) SetSessionWorkDir(id, workDir string) {
	if sess, ok := c.mgr.Get(id); ok {
		old := sess.WorkDir
		sess.WorkDir = workDir
		if old != "" && old != workDir {
			// Tell the model about the move on its next turn.
			sess.AddReminder(agent.WorkDirChangedLabel(old, workDir))
		}
	}
	if c.st != nil {
		_ = c.st.UpdateSessionWorkDir(id, workDir) //nolint:errcheck
	}
}

// toolStatusFromLoaded builds a RoleTool status item from a loaded row's tool
// calls and its status_label (JSON-encoded []StatusEvent).
func toolStatusFromLoaded(lm store.LoadedMessage) session.Message {
	m := session.Message{Role: session.RoleTool}
	if lm.StatusLabel != "" && lm.StatusLabel[0] == '[' {
		var evs []session.StatusEvent
		if json.Unmarshal([]byte(lm.StatusLabel), &evs) == nil {
			m.StatusEvents = evs
		}
	}
	for _, tc := range lm.ToolCalls {
		m.ToolUses = append(m.ToolUses, session.ToolUse{
			ID: tc.CallID, Name: tc.Name, Input: tc.Args, Output: tc.Result, State: tc.Status,
		})
	}
	return m
}

// PersistSession writes all non-partial items for sess to the store, one row per
// item. Safe to call from a goroutine; store errors are silently ignored.
// At most one persist runs per session at a time to prevent seq-number races.
func (c *Controller) PersistSession(sess *session.Session, cost float64, promptTokens int64) {
	if c.st == nil {
		return
	}
	mu, _ := c.persistMu.LoadOrStore(sess.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	msgs, _ := sess.Snapshot()
	persistable := false
	for _, msg := range msgs {
		if !msg.Partial {
			persistable = true
			break
		}
	}
	if !persistable {
		return
	}
	c.mu.Lock()
	sm := c.sessionModels[sess.ID]
	c.mu.Unlock()
	model, ep := sm.ID, sm.Endpoint
	if model == "" {
		model, ep = c.cfg.Model, c.cfg.ActiveEndpointName
	}
	workDir, _ := os.Getwd()
	c.st.CreateSession(sess.ID, workDir)                 //nolint:errcheck
	c.st.UpdateSessionUsage(sess.ID, cost, promptTokens) //nolint:errcheck
	c.st.UpdateSessionModel(sess.ID, model, ep)          //nolint:errcheck
	for seq, msg := range msgs {
		if msg.Partial {
			continue
		}
		callID := ""
		isError := false
		statusLabel := ""
		if msg.Role == session.RoleTool {
			if len(msg.StatusEvents) > 0 {
				b, _ := json.Marshal(msg.StatusEvents)
				statusLabel = string(b)
			}
			if len(msg.ToolUses) > 0 {
				callID = msg.ToolUses[0].ID
				isError = msg.ToolUses[0].State == "error"
			}
		}
		msgID, err := c.st.InsertMessage(sess.ID, seq, string(msg.Role), msg.Content, msg.Thinking, msg.ThinkingSecs, callID, isError, msg.SentLabel, statusLabel)
		if err != nil {
			continue
		}
		if msgID == 0 {
			msgID, err = c.st.MessageID(sess.ID, seq)
			if err != nil {
				continue
			}
		}
		for _, tu := range msg.ToolUses {
			c.st.InsertToolCall(msgID, tu.ID, tu.Name, tu.Input) //nolint:errcheck
			if tu.State != "running" && tu.State != "pending" {
				c.st.FinalizeToolCall(tu.ID, tu.Output, tu.State, tu.State == "error") //nolint:errcheck
			}
		}
	}
	if sess.Title != "" {
		c.st.UpdateSessionTitle(sess.ID, sess.Title) //nolint:errcheck
	}
}
