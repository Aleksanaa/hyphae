package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/config"
	"github.com/aleksanaa/hyphae/internal/session"
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

	// status indicators
	statusCancel    context.CancelFunc // cancels the active connecting timer goroutine
	connectStart    time.Time          // when the current connect attempt began
	thinkingPending bool               // true once reasoning_content has started streaming
	thinkingStart   time.Time          // when first reasoning chunk arrived
	thinkingFrozen  bool               // true once "thought for Xs" has been set for this turn
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
	scrollbar := NewScrollbar(
		func() int { return chat.TotalLines },
		func() int { _, _, _, h := chat.GetInnerRect(); return h },
		func() int { y, _ := chat.GetScrollOffset(); return y },
		func(y int) { chat.ScrollTo(y, 0) },
	)
	status := NewStatusBar()
	input := NewInputView(a.sendMessage)
	approval := NewApprovalView()
	diffView := NewDiffView()
	selectView := NewSelectView()
	palette := NewCommandPalette()
	layout := NewLayout(chat, scrollbar, input, status, approval, diffView, selectView, palette)
	a.layout = layout
	status.SetDefault(cfg.Model, session.StatusIdle)

	a.setupPalette()

	chat.SetStatusExpandCallback(func(sessionIdx int) {
		if sess, ok := a.manager.Active(); ok {
			sess.ToggleThinkingExpanded(sessionIdx)
			a.redrawActive()
		}
	})
	chat.SetToolGroupExpandCallback(func(sessionIdx int) {
		if sess, ok := a.manager.Active(); ok {
			sess.ToggleToolGroupExpanded(sessionIdx)
			a.redrawActive()
		}
	})

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
	// When the select view is active, intercept Escape (cancel) and Tab (prevent focus steal).
	if a.layout.SelectView.IsVisible() {
		switch event.Key() {
		case tcell.KeyEscape:
			a.layout.SelectView.Cancel()
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			return nil
		}
	}

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
			if a.layout.SelectView.IsVisible() {
				a.layout.HideSelect()
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

// stopCountdown cancels any running retry countdown goroutine.
func (a *App) stopCountdown() {
	if a.statusCancel != nil {
		a.statusCancel()
		a.statusCancel = nil
	}
}

// resetTurnState clears per-turn thinking/connecting state.
// Called at the start of each new agent turn.
func (a *App) resetTurnState() {
	a.stopCountdown()
	a.connectStart = time.Time{}
	a.thinkingPending = false
	a.thinkingFrozen = false
}

// startConnectingTimer ticks the session status every second until cancelled.
// retryAttempt > 0 means a retry countdown is active; the status then reads
// "connecting to apex model... (Xs, retrying N/M in Ys)".
func (a *App) startConnectingTimer(sess *session.Session, retryAttempt, maxAttempts int, retryDelay time.Duration) {
	a.stopCountdown()
	ctx, cancel := context.WithCancel(a.appCtx)
	a.statusCancel = cancel
	start := a.connectStart
	go func() {
		retryRemaining := int(retryDelay.Seconds())
		for {
			elapsed := int(time.Since(start).Seconds())
			var text string
			if retryAttempt > 0 {
				text = fmt.Sprintf(
					"[%s]connecting to [%s]apex[-][%s] model... (%ds, retrying %d/%d in %ds)[-]",
					TC.Muted, TC.ApexDim, TC.Muted, elapsed, retryAttempt+1, maxAttempts, retryRemaining)
			} else {
				text = fmt.Sprintf(
					"[%s]connecting to [%s]apex[-][%s] model... (%ds)[-]",
					TC.Muted, TC.ApexDim, TC.Muted, elapsed)
			}
			a.tapp.QueueUpdateDraw(func() {
				if ctx.Err() != nil {
					return
				}
				sess.UpdateStatus(text)
				a.redrawActive()
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

// finalizeStatus sets the status message for the current turn to its final value.
// If CoT was active, shows "apex thought for Xs". Otherwise clears it (hidden).
// No-op after the first call per turn.
func (a *App) finalizeStatus(sess *session.Session) {
	if a.thinkingFrozen {
		return
	}
	a.thinkingFrozen = true
	a.stopCountdown()
	if a.thinkingPending {
		secs := int(time.Since(a.thinkingStart).Seconds())
		var label string
		if secs < 1 {
			label = fmt.Sprintf("[%s]apex[-][%s] thought for a moment[-]", TC.ApexDim, TC.Muted)
		} else {
			label = fmt.Sprintf("[%s]apex[-][%s] thought for %ds[-]", TC.ApexDim, TC.Muted, secs)
		}
		sess.FinalizeThinkingStatus(label, secs)
		a.thinkingPending = false
	} else {
		sess.UpdateStatus("") // no CoT — connecting status disappears with no artifact
	}
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

	a.resetTurnState()
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
		case agent.EventSelectPrompt:
			respCh := ev.SelectRespCh
			tool := ev.Tool
			a.tapp.QueueUpdateDraw(func() {
				msgs, status := sess.Snapshot()
				a.layout.Chat.Render(msgs)
				a.layout.Status.SetDefault(a.cfg.Model, status)

				a.layout.SelectView.Show(tool.SelectQuestion, tool.SelectOptions)
				_, _, chatW, _ := a.layout.Chat.GetInnerRect()
				a.layout.ShowSelect(a.layout.SelectView.Height(chatW + 1))
				a.tapp.SetFocus(a.layout.SelectView)
				a.layout.SelectView.SetCallback(func(answer string) {
					a.layout.HideSelect()
					a.tapp.SetFocus(a.layout.Input.TextArea)
					respCh <- answer
				})
			})

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

		case agent.EventReasoningDelta:
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					if a.thinkingFrozen {
						return
					}
					if !a.thinkingPending {
						a.thinkingPending = true
						a.thinkingStart = time.Now()
						a.stopCountdown() // cancel connecting timer; reasoning has started
					}
					secs := int(time.Since(a.thinkingStart).Seconds())
					sess.UpdateStatus(fmt.Sprintf(
						"[%s]apex[-][%s] is thinking... (%ds)[-]", TC.ApexDim, TC.Muted, secs))
					a.redrawActive()
				})
			}

		case agent.EventTextDelta:
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					a.finalizeStatus(sess)
					a.redrawActive()
				})
			}

		case agent.EventConnecting:
			attempt, maxAttempts, retryAfter, connErr := ev.Attempt, ev.MaxAttempts, ev.RetryAfter, ev.Err
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					if attempt == 1 && retryAfter == 0 {
						// Brand new turn — reset all per-turn state.
						a.resetTurnState()
						a.connectStart = time.Now()
					}
					if retryAfter > 0 {
						// This attempt failed; surface the error and fold countdown into the timer.
						if connErr != nil {
							a.layout.Status.SetError(fmt.Sprintf("error: %s", connErr.Error()))
						}
						a.startConnectingTimer(sess, attempt, maxAttempts, retryAfter)
					} else {
						// A new attempt is starting; clear any error shown for the previous attempt.
						if attempt > 1 {
							a.layout.Status.SetDefault(a.cfg.Model, session.StatusRunning)
						}
						a.startConnectingTimer(sess, 0, 0, 0)
					}
				})
			}

		case agent.EventPreparingTool:
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					a.finalizeStatus(sess)
					a.redrawActive()
				})
			}

		case agent.EventToolStart, agent.EventToolDone:
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					a.redrawActive()
				})
			}

		case agent.EventDone:
			sess.SetStatus(session.StatusIdle)
			if isActive {
				a.tapp.QueueUpdateDraw(func() {
					a.finalizeStatus(sess)
					a.redrawActive()
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
					a.stopCountdown()
					sess.UpdateStatus("") // clear connecting/retrying message from chat
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
