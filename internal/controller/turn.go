package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/session"
)

// turnState holds per-turn timing data. Each SendMessage / Compact call gets its
// own instance, avoiding races between a draining old goroutine and a new turn.
type turnState struct {
	connectStart    time.Time
	thinkingPending bool
	thinkingStart   time.Time
	thinkingFrozen  bool
	statusCancel    context.CancelFunc
}

func (ts *turnState) stopCountdown() {
	if ts.statusCancel != nil {
		ts.statusCancel()
		ts.statusCancel = nil
	}
}

func (ts *turnState) reset() {
	ts.stopCountdown()
	ts.connectStart = time.Time{}
	ts.thinkingPending = false
	ts.thinkingFrozen = false
}

// startConnectingTimer ticks EvConnecting events once per second until the turnState
// context is cancelled. retryAttempt > 0 means a retry countdown is active.
func (c *Controller) startConnectingTimer(sessionID string, ts *turnState, retryAttempt, maxAttempts int, retryDelay time.Duration) {
	ts.stopCountdown()
	ctx, cancel := context.WithCancel(c.ctx)
	ts.statusCancel = cancel
	start := ts.connectStart
	go func() {
		retryRemaining := int(retryDelay.Seconds())
		for {
			elapsed := int(time.Since(start).Seconds())
			c.emit(Event{
				Kind:           EvConnecting,
				SessionID:      sessionID,
				Elapsed:        elapsed,
				RetryAttempt:   retryAttempt,
				MaxAttempts:    maxAttempts,
				RetryRemaining: retryRemaining,
			})
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return
			}
			if retryAttempt > 0 && retryRemaining > 0 {
				retryRemaining--
			}
		}
	}()
}

// finalizeStatus emits EvThinkingDone and stops the connecting timer.
// No-op after the first call per turn.
func (c *Controller) finalizeStatus(sessionID string, ts *turnState) {
	if ts.thinkingFrozen {
		return
	}
	ts.thinkingFrozen = true
	ts.stopCountdown()
	if ts.thinkingPending {
		secs := int(time.Since(ts.thinkingStart).Seconds())
		c.emit(Event{Kind: EvThinkingDone, SessionID: sessionID, ThinkingSecs: secs})
		ts.thinkingPending = false
	} else {
		c.emit(Event{Kind: EvThinkingDone, SessionID: sessionID, ThinkingSecs: -1})
	}
}

// SendMessage adds a user message to the active (or new) session and starts the
// agent loop. Caller must be in the tview event loop; rendering is the caller's
// responsibility after this returns (the session is already updated).
func (c *Controller) SendMessage(text string) {
	c.mu.Lock()
	if c.sendCancel != nil {
		old := c.sendCancel
		c.sendCancel = nil
		c.mu.Unlock()
		old()
	} else {
		c.mu.Unlock()
	}

	sess, ok := c.mgr.Active()
	if !ok {
		return
	}

	sess.AddMessage(session.Message{
		Role:      session.RoleUser,
		Content:   text,
		SentLabel: agent.FormatSentLabel(time.Now()),
	})
	sess.SetStatus(session.StatusConnecting)

	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	c.sendCancel = cancel
	c.mu.Unlock()

	c.mu.Lock()
	ag := c.ag
	c.mu.Unlock()

	agCh := ag.Send(ctx, sess)
	go c.processAgentEvents(sess.ID, agCh, &turnState{})
}

// processAgentEvents translates raw agent events into controller events and updates
// session state. Runs in a goroutine; must not touch the UI directly.
func (c *Controller) processAgentEvents(sessionID string, agCh <-chan agent.Event, ts *turnState) {
	for agEv := range agCh {
		sess, ok := c.mgr.Get(sessionID)
		if !ok {
			continue
		}
		isActive := c.mgr.ActiveID() == sessionID

		switch agEv.Type {
		case agent.EventSelectPrompt:
			sess.SetStatus(session.StatusWaiting)
			c.emit(Event{
				Kind:         EvSelectPrompt,
				SessionID:    sessionID,
				Tool:         agEv.Tool,
				SelectRespCh: agEv.SelectRespCh,
			})

		case agent.EventToolApproval:
			sess.SetStatus(session.StatusWaiting)
			c.emit(Event{
				Kind:      EvToolApproval,
				SessionID: sessionID,
				Tool:      agEv.Tool,
				RespCh:    agEv.RespCh,
			})

		case agent.EventReasoningDelta:
			sess.SetStatus(session.StatusRunning)
			if !isActive {
				break
			}
			if !ts.thinkingFrozen {
				if !ts.thinkingPending {
					ts.thinkingPending = true
					ts.thinkingStart = time.Now()
					ts.stopCountdown()
				}
				secs := int(time.Since(ts.thinkingStart).Seconds())
				c.emit(Event{Kind: EvThinkingUpdate, SessionID: sessionID, ThinkingSecs: secs})
			}

		case agent.EventTextDelta:
			sess.SetStatus(session.StatusRunning)
			if isActive {
				c.finalizeStatus(sessionID, ts)
				c.emit(Event{Kind: EvRedraw, SessionID: sessionID})
			}

		case agent.EventConnecting:
			sess.SetStatus(session.StatusConnecting)
			attempt, maxAttempts, retryAfter, connErr := agEv.Attempt, agEv.MaxAttempts, agEv.RetryAfter, agEv.Err
			if attempt == 1 && retryAfter == 0 {
				ts.reset()
				ts.connectStart = time.Now()
			}
			if retryAfter > 0 {
				if connErr != nil {
					c.emit(Event{Kind: EvStatusErr, SessionID: sessionID, Text: connErr.Error()})
				}
				c.startConnectingTimer(sessionID, ts, attempt, maxAttempts, retryAfter)
			} else {
				if attempt > 1 {
					c.emit(Event{Kind: EvRedraw, SessionID: sessionID})
				}
				c.startConnectingTimer(sessionID, ts, 0, 0, 0)
			}

		case agent.EventPreparingTool:
			sess.SetStatus(session.StatusRunning)
			if isActive {
				c.finalizeStatus(sessionID, ts)
				c.emit(Event{Kind: EvRedraw, SessionID: sessionID})
			}

		case agent.EventToolStart, agent.EventToolDone:
			sess.SetStatus(session.StatusRunning)
			if isActive {
				c.emit(Event{Kind: EvRedraw, SessionID: sessionID})
			}

		case agent.EventUsageUpdate:
			pt := agEv.PromptTokens
			ct := agEv.CompletionTokens
			callCost := agEv.CallCost
			c.mu.Lock()
			cost := callCost
			if cost == 0 {
				cost = float64(pt)*c.cfg.InputPrice/1_000_000 +
					float64(ct)*c.cfg.OutputPrice/1_000_000
			}
			if cost > 0 {
				c.sessionCosts[sessionID] += cost
			}
			c.lastPromptTokens[sessionID] = pt
			totalCost := c.sessionCosts[sessionID]
			c.mu.Unlock()
			c.emit(Event{
				Kind:         EvTokensUpdate,
				SessionID:    sessionID,
				PromptTokens: pt,
				SessionCost:  totalCost,
			})

		case agent.EventDone:
			sess.SetStatus(session.StatusIdle)
			c.finalizeStatus(sessionID, ts)
			c.mu.Lock()
			cost := c.sessionCosts[sessionID]
			pt := c.lastPromptTokens[sessionID]
			c.mu.Unlock()
			go c.PersistSession(sess, cost, pt)
			c.emit(Event{Kind: EvDone, SessionID: sessionID})

		case agent.EventError:
			sess.SetStatus(session.StatusError)
			errStr := "agent error"
			if agEv.Err != nil {
				errStr = agEv.Err.Error()
			}
			ts.stopCountdown()
			sess.UpdateStatus("")
			c.emit(Event{Kind: EvError, SessionID: sessionID, Text: fmt.Sprintf("error: %s", errStr)})
		}
	}
}
