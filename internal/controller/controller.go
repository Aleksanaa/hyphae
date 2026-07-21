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
	EvTitle                           // session title changed; refresh tab labels
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

	// EvThinkingUpdate — elapsed CoT seconds for the live "thinking… (Ns)" label.
	// (The final duration is recorded on the thinking status by the agent.)
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

// Model is the uniform description of a chat model — the single value the
// controller uses for display, cost calculation, and persistence. Zero-valued
// numeric fields mean "unknown" (e.g. before models.dev has been consulted).
type Model struct {
	Endpoint      string  // endpoint name that serves the model
	ID            string  // model identifier
	ContextWindow int64   // max context tokens, 0 if unknown
	InputPrice    float64 // USD per 1M input tokens, 0 if unknown
	OutputPrice   float64 // USD per 1M output tokens, 0 if unknown
}

// Cost returns the USD cost of a turn with the given prompt/completion token
// counts, using the model's per-1M-token pricing.
func (m Model) Cost(promptTokens, completionTokens int64) float64 {
	return float64(promptTokens)*m.InputPrice/1_000_000 +
		float64(completionTokens)*m.OutputPrice/1_000_000
}

// SessionSummary is a lightweight session record for listing.
type SessionSummary struct {
	ID            string
	Title         string
	UpdatedAt     int64
	WorkDir       string
	ContextWindow int64 // models.dev-derived context window, 0 if unknown
	PromptTokens  int64 // last prompt token count, 0 if none yet
}

// Controller owns session lifecycle, agent orchestration, and persistence.
// The UI subscribes to events via Events() and calls methods to trigger actions.
type Controller struct {
	mu        sync.Mutex
	persistMu sync.Map // map[sessionID]*sync.Mutex — serializes concurrent PersistSession calls
	mgr       *session.Manager
	cfg       *config.Config
	st        *store.Store
	ctx       context.Context

	incoming chan Event // emit() writes here; eventForwarder drains it
	ch       chan Event // Events() returns this; eventForwarder fills it

	sessionCosts     map[string]float64
	lastPromptTokens map[string]int64
	sessionModels    map[string]Model        // sessionID → its model (identity, context, pricing)
	sessionAgents    map[string]*agent.Agent // sessionID → its agent (own model, endpoint, and run namespace)
	sendCancel       context.CancelFunc
}

// New creates a Controller. ctx is the application-lifetime context; when it is
// cancelled the event channel is closed and all background operations stop.
func New(mgr *session.Manager, cfg *config.Config, st *store.Store, ctx context.Context) *Controller {
	c := &Controller{
		mgr:              mgr,
		cfg:              cfg,
		st:               st,
		ctx:              ctx,
		incoming:         make(chan Event),
		ch:               make(chan Event),
		sessionCosts:     make(map[string]float64),
		lastPromptTokens: make(map[string]int64),
		sessionModels:    make(map[string]Model),
		sessionAgents:    make(map[string]*agent.Agent),
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

// NewFromConfig is the preferred constructor: it creates the session manager and
// store from cfg and returns a ready-to-use Controller along with a cancel func
// that must be called to shut it down (e.g. on app exit). Per-session agents are
// created lazily as sessions are opened.
func NewFromConfig(cfg *config.Config) (*Controller, context.CancelFunc) {
	workDir, _ := os.Getwd()
	mgr := session.NewManager(workDir)
	st, _ := store.Open(store.DefaultPath()) // non-fatal if nil
	ctx, cancel := context.WithCancel(context.Background())
	c := New(mgr, cfg, st, ctx)
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

// ContextWindow returns the in-memory context window for the active session's model.
func (c *Controller) ContextWindow() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionModels[c.mgr.ActiveID()].ContextWindow
}

// defaultModel is the identity a fresh session starts from: the config default.
// Pricing and context window are unknown here and filled per-session by
// EnrichSessionAsync. There is deliberately no global "current model" — each
// session owns its own (see sessionModels / sessionAgents).
func (c *Controller) defaultModel() Model {
	return Model{Endpoint: c.cfg.ActiveEndpointName, ID: c.cfg.Model}
}

// agentForModel builds an agent configured for m, resolving m.Endpoint to its
// base URL and API key from config (falling back to the active endpoint when the
// name is unknown). Each call yields a fresh agent with its own run namespace.
func (c *Controller) agentForModel(m Model) *agent.Agent {
	ep := c.cfg.ActiveEndpoint()
	for _, e := range c.cfg.Endpoints {
		if e.Name == m.Endpoint {
			ep = e
			break
		}
	}
	return agent.New(ep.BaseURL, ep.APIKey, m.ID)
}

// agentFor returns the agent that serves a session, lazily creating one from the
// session's model (or the current default) on first use. Caller must not hold c.mu.
func (c *Controller) agentFor(sessionID string) *agent.Agent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ag := c.sessionAgents[sessionID]; ag != nil {
		return ag
	}
	m, ok := c.sessionModels[sessionID]
	if !ok {
		m = c.defaultModel()
	}
	ag := c.agentForModel(m)
	c.sessionAgents[sessionID] = ag
	return ag
}

// emit enqueues an event for delivery. Never blocks; returns immediately once
// the forwarder goroutine accepts the event (or the context expires).
func (c *Controller) emit(ev Event) {
	select {
	case c.incoming <- ev:
	case <-c.ctx.Done():
	}
}

// Manager returns the session manager.
func (c *Controller) Manager() *session.Manager { return c.mgr }

// ActiveSession returns the active session if one exists.
func (c *Controller) ActiveSession() (*session.Session, bool) { return c.mgr.Active() }

// ActiveID returns the active session ID.
func (c *Controller) ActiveID() string { return c.mgr.ActiveID() }

// ClearActive marks no session as active. The UI calls this when it shows a tab
// that is not backed by a session, so session events are treated as background.
func (c *Controller) ClearActive() { c.mgr.SetActive("") }

// IsRunning reports whether the active session has any in-progress operation.
func (c *Controller) IsRunning() bool {
	if sess, ok := c.mgr.Active(); ok {
		_, st := sess.Snapshot()
		return st.IsActive()
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
