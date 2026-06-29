package ui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksana/hyphae/internal/agent"
	"github.com/aleksana/hyphae/internal/config"
	"github.com/aleksana/hyphae/internal/session"
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
	ep := cfg.ActiveEndpoint()
	ag := agent.New(ep.BaseURL, ep.APIKey, cfg.Model)
	workDir, _ := os.Getwd()
	manager := session.NewManager(workDir)

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
	approval := NewApprovalView()
	diffView := NewDiffView()
	palette := NewCommandPalette()
	layout := NewLayout(chat, scrollbar, input, status, approval, diffView, palette)
	a.layout = layout
	status.SetDefault(cfg.Model, session.StatusIdle)

	a.setupPalette()

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
		layout.Status.SetSelActive(chat.HasSelection())
		return event, action
	})
	a.tapp.SetInputCapture(a.handleGlobalKey)
	a.tapp.SetRoot(layout.Root, true).SetFocus(input.TextArea)

	return a
}

// Run starts the event loop.
func (a *App) Run() error {
	if len(a.cfg.Endpoints) == 0 {
		a.layout.Status.SetError("no endpoint configured — press Ctrl+P to add one")
	} else if a.cfg.Model == "" {
		a.layout.Status.SetError("no model selected — press Ctrl+P to select one")
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
	// When the approval bar is active, Tab/Left/Right cycle Allow/Deny and Esc denies.
	// SetFocus re-triggers Focus() delegation so denyField gets real focus when deny is active.
	if a.layout.Approval.IsVisible() {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			if a.layout.Approval.GetSelected() == "allow" {
				a.layout.Approval.SetSelected("deny")
			} else {
				a.layout.Approval.SetSelected("allow")
			}
			a.tapp.SetFocus(a.layout.Approval)
			return nil
		case tcell.KeyEscape:
			a.layout.Approval.Deny("")
			return nil
		}
	}

	// When the diff view is active, Tab/Left/Right cycle Allow/Deny and Esc denies.
	if a.layout.DiffView.IsVisible() {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			if a.layout.DiffView.GetSelected() == "allow" {
				a.layout.DiffView.SetSelected("deny")
			} else {
				a.layout.DiffView.SetSelected("allow")
			}
			a.tapp.SetFocus(a.layout.DiffView)
			return nil
		case tcell.KeyEscape:
			a.layout.DiffView.Deny("")
			return nil
		}
	}

	// When palette is open, intercept navigation keys; text input falls through
	// to the focused InputField (queryField or active form field).
	if a.layout.Palette.IsVisible() {
		p := a.layout.Palette
		switch event.Key() {
		case tcell.KeyCtrlP, tcell.KeyEscape:
			if p.GetMode() != paletteModeMenu {
				p.SwitchMode(paletteModeMenu)
				a.tapp.SetFocus(p)
			} else {
				p.Close()
			}
			return nil
		case tcell.KeyEnter:
			p.Confirm()
			if p.IsVisible() {
				a.tapp.SetFocus(p)
			}
			return nil
		case tcell.KeyUp:
			if p.GetMode() == paletteModeAddEndpoint {
				p.PrevFormField()
				a.tapp.SetFocus(p)
			} else {
				p.NavigateUp()
			}
			return nil
		case tcell.KeyDown:
			if p.GetMode() == paletteModeAddEndpoint {
				p.NextFormField()
				a.tapp.SetFocus(p)
			} else {
				p.NavigateDown()
			}
			return nil
		case tcell.KeyTab:
			if p.GetMode() == paletteModeAddEndpoint {
				p.NextFormField()
				a.tapp.SetFocus(p)
				return nil
			}
		case tcell.KeyBacktab:
			if p.GetMode() == paletteModeAddEndpoint {
				p.PrevFormField()
				a.tapp.SetFocus(p)
				return nil
			}
		}
		return event
	}

	switch {
	case event.Key() == tcell.KeyCtrlP:
		a.openPalette()
		return nil

	case event.Key() == tcell.KeyCtrlD:
		a.Stop()
		return nil

	case event.Key() == tcell.KeyCtrlC:
		if a.layout.Chat.HasSelection() {
			// Active drag selection — copy it.
			text := a.layout.Chat.SelectedText()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
					a.layout.Status.SetError(err.Error())
				}
			}
		} else if a.sendCancel != nil {
			// Agent is running — interrupt it.
			a.sendCancel()
			a.sendCancel = nil
			if a.layout.Approval.IsVisible() {
				a.layout.HideApproval()
			}
			if a.layout.DiffView.IsVisible() {
				a.layout.HideDiffView()
			}
			a.tapp.SetFocus(a.layout.Input.TextArea)
			if sess, ok := a.manager.Active(); ok {
				sess.SetStatus(session.StatusIdle)
				a.redrawActive()
			}
		} else {
			// Agent is idle, no selection — copy hovered message.
			text := a.layout.Chat.HoveredContent()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
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
		case agent.EventToolApproval:
			respCh := ev.RespCh
			tool := ev.Tool
			a.tapp.QueueUpdateDraw(func() {
				msgs, status := sess.Snapshot()
				a.layout.Chat.Render(msgs)
				a.layout.Status.SetDefault(a.cfg.Model, status)

				if tool.DiffPatch != "" {
					// Show the diff approval view for write_file / edit_file.
					files := []DiffFileChange{{
						Path:  tool.FilePath,
						Lines: ParseUnifiedDiff(tool.DiffPatch),
					}}
					a.layout.DiffView.Show(tool.Name, tool.Reasoning, files)
					a.layout.ShowDiffView()
					a.tapp.SetFocus(a.layout.DiffView)
					a.layout.DiffView.SetCallbacks(
						func() {
							a.layout.HideDiffView()
							a.tapp.SetFocus(a.layout.Input.TextArea)
							respCh <- agent.ApprovalResult{Allowed: true}
						},
						func(reason string) {
							a.layout.HideDiffView()
							a.tapp.SetFocus(a.layout.Input.TextArea)
							respCh <- agent.ApprovalResult{Allowed: false, DenyReason: reason}
						},
					)
				} else {
					a.layout.Approval.Show(tool.Name, tool.Input, tool.Reasoning)
					a.layout.ShowApproval()
					a.tapp.SetFocus(a.layout.Approval)
					a.layout.Approval.SetCallbacks(
						func() {
							a.layout.HideApproval()
							a.tapp.SetFocus(a.layout.Input.TextArea)
							respCh <- agent.ApprovalResult{Allowed: true}
						},
						func(reason string) {
							a.layout.HideApproval()
							a.tapp.SetFocus(a.layout.Input.TextArea)
							respCh <- agent.ApprovalResult{Allowed: false, DenyReason: reason}
						},
					)
				}
			})

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

// setupPalette wires palette callbacks.
func (a *App) setupPalette() {
	p := a.layout.Palette
	p.SetCallbacks(
		// onClose
		func() {
			a.layout.HidePalette()
			a.tapp.SetFocus(a.layout.Input.TextArea)
		},
		// onAddEndpoint
		func(name, baseURL, apiKey string) {
			a.cfg.Endpoints = append(a.cfg.Endpoints, config.Endpoint{
				Name:    name,
				BaseURL: baseURL,
				APIKey:  apiKey,
			})
			if err := a.cfg.Save(); err != nil {
				a.layout.Status.SetError("save failed: " + err.Error())
			} else {
				a.layout.Status.SetMessage(fmt.Sprintf("endpoint %q added", name))
			}
		},
		// onDelEndpoint
		func(name string) {
			eps := a.cfg.Endpoints
			for i, ep := range eps {
				if ep.Name == name {
					a.cfg.Endpoints = append(eps[:i], eps[i+1:]...)
					break
				}
			}
			if err := a.cfg.Save(); err != nil {
				a.layout.Status.SetError("save failed: " + err.Error())
			} else {
				a.layout.Status.SetMessage(fmt.Sprintf("endpoint %q removed", name))
			}
		},
		// onSelectModel — value is "endpointName\x00modelName"
		func(val string) {
			epName, model, _ := strings.Cut(val, "\x00")
			a.cfg.ActiveEndpointName = epName
			a.cfg.Model = model
			var ep config.Endpoint
			for _, e := range a.cfg.Endpoints {
				if e.Name == epName {
					ep = e
					break
				}
			}
			a.ag = agent.New(ep.BaseURL, ep.APIKey, model)
			if err := a.cfg.Save(); err != nil {
				a.layout.Status.SetError("save failed: " + err.Error())
			}
			a.layout.Status.SetDefault(model, session.StatusIdle)
		},
		// getEndpoints
		func() []paletteEndpointInfo {
			eps := a.cfg.Endpoints
			out := make([]paletteEndpointInfo, len(eps))
			for i, ep := range eps {
				out[i] = paletteEndpointInfo{Name: ep.Name, BaseURL: ep.BaseURL}
			}
			return out
		},
	)

}

func (a *App) openPalette() {
	p := a.layout.Palette
	// Wire actions onto menuItems, then Open() picks them up via cp.items = cp.menuItems.
	p.menuItems = topLevelItems()
	p.menuItems[0].Action = func() { p.switchMode(paletteModeAddEndpoint) }
	p.menuItems[1].Action = func() { p.switchMode(paletteModeDelEndpoint) }
	p.menuItems[2].Action = func() {
		p.switchMode(paletteModeSelectModel)
		go func() {
			var items []PaletteItem
			for _, ep := range a.cfg.Endpoints {
				ag := agent.New(ep.BaseURL, ep.APIKey, "")
				models, _ := ag.ListModels(a.appCtx)
				for _, m := range models {
					items = append(items, PaletteItem{
						Label: m,
						Sub:   ep.Name,
						Value: ep.Name + "\x00" + m,
					})
				}
			}
			if len(items) == 0 {
				items = []PaletteItem{{Label: "no models found"}}
			}
			a.tapp.QueueUpdateDraw(func() {
				a.layout.Palette.SetModelItems(items)
			})
		}()
	}
	p.Open() // sets visible=true, cp.items=cp.menuItems, refilters
	a.layout.ShowPalette()
	a.tapp.SetFocus(p)
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
