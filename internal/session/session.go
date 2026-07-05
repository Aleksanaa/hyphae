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

// Role is the speaker of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleStatus    Role = "status" // UI-only status message; empty content = hidden
)

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

// ToolParam is a display key-value pair extracted from a tool's JSON input.
type ToolParam struct{ Key, Value string }

// ToolUse is a tool call made by the assistant.
type ToolUse struct {
	ID            string
	Name          string
	Input         string // JSON; sent verbatim to the model
	Output        string
	State         string      // "running" | "done" | "error"
	DisplayKey    string      // primary arg value for summary labels (≤50 chars)
	DisplayParams []ToolParam // ordered key-value pairs for the expanded param view
}

// ToolResult is the response to a tool call.
type ToolResult struct {
	ID      string
	Content string
	IsError bool
}

// Message is one turn in the conversation.
type Message struct {
	Role              Role
	Content           string
	SentLabel         string      // frozen "[Sent: ...]" suffix for user messages; empty for all other roles
	Thinking          string      // reasoning_content from CoT-capable models
	ThinkingSecs      int         // UI: CoT duration in seconds; set once per turn on RoleStatus
	ThinkingExpanded  bool        // UI: whether the thoughts box is expanded (RoleStatus only)
	ToolGroupExpanded bool        // UI: whether the tool-call group is shown as expanded param boxes
	ExpandedBox       bool        // UI: synthetic renderedMsgs entry rendered as an expanded dotted box
	BoxTitle          string      // tview-tagged label for ExpandedBox or FullWidth box
	FullWidth         bool        // UI: force box to terminal width with centered title (requires BoxTitle)
	ContentTagged     bool        // UI: Content already contains tview tags; skip Escape in rendering
	ToolUses          []ToolUse   // assistant turns only
	ToolResult        *ToolResult // tool turns only
	Partial           bool        // streaming in progress
	Error             error       // set if the turn failed
}

// ToggleToolGroupExpanded flips the ToolGroupExpanded flag on an assistant message,
// toggling whether the tool-call group is shown as expanded parameter boxes.
func (s *Session) ToggleToolGroupExpanded(msgIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msgIdx >= 0 && msgIdx < len(s.msgs) {
		s.msgs[msgIdx].ToolGroupExpanded = !s.msgs[msgIdx].ToolGroupExpanded
	}
}

// ToggleThinkingExpanded flips the ThinkingExpanded flag on a RoleStatus message.
func (s *Session) ToggleThinkingExpanded(idx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx >= 0 && idx < len(s.msgs) && s.msgs[idx].Role == RoleStatus {
		s.msgs[idx].ThinkingExpanded = !s.msgs[idx].ThinkingExpanded
	}
}

// Session holds the full local state for one conversation.
type Session struct {
	mu               sync.RWMutex
	ID               string
	Title            string
	Status           Status
	WorkDir          string
	msgs             []Message
	statusMsgIdx     int    // index of current turn's RoleStatus message; -1 if none
	compactedSummary string // latest compact summary; empty = no compact
	compactSeqs      []int  // all compact atSeqs in ascending order; nil = no compact
}

// UpdateStatus replaces the content of the current turn's status message.
// Empty string hides it. No-op if no status message is set.
func (s *Session) UpdateStatus(content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusMsgIdx >= 0 && s.statusMsgIdx < len(s.msgs) {
		s.msgs[s.statusMsgIdx].Content = content
	}
}

// FinalizeThinkingStatus sets the status content and thinking duration together.
// Called once per turn when CoT reasoning completes.
func (s *Session) FinalizeThinkingStatus(content string, secs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusMsgIdx >= 0 && s.statusMsgIdx < len(s.msgs) {
		s.msgs[s.statusMsgIdx].Content = content
		s.msgs[s.statusMsgIdx].ThinkingSecs = secs
	}
}

// NewSession creates an empty session.
func NewSession(id, workDir string) *Session {
	return &Session{
		ID:           id,
		WorkDir:      workDir,
		Status:       StatusIdle,
		statusMsgIdx: -1,
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
	if m.Role == RoleStatus {
		s.statusMsgIdx = idx
	}
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

// FinalizeMessage marks a message as complete with its final content and tool uses.
func (s *Session) FinalizeMessage(idx int, content string, toolUses []ToolUse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < len(s.msgs) {
		s.msgs[idx].Content = content
		s.msgs[idx].ToolUses = toolUses
		s.msgs[idx].Partial = false
		for i := range s.msgs[idx].ToolUses {
			s.msgs[idx].ToolUses[i].State = "running"
		}
	}
}

// SetToolResult updates the tool use state within an assistant message.
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

// SetToolState sets the State field of a specific tool use within an assistant message.
func (s *Session) SetToolState(msgIdx int, callID, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msgIdx >= len(s.msgs) {
		return
	}
	for i, tu := range s.msgs[msgIdx].ToolUses {
		if tu.ID == callID {
			s.msgs[msgIdx].ToolUses[i].State = state
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
// Unlike AddMessage it does not derive the title or track status messages.
func (s *Session) BulkLoad(msgs []Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = make([]Message, len(msgs))
	copy(s.msgs, msgs)
	s.statusMsgIdx = -1
}

// Snapshot returns a copy of messages and current status for rendering.
func (s *Session) Snapshot() ([]Message, Status) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := make([]Message, len(s.msgs))
	copy(msgs, s.msgs)
	return msgs, s.Status
}

// Manager tracks all sessions and which is active.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	order    []string
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

// New creates a fresh session and adds it to the front.
func (m *Manager) New() *Session {
	id := newID()
	s := NewSession(id, m.workDir)

	m.mu.Lock()
	m.sessions[id] = s
	m.order = append([]string{id}, m.order...)
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
	m.order = append([]string{s.ID}, m.order...)
}

// All returns sessions in display order.
func (m *Manager) All() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.order))
	for _, id := range m.order {
		if s, ok := m.sessions[id]; ok {
			out = append(out, s)
		}
	}
	return out
}
