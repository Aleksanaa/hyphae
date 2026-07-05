package controller

import (
	"fmt"
	"os"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/session"
	"github.com/aleksanaa/hyphae/internal/store"
)

// SessionInfo carries the billing and display metadata for a resumed session.
type SessionInfo struct {
	TotalCost     float64
	PromptTokens  int64
	ContextWindow int64
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

	workDir, _ := os.Getwd()
	sess := session.NewSession(id, workDir)

	seqToMemIdx := make(map[int]int, len(loaded))
	msgs := make([]session.Message, 0, len(loaded))
	for _, lm := range loaded {
		if lm.Role == string(session.RoleAssistant) && lm.Thinking != "" {
			// Insert a bare status row; the chat renderer derives the "thought for Xs"
			// label itself from ThinkingSecs + the adjacent assistant's Thinking field.
			msgs = append(msgs, session.Message{
				Role:         session.RoleStatus,
				ThinkingSecs: lm.ThinkingSecs,
			})
		}
		seqToMemIdx[lm.Seq] = len(msgs)
		msg := session.Message{
			Role:      session.Role(lm.Role),
			Content:   lm.Content,
			Thinking:  lm.Thinking,
			SentLabel: lm.SentLabel,
		}
		if lm.Role == string(session.RoleTool) {
			msg.ToolResult = &session.ToolResult{
				ID:      lm.CallID,
				Content: lm.Content,
				IsError: lm.IsError,
			}
			msg.Content = ""
		}
		if len(lm.ToolCalls) > 0 {
			msg.ToolUses = make([]session.ToolUse, len(lm.ToolCalls))
			for i, tc := range lm.ToolCalls {
				_, params := agent.ParseToolDisplay(tc.Name, tc.Args)
				msg.ToolUses[i] = session.ToolUse{
					ID:            tc.CallID,
					Name:          tc.Name,
					DisplayKey:    tc.DisplayKey,
					Input:         tc.Args,
					Output:        tc.Result,
					State:         tc.Status,
					DisplayParams: params,
				}
			}
		}
		msgs = append(msgs, msg)
	}
	sess.BulkLoad(msgs)

	for _, m := range msgs {
		if m.Role == session.RoleUser && m.Content != "" {
			t := m.Content
			if len(t) > 40 {
				t = t[:37] + "…"
			}
			sess.Title = t
			break
		}
	}

	c.mgr.AddExisting(sess)
	c.mgr.SetActive(id)

	var info SessionInfo
	if row, err := c.st.GetSession(id); err == nil {
		info.TotalCost = row.TotalCost
		info.PromptTokens = row.LastPromptTokens
		info.ContextWindow = row.ContextWindow

		c.mu.Lock()
		c.sessionCosts[id] = row.TotalCost
		c.lastPromptTokens[id] = row.LastPromptTokens
		c.mu.Unlock()

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

// PersistSession writes all non-status, non-partial messages for sess to the store.
// Safe to call from a goroutine; store errors are silently ignored.
func (c *Controller) PersistSession(sess *session.Session) {
	if c.st == nil {
		return
	}
	msgs, _ := sess.Snapshot()
	var lastThinkingSecs int
	for seq, msg := range msgs {
		if msg.Role == session.RoleStatus {
			lastThinkingSecs = msg.ThinkingSecs
			continue
		}
		if msg.Partial {
			continue
		}
		content := msg.Content
		callID := ""
		isError := false
		thinkingSecs := 0
		if msg.Role == session.RoleAssistant {
			thinkingSecs = lastThinkingSecs
		}
		lastThinkingSecs = 0
		if msg.Role == session.RoleTool && msg.ToolResult != nil {
			content = msg.ToolResult.Content
			callID = msg.ToolResult.ID
			isError = msg.ToolResult.IsError
		}
		msgID, err := c.st.InsertMessage(sess.ID, seq, string(msg.Role), content, msg.Thinking, thinkingSecs, callID, isError, msg.SentLabel)
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
			c.st.InsertToolCall(msgID, tu.ID, tu.Name, tu.DisplayKey, tu.Input) //nolint:errcheck
			if tu.State != "running" && tu.State != "pending" {
				c.st.FinalizeToolCall(tu.ID, tu.Output, tu.State, tu.State == "error") //nolint:errcheck
			}
		}
	}
	if sess.Title != "" {
		c.st.UpdateSessionTitle(sess.ID, sess.Title) //nolint:errcheck
	}
}
