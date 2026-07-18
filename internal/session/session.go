package session

import (
	"crypto/rand"
	"fmt"
	"sync"
)

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("session: rand.Read: %v", err))
	}
	return fmt.Sprintf("%x", b)
}

// Role identifies the kind of conversation item.
type Role string

// The conversation is a plain list of items at one level. A message is spoken
// text (RoleUser / RoleAssistant); a status is a step the assistant took —
// either RoleThinking (chain-of-thought) or RoleTool (one tool call and its
// result). Statuses render as expandable boxes and may be aggregated by the UI.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleThinking  Role = "thinking" // status: chain-of-thought reasoning
	RoleTool      Role = "tool"     // status: one tool call with its result
)

// IsStatus reports whether the role is a status step (thinking or tool call)
// rather than a spoken message.
func (r Role) IsStatus() bool { return r == RoleThinking || r == RoleTool }

// Status mirrors the agent's current state.
type Status string

const (
	StatusIdle       Status = "idle"
	StatusConnecting Status = "connecting"
	StatusRunning    Status = "running"
	StatusWaiting    Status = "waiting" // waiting for user input (tool approval)
	StatusCompacting Status = "compacting"
	StatusError      Status = "error"
)

// IsActive reports whether the status represents an in-progress operation.
func (s Status) IsActive() bool {
	switch s {
	case StatusConnecting, StatusRunning, StatusWaiting, StatusCompacting:
		return true
	}
	return false
}

// StatusEventKind classifies a single tool operation update.
type StatusEventKind int8

const (
	StatusEventDoing   StatusEventKind = iota // tool executing now
	StatusEventWants                          // awaiting user approval
	StatusEventDone                           // tool completed
	StatusEventRefused                        // approval denied; excluded from aggregate
	StatusEventFailed                         // tool errored; excluded from aggregate
)

// StatusEvent records one structured tool operation update.
type StatusEvent struct {
	Kind   StatusEventKind
	Verb   string // doing: gerund; wants: infinitive; done: past-tense
	NounP  string // plural noun for aggregation (done only): "files", "commands"
	Target string // display string: "config.go", "ls -la"
}

// ToolUse is a tool call made by the assistant.
type ToolUse struct {
	ID     string
	Name   string
	Input  string // JSON; sent verbatim to the model
	Output string
	State  string // "running" | "done" | "error"
}

// Message is one item in the conversation. Its Role selects which fields apply:
//
//	RoleUser      — Content, SentLabel
//	RoleAssistant — Content (final text), or Error
//	RoleThinking  — Thinking text, ThinkingSecs
//	RoleTool      — ToolUses[0] (the call + its result in Output/State), StatusEvents
//
// Thinking and tool items are "statuses" (Role.IsStatus): expandable boxes the
// UI may aggregate. Whether a box is expanded is UI-only state (the UI owns
// display arrangement); Partial marks streaming in progress.
type Message struct {
	Role         Role
	Content      string        // RoleUser / RoleAssistant text
	SentLabel    string        // RoleUser: frozen "[Sent: ...]" suffix
	Thinking     string        // RoleThinking: reasoning_content
	ThinkingSecs int           // RoleThinking: CoT duration in seconds
	StatusEvents []StatusEvent // RoleTool: append-only op record for aggregation
	ToolUses     []ToolUse     // RoleTool: the single tool call, carrying its own result
	Partial      bool          // streaming in progress
	Error        error         // RoleAssistant: set if the turn failed
}

// Session holds the full local state for one conversation.
type Session struct {
	mu               sync.RWMutex
	ID               string
	Title            string
	Status           Status
	WorkDir          string
	PlanMode         bool
	PlanModeExited   bool // true for one turn after plan mode is disabled
	ColdResumed      bool // true for one turn after session is loaded from DB
	msgs             []Message
	compactedSummary string // latest compact summary; empty = no compact
	compactSeqs      []int  // all compact atSeqs in ascending order; nil = no compact
}

// SetPlanMode enables or disables plan mode for this session.
// Disabling sets PlanModeExited so the next buildMessages call can notify the model.
func (s *Session) SetPlanMode(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on && s.PlanMode {
		s.PlanModeExited = true
	}
	if on {
		s.PlanModeExited = false
	}
	s.PlanMode = on
}

// IsPlanMode reports whether plan mode is active for this session.
func (s *Session) IsPlanMode() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.PlanMode
}

// ClearPlanModeExited clears the one-shot exit flag after the model has been notified.
func (s *Session) ClearPlanModeExited() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PlanModeExited = false
}

// SetColdResumed marks that this session was just loaded from DB.
func (s *Session) SetColdResumed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ColdResumed = true
}

// TakeColdResumed returns and clears the cold-resume flag.
func (s *Session) TakeColdResumed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.ColdResumed
	s.ColdResumed = false
	return v
}

// AppendStatusEvent appends a structured op event to the tool status at idx.
// idx is the value returned by AddMessage when the tool item was created.
func (s *Session) AppendStatusEvent(idx int, ev StatusEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx >= 0 && idx < len(s.msgs) && s.msgs[idx].Role == RoleTool {
		s.msgs[idx].StatusEvents = append(s.msgs[idx].StatusEvents, ev)
	}
}

// GetStatus returns the current session status without copying messages.
func (s *Session) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// NewSession creates an empty session.
func NewSession(id, workDir string) *Session {
	return &Session{
		ID:      id,
		WorkDir: workDir,
		Status:  StatusIdle,
	}
}

// SetCompact appends a new compact point. summary is the new cumulative summary
// covering everything up to and including atSeq.
func (s *Session) SetCompact(summary string, atSeq int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactedSummary = summary
	s.compactSeqs = append(s.compactSeqs, atSeq)
}

// LoadCompact restores compact state from persistent storage (replaces, does not append).
func (s *Session) LoadCompact(summary string, seqs []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.compactedSummary = summary
	if len(seqs) > 0 {
		s.compactSeqs = make([]int, len(seqs))
		copy(s.compactSeqs, seqs)
	} else {
		s.compactSeqs = nil
	}
}

// GetCompact returns the latest compact summary and all compact point indices.
// summary is empty and seqs is nil when no compact has been performed.
func (s *Session) GetCompact() (summary string, seqs []int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.compactSeqs) == 0 {
		return s.compactedSummary, nil
	}
	out := make([]int, len(s.compactSeqs))
	copy(out, s.compactSeqs)
	return s.compactedSummary, out
}

// AddMessage appends a message and returns its index.
func (s *Session) AddMessage(m Message) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := len(s.msgs)
	s.msgs = append(s.msgs, m)
	// Use first user message as title
	if s.Title == "" && m.Role == RoleUser && m.Content != "" {
		t := m.Content
		if len(t) > 40 {
			t = t[:37] + "…"
		}
		s.Title = t
	}
	return idx
}

// AppendTextDelta appends streaming text to the message at idx.
func (s *Session) AppendTextDelta(idx int, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].Content += delta
	}
}

// AppendThinkingDelta appends streaming reasoning_content to the message at idx.
func (s *Session) AppendThinkingDelta(idx int, delta string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].Thinking += delta
	}
}

// FinalizeMessage marks an assistant message complete with its final text.
func (s *Session) FinalizeMessage(idx int, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].Content = content
		s.msgs[idx].Partial = false
	}
}

// FinalizeThinking marks a thinking status complete and records its duration.
func (s *Session) FinalizeThinking(idx, secs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].ThinkingSecs = secs
		s.msgs[idx].Partial = false
	}
}

// SetToolResult updates the tool use state within a tool status message.
func (s *Session) SetToolResult(msgIdx int, callID, output string, isError bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msgIdx >= len(s.msgs) {
		return
	}
	for i, tu := range s.msgs[msgIdx].ToolUses {
		if tu.ID == callID {
			s.msgs[msgIdx].ToolUses[i].Output = output
			if isError {
				s.msgs[msgIdx].ToolUses[i].State = "error"
			} else {
				s.msgs[msgIdx].ToolUses[i].State = "done"
			}
			return
		}
	}
}

// SetMessageError marks an assistant message as failed.
func (s *Session) SetMessageError(idx int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].Partial = false
		s.msgs[idx].Error = err
	}
}

// SetStatus changes the session status.
func (s *Session) SetStatus(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = st
}

// BulkLoad replaces the message history with loaded records (e.g. from DB).
// Unlike AddMessage it does not derive the title.
func (s *Session) BulkLoad(msgs []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = make([]Message, len(msgs))
	copy(s.msgs, msgs)
}

// Snapshot returns a copy of messages and current status for rendering.
func (s *Session) Snapshot() ([]Message, Status) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := make([]Message, len(s.msgs))
	copy(msgs, s.msgs)
	return msgs, s.Status
}

// Manager tracks all sessions and which is active. It is an unordered set:
// tab ordering and arrangement are a UI-only concern (see the UI's tab list),
// so the manager deliberately keeps no display order of its own.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	activeID string
	workDir  string
}

// NewManager creates an empty manager.
func NewManager(workDir string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		workDir:  workDir,
	}
}

// WorkDir returns the working directory this manager was created with.
func (m *Manager) WorkDir() string {
	return m.workDir
}

// New creates a fresh session and registers it.
func (m *Manager) New() *Session {
	id := newID()
	s := NewSession(id, m.workDir)

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	return s
}

// Get returns the session by ID.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// SetActive marks the active session ID.
func (m *Manager) SetActive(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeID = id
}

// ActiveID returns the current active session ID.
func (m *Manager) ActiveID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeID
}

// Active returns the active session if set.
func (m *Manager) Active() (*Session, bool) {
	id := m.ActiveID()
	if id == "" {
		return nil, false
	}
	return m.Get(id)
}

// AddExisting registers a pre-built session (e.g. loaded from DB).
// No-ops if the id is already present.
func (m *Manager) AddExisting(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[s.ID]; ok {
		return
	}
	m.sessions[s.ID] = s
}

// Remove deletes a session from the manager.
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// AnyOther returns some session whose ID differs from exceptID, or nil if none.
// Order is unspecified — the UI decides which tab to focus after a close.
func (m *Manager) AnyOther(exceptID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, s := range m.sessions {
		if id != exceptID {
			return s
		}
	}
	return nil
}
