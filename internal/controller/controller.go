package controller

import (
	"context"
	"os"
	"sync"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/config"
	"github.com/aleksanaa/hyphae/internal/session"
	"github.com/aleksanaa/hyphae/internal/store"
)

// EventKind classifies a controller event sent to the UI.
type EventKind int

const (
	EvRedraw         EventKind = iota // session messages changed; re-render active session
	EvStatusMsg                       // show plain message in status bar
	EvStatusErr                       // show error in status bar
	EvTokensUpdate                    // prompt tokens / session cost updated
	EvContextWindow                   // context window size discovered
	EvConnecting                      // connecting timer tick (UI formats the string)
	EvThinkingUpdate                  // CoT in progress (UI formats the string)
	EvThinkingDone                    // CoT finished; ThinkingSecs < 0 means no thinking occurred
	EvDone                            // turn complete
	EvError                           // turn errored
	EvToolApproval                    // tool needs user approval (forwarded from agent)
	EvSelectPrompt                    // ask_user tool needs user selection (forwarded from agent)
)

// Event is one item sent from the controller to the UI.
type Event struct {
	Kind      EventKind
	SessionID string

	// EvStatusMsg / EvStatusErr
	Text string

	// EvTokensUpdate
	PromptTokens int64
	SessionCost  float64

	// EvContextWindow
	ContextWindow int64

	// EvConnecting — raw numbers; UI formats the display string
	Elapsed        int
	RetryAttempt   int
	MaxAttempts    int
	RetryRemaining int
	ConnErr        error

	// EvThinkingUpdate / EvThinkingDone — raw seconds; UI formats the display string
	// ThinkingSecs < 0 in EvThinkingDone means no CoT occurred this turn
	ThinkingSecs int

	// EvToolApproval — forwarded from agent
	Tool   *agent.ToolEvent
	RespCh chan<- agent.ApprovalResult // send exactly once to resolve the approval

	// EvSelectPrompt — forwarded from agent
	SelectRespCh chan<- string // send exactly once to resolve the selection
}

// Re-exported agent types so callers of this package need not import agent directly.
type (
	ApprovalResult = agent.ApprovalResult
	ToolEvent      = agent.ToolEvent
)

// ModelInfo describes a model available at an endpoint.
type ModelInfo struct {
	ID            string
	ContextWindow int64
}

// SessionSummary is a lightweight session record for listing.
type SessionSummary struct {
	ID        string
	Title     string
	UpdatedAt int64
}

// Controller owns session lifecycle, agent orchestration, and persistence.
// The UI subscribes to events via Events() and calls methods to trigger actions.
type Controller struct {
	mu  sync.Mutex
	ag  *agent.Agent
	mgr *session.Manager
	cfg *config.Config
	st  *store.Store
	ctx context.Context

	incoming chan Event // emit() writes here; eventForwarder drains it
	ch       chan Event // Events() returns this; eventForwarder fills it

	sessionCosts     map[string]float64
	lastPromptTokens map[string]int64
	sendCancel       context.CancelFunc
}

// New creates a Controller. ctx is the application-lifetime context; when it is
// cancelled the event channel is closed and all background operations stop.
func New(ag *agent.Agent, mgr *session.Manager, cfg *config.Config, st *store.Store, ctx context.Context) *Controller {
	c := &Controller{
		ag:               ag,
		mgr:              mgr,
		cfg:              cfg,
		st:               st,
		ctx:              ctx,
		incoming:         make(chan Event),
		ch:               make(chan Event),
		sessionCosts:     make(map[string]float64),
		lastPromptTokens: make(map[string]int64),
	}
	go c.eventForwarder()
	return c
}

// eventForwarder relays events from incoming to ch via an in-memory queue so
// that emit never blocks regardless of consumer speed. It closes ch when the
// application context expires.
func (c *Controller) eventForwarder() {
	defer close(c.ch)
	var queue []Event
	for {
		if len(queue) == 0 {
			select {
			case ev := <-c.incoming:
				queue = append(queue, ev)
			case <-c.ctx.Done():
				return
			}
		} else {
			select {
			case ev := <-c.incoming:
				queue = append(queue, ev)
			case c.ch <- queue[0]:
				queue = queue[1:]
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// NewFromConfig is the preferred constructor: it creates all dependencies (agent,
// session manager, store) from cfg and returns a ready-to-use Controller along
// with a cancel func that must be called to shut it down (e.g. on app exit).
func NewFromConfig(cfg *config.Config) (*Controller, context.CancelFunc) {
	ep := cfg.ActiveEndpoint()
	ag := agent.New(ep.BaseURL, ep.APIKey, cfg.Model)
	workDir, _ := os.Getwd()
	mgr := session.NewManager(workDir)
	st, _ := store.Open(store.DefaultPath()) // non-fatal if nil
	ctx, cancel := context.WithCancel(context.Background())
	c := New(ag, mgr, cfg, st, ctx)
	shutdown := func() {
		cancel()
		if st != nil {
			st.Close() //nolint:errcheck
		}
	}
	return c, shutdown
}

// Events returns the read-only event channel. Closed when the application context expires.
func (c *Controller) Events() <-chan Event { return c.ch }

// Context returns the application-lifetime context.
func (c *Controller) Context() context.Context { return c.ctx }

// emit enqueues an event for delivery. Never blocks; returns immediately once
// the forwarder goroutine accepts the event (or the context expires).
func (c *Controller) emit(ev Event) {
	select {
	case c.incoming <- ev:
	case <-c.ctx.Done():
	}
}

// SetAgent replaces the active agent (called on model switch).
func (c *Controller) SetAgent(ag *agent.Agent) {
	c.mu.Lock()
	c.ag = ag
	c.mu.Unlock()
}

// Manager returns the session manager.
func (c *Controller) Manager() *session.Manager { return c.mgr }

// ActiveSession returns the active session if one exists.
func (c *Controller) ActiveSession() (*session.Session, bool) { return c.mgr.Active() }

// ActiveID returns the active session ID.
func (c *Controller) ActiveID() string { return c.mgr.ActiveID() }

// IsRunning reports whether the active session's agent is currently running.
func (c *Controller) IsRunning() bool {
	if sess, ok := c.mgr.Active(); ok {
		_, st := sess.Snapshot()
		return st == session.StatusRunning
	}
	return false
}

// Cancel interrupts the current agent turn or compact operation and sets the
// active session status to Idle. Safe to call when nothing is running.
func (c *Controller) Cancel() {
	c.mu.Lock()
	cancel := c.sendCancel
	c.sendCancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		if sess, ok := c.mgr.Active(); ok {
			sess.SetStatus(session.StatusIdle)
		}
	}
}

// SessionCost returns the cumulative cost for a session (in-memory only).
func (c *Controller) SessionCost(id string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionCosts[id]
}

// LastPromptTokens returns the most recently recorded prompt token count for a session.
func (c *Controller) LastPromptTokens(id string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastPromptTokens[id]
}
