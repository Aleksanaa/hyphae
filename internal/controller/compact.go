package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aleksanaa/hyphae/internal/session"
)

// Compact runs the compact workflow: validates the session state, calls the model
// with the compact prompt, updates compact state, and persists to the store.
// Returns an error if compaction cannot proceed (no active session, already running,
// or no complete exchange since the last compact point).
func (c *Controller) Compact() error {
	sess, ok := c.mgr.Active()
	if !ok {
		return fmt.Errorf("no active session")
	}

	msgs, status := sess.Snapshot()
	if status == session.StatusRunning {
		return fmt.Errorf("agent or compact is already running")
	}

	_, compactSeqs := sess.GetCompact()
	compactAtSeq := -1
	if len(compactSeqs) > 0 {
		compactAtSeq = compactSeqs[len(compactSeqs)-1]
	}
	var hasUser, hasAssistant bool
	for i, m := range msgs {
		if i <= compactAtSeq {
			continue
		}
		if m.Role == session.RoleUser {
			hasUser = true
		} else if m.Role == session.RoleAssistant && !m.Partial && m.Error == nil && m.Content != "" {
			hasAssistant = true
		}
		if hasUser && hasAssistant {
			break
		}
	}
	if !hasUser || !hasAssistant {
		return fmt.Errorf("need at least one complete exchange to compact")
	}

	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	c.sendCancel = cancel
	c.mu.Unlock()

	sess.SetStatus(session.StatusRunning)
	c.emit(Event{Kind: EvStatusMsg, SessionID: sess.ID, Text: "compacting conversation..."})
	c.emit(Event{Kind: EvRedraw, SessionID: sess.ID})

	c.mu.Lock()
	ag := c.ag
	c.mu.Unlock()

	go func() {
		summary, usage, err := ag.Compact(ctx, sess)

		c.mu.Lock()
		c.sendCancel = nil
		wasInterrupted := ctx.Err() != nil
		c.mu.Unlock()
		cancel()

		sess.SetStatus(session.StatusIdle)

		if err != nil {
			if wasInterrupted {
				c.emit(Event{Kind: EvRedraw, SessionID: sess.ID})
			} else {
				c.emit(Event{Kind: EvStatusErr, SessionID: sess.ID, Text: "compact failed: " + err.Error()})
				c.emit(Event{Kind: EvRedraw, SessionID: sess.ID})
			}
			return
		}

		currentMsgs, _ := sess.Snapshot()
		atSeq := len(currentMsgs) - 1
		sess.SetCompact(summary, atSeq)

		if c.st != nil {
			_, allMemSeqs := sess.GetCompact()
			seqParts := make([]string, len(allMemSeqs))
			for j, memSeq := range allMemSeqs {
				dbS := memSeq
				for dbS >= 0 && currentMsgs[dbS].Role == session.RoleStatus {
					dbS--
				}
				seqParts[j] = strconv.Itoa(dbS)
			}
			lastDBSeq, _ := strconv.Atoi(seqParts[len(seqParts)-1])
			go c.st.UpdateSessionCompact(sess.ID, summary, int64(lastDBSeq), strings.Join(seqParts, ",")) //nolint:errcheck
		}

		// After compact, the effective context size is the output (the summary).
		// Use CompletionTokens as the new prompt-token baseline.
		c.mu.Lock()
		c.lastPromptTokens[sess.ID] = usage.CompletionTokens
		cost := float64(usage.PromptTokens)*c.cfg.InputPrice/1_000_000 +
			float64(usage.CompletionTokens)*c.cfg.OutputPrice/1_000_000
		if cost > 0 {
			c.sessionCosts[sess.ID] += cost
		}
		totalCost := c.sessionCosts[sess.ID]
		c.mu.Unlock()

		if c.st != nil {
			go c.st.UpdateSessionUsage(sess.ID, totalCost, usage.CompletionTokens) //nolint:errcheck
		}

		c.emit(Event{
			Kind:         EvTokensUpdate,
			SessionID:    sess.ID,
			PromptTokens: usage.CompletionTokens,
			SessionCost:  totalCost,
		})
		c.emit(Event{Kind: EvRedraw, SessionID: sess.ID})
		c.emit(Event{Kind: EvStatusMsg, SessionID: sess.ID, Text: "conversation compacted"})
	}()

	return nil
}
