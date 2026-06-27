package ui

import (
	"context"
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksana/hypane/internal/agent"
	"github.com/aleksana/hypane/internal/config"
	"github.com/aleksana/hypane/internal/llm"
	"github.com/aleksana/hypane/internal/session"
)

// App is the root application coordinator.
type App struct {
	tapp       *tview.Application
	layout     *Layout
	ag         *agent.Agent
	manager    *session.Manager
	cfg        *config.Config
	appCtx     context.Context
	appCancel  context.CancelFunc
	sendCancel context.CancelFunc
}

// New wires up and returns a ready-to-run App.
func New(cfg *config.Config) *App {
	client := llm.New(cfg.BaseURL, cfg.APIKey, cfg.Model)
	ag := agent.New(client)
	manager := session.NewManager(cfg.WorkDir)

	ctx, cancel := context.WithCancel(context.Background())

	a := &App{
		tapp:      tview.NewApplication(),
		ag:        ag,
		manager:   manager,
		cfg:       cfg,
		appCtx:    ctx,
		appCancel: cancel,
	}

	chat := NewChatView()
	scrollbar := NewScrollbar(chat)
	status := NewStatusBar()
	input := NewInputView(a.sendMessage)
	layout := NewLayout(chat, scrollbar, input, status)
	a.layout = layout

	chat.TextView.SetFocusFunc(func() { chat.SetFocused(true) })
	chat.TextView.SetBlurFunc(func() { chat.SetFocused(false) })
	input.TextArea.SetFocusFunc(func() { input.SetFocused(true) })
	input.TextArea.SetBlurFunc(func() { input.SetFocused(false) })

	a.tapp.EnableMouse(true)
	a.tapp.SetMouseCapture(func(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
		_, iy, _, ih := chat.GetInnerRect()
		_, my := event.Position()
		if my < iy || my >= iy+ih {
			chat.ClearHover()
		}
		return event, action
	})
	a.tapp.SetInputCapture(a.handleGlobalKey)
	a.tapp.SetRoot(layout.Root, true).SetFocus(input.TextArea)

	return a
}

// Run starts the event loop.
func (a *App) Run() error {
	if a.cfg.APIKey == "" {
		a.layout.Status.SetError("OPENCODE_API_KEY is not set — add it to config or environment")
	} else if a.cfg.Model == "" {
		a.layout.Status.SetError("no model configured — set HYPANE_MODEL or model in config.toml")
	}

	defer a.appCancel()
	return a.tapp.Run()
}

// Stop shuts down the application.
func (a *App) Stop() {
	a.appCancel()
	a.tapp.Stop()
}

// handleGlobalKey intercepts application-level shortcuts.
func (a *App) handleGlobalKey(event *tcell.EventKey) *tcell.EventKey {
	switch {
	case event.Key() == tcell.KeyCtrlD:
		a.Stop()
		return nil

	case event.Key() == tcell.KeyCtrlC:
		if a.layout.Chat.HasSelection() {
			// Active drag selection — copy it.
			text := a.layout.Chat.SelectedText()
			if text != "" {
				if err := writeClipboard(text); err != nil {
					a.layout.Status.SetError(err.Error())
				}
			}
		} else if a.sendCancel != nil {
			// Agent is running — interrupt it.
			a.sendCancel()
			a.sendCancel = nil
			if sess, ok := a.manager.Active(); ok {
				sess.SetStatus(session.StatusIdle)
				a.redrawActive()
			}
		} else {
			// Agent is idle, no selection — copy hovered message.
			text := a.layout.Chat.HoveredContent()
			if text != "" {
				if err := writeClipboard(text); err != nil {
					a.layout.Status.SetError(err.Error())
				}
			}
		}
		return nil

	case event.Key() == tcell.KeyTab:
		// Toggle focus between input and chat.
		if a.tapp.GetFocus() == a.layout.Input.TextArea {
			a.tapp.SetFocus(a.layout.Chat.TextView)
		} else {
			a.tapp.SetFocus(a.layout.Input.TextArea)
		}
		return nil

	case event.Key() == tcell.KeyEscape:
		a.tapp.SetFocus(a.layout.Input.TextArea)
		return nil
	}
	return event
}

// sendMessage sends user text to the active (or a new) session.
// Called from the TextArea input capture → we are in the event loop, call directly.
func (a *App) sendMessage(text string) {
	if a.sendCancel != nil {
		a.sendCancel()
	}

	sess, ok := a.manager.Active()
	if !ok {
		sess = a.manager.New()
		a.manager.SetActive(sess.ID)
	}

	// Add user message and render before the goroutine starts so it appears immediately.
	sess.AddMessage(session.Message{Role: session.RoleUser, Content: text})
	msgs, _ := sess.Snapshot()
	a.layout.Chat.Render(msgs)

	ctx, cancel := context.WithCancel(a.appCtx)
	a.sendCancel = cancel

	sess.SetStatus(session.StatusRunning)
	a.layout.Status.SetDefault(a.cfg.Model, session.StatusRunning)

	ch := a.ag.Send(ctx, sess)
	go a.handleAgentEvents(sess.ID, ch)
}

// handleAgentEvents reads the event channel and updates the UI.
// Runs in a goroutine — must use QueueUpdateDraw for all UI calls.
func (a *App) handleAgentEvents(sessionID string, ch <-chan agent.Event) {
	for ev := range ch {
		isActive := a.manager.ActiveID() == sessionID
		sess, ok := a.manager.Get(sessionID)
		if !ok {
			continue
		}

		switch ev.Type {
		case agent.EventTextDelta, agent.EventToolStart, agent.EventToolDone:
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					msgs, status := sess.Snapshot()
					a.layout.Chat.Render(msgs)
					a.layout.Status.SetDefault(a.cfg.Model, status)
				})
			}

		case agent.EventDone:
			sess.SetStatus(session.StatusIdle)
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					msgs, status := sess.Snapshot()
					a.layout.Chat.Render(msgs)
					a.layout.Status.SetDefault(a.cfg.Model, status)
				})
			}

		case agent.EventError:
			sess.SetStatus(session.StatusError)
			errStr := "agent error"
			if ev.Err != nil {
				errStr = ev.Err.Error()
			}
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					msgs, _ := sess.Snapshot()
					a.layout.Chat.Render(msgs)
					a.layout.Status.SetError(fmt.Sprintf("error: %s", errStr))
				})
			}
		}
	}
}

// redrawActive refreshes the chat and status for the current session.
// Must only be called from the tview event loop.
func (a *App) redrawActive() {
	sess, ok := a.manager.Active()
	if !ok {
		return
	}
	msgs, status := sess.Snapshot()
	a.layout.Chat.Render(msgs)
	a.layout.Status.SetDefault(a.cfg.Model, status)
}
