package ui

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/config"
	"github.com/aleksanaa/hyphae/internal/controller"
	"github.com/aleksanaa/hyphae/internal/session"
)

// App is the root application coordinator.
type App struct {
	tapp     *tview.Application
	layout   *Layout
	ctrl     *controller.Controller
	cfg      *config.Config
	shutdown func() // cancels the controller context and closes the store
}

// New wires up and returns a ready-to-run App.
func New(cfg *config.Config) *App {
	ctrl, shutdown := controller.NewFromConfig(cfg)

	a := &App{
		tapp:     tview.NewApplication(),
		cfg:      cfg,
		ctrl:     ctrl,
		shutdown: shutdown,
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
	if cfg.ContextWindow > 0 {
		status.SetContextWindow(cfg.ContextWindow)
	}
	if cfg.Model != "" && (cfg.ContextWindow == 0 || cfg.InputPrice == 0) {
		go ctrl.FetchModelDevInfoAsync(ctrl.Context(), cfg.Model)
	}

	a.setupPalette()

	chat.SetStatusExpandCallback(func(sessionIdx int) {
		if sess, ok := ctrl.ActiveSession(); ok {
			sess.ToggleThinkingExpanded(sessionIdx)
			a.redrawActive()
		}
	})
	chat.SetToolGroupExpandCallback(func(sessionIdx int) {
		if sess, ok := ctrl.ActiveSession(); ok {
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

	go func() {
		for ev := range ctrl.Events() {
			ev := ev
			a.tapp.QueueUpdateDraw(func() { a.handleControllerEvent(ev) })
		}
	}()

	return a
}

// Run starts the event loop.
func (a *App) Run() error {
	if len(a.cfg.Endpoints) == 0 {
		a.layout.Status.SetError("no endpoint configured — press Ctrl+P to add one")
	} else if a.cfg.Model == "" {
		a.layout.Status.SetError("no model selected — press Ctrl+P to select one")
	}

	defer a.shutdown()
	return a.tapp.Run()
}

// Stop shuts down the application.
func (a *App) Stop() {
	a.shutdown()
	a.tapp.Stop()
}

// handleControllerEvent dispatches a controller event to the appropriate UI update.
// Always called from within QueueUpdateDraw, i.e. on the tview event loop.
func (a *App) handleControllerEvent(ev controller.Event) {
	isActive := a.ctrl.ActiveID() == ev.SessionID
	sess, hasSess := a.ctrl.Manager().Get(ev.SessionID)

	switch ev.Kind {
	case controller.EvRedraw:
		if isActive {
			a.redrawActive()
		}

	case controller.EvStatusMsg:
		if isActive {
			a.layout.Status.SetMessage(ev.Text)
		}

	case controller.EvStatusErr:
		if isActive {
			a.layout.Status.SetError("error: " + ev.Text)
		}

	case controller.EvTokensUpdate:
		if isActive {
			a.layout.Status.SetPromptTokens(ev.PromptTokens)
			a.layout.Status.SetSessionCost(ev.SessionCost)
		}

	case controller.EvContextWindow:
		if isActive {
			a.layout.Status.SetContextWindow(ev.ContextWindow)
		}

	case controller.EvConnecting:
		if !isActive || !hasSess {
			break
		}
		var text string
		if ev.RetryAttempt > 0 {
			text = fmt.Sprintf(
				"[%s]connecting to [%s]apex[-][%s] model... (%ds, retrying %d/%d in %ds)[-]",
				TC.Muted, TC.ApexDim, TC.Muted, ev.Elapsed, ev.RetryAttempt+1, ev.MaxAttempts, ev.RetryRemaining)
		} else {
			text = fmt.Sprintf(
				"[%s]connecting to [%s]apex[-][%s] model... (%ds)[-]",
				TC.Muted, TC.ApexDim, TC.Muted, ev.Elapsed)
		}
		sess.UpdateStatus(text)
		a.redrawActive()

	case controller.EvThinkingUpdate:
		if !isActive || !hasSess {
			break
		}
		secs := ev.ThinkingSecs
		sess.UpdateStatus(fmt.Sprintf(
			"[%s]apex[-][%s] is thinking... (%ds)[-]", TC.ApexDim, TC.Muted, secs))
		a.redrawActive()

	case controller.EvThinkingDone:
		if !hasSess {
			break
		}
		if ev.ThinkingSecs < 0 {
			sess.UpdateStatus("")
		} else {
			secs := ev.ThinkingSecs
			var label string
			if secs < 1 {
				label = fmt.Sprintf("[%s]apex[-][%s] thought for a moment[-]", TC.ApexDim, TC.Muted)
			} else {
				label = fmt.Sprintf("[%s]apex[-][%s] thought for %ds[-]", TC.ApexDim, TC.Muted, secs)
			}
			sess.FinalizeThinkingStatus(label, secs)
		}
		if isActive {
			a.redrawActive()
		}

	case controller.EvDone:
		if isActive {
			a.redrawActive()
		}

	case controller.EvError:
		if !isActive {
			break
		}
		msgs, _ := sess.Snapshot()
		a.layout.Chat.Render(msgs)
		a.layout.Status.SetError(ev.Text)

	case controller.EvToolApproval:
		tool := ev.Tool
		respCh := ev.RespCh
		msgs, status := sess.Snapshot()
		a.layout.Chat.Render(msgs)
		a.layout.Status.SetDefault(a.cfg.Model, status)

		if tool.DiffPatch != "" {
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
					respCh <- controller.ApprovalResult{Allowed: true}
				},
				func(reason string) {
					a.layout.HideDiffView()
					a.tapp.SetFocus(a.layout.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: false, DenyReason: reason}
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
					respCh <- controller.ApprovalResult{Allowed: true}
				},
				func(reason string) {
					a.layout.HideApproval()
					a.tapp.SetFocus(a.layout.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: false, DenyReason: reason}
				},
			)
		}

	case controller.EvSelectPrompt:
		tool := ev.Tool
		respCh := ev.SelectRespCh
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
	}
}

// handleGlobalKey intercepts application-level shortcuts.
func (a *App) handleGlobalKey(event *tcell.EventKey) *tcell.EventKey {
	if a.layout.SelectView.IsVisible() {
		switch event.Key() {
		case tcell.KeyEscape:
			a.layout.SelectView.Cancel()
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			return nil
		}
	}

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
			text := a.layout.Chat.SelectedText()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
					a.layout.Status.SetError(err.Error())
				}
			}
		} else if sel, _, _ := a.layout.Input.GetSelection(); sel != "" {
			if err := clipboard.WriteAll(sel); err != nil {
				a.layout.Status.SetError(err.Error())
			}
		} else if a.ctrl.IsRunning() {
			a.ctrl.Cancel()
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
			a.redrawActive()
		} else {
			text := a.layout.Chat.HoveredContent()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
					a.layout.Status.SetError(err.Error())
				}
			}
		}
		return nil

	case event.Key() == tcell.KeyTab:
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
// Called from the TextArea input capture; runs on the tview event loop.
func (a *App) sendMessage(text string) {
	a.ctrl.SendMessage(text)
	// Immediate re-render from within the event loop; EvRedraw from the agent
	// goroutine will handle subsequent updates.
	a.redrawActive()
	a.layout.Status.SetDefault(a.cfg.Model, session.StatusRunning)
}

// compactConversation runs the compact workflow via the controller.
func (a *App) compactConversation() {
	if err := a.ctrl.Compact(); err != nil {
		a.layout.Status.SetError(err.Error())
		return
	}
	a.redrawActive()
}

// resumeSession loads and activates a session, then updates the status bar.
func (a *App) resumeSession(id string) {
	sess, info, err := a.ctrl.ResumeSession(id)
	if err != nil {
		return
	}
	_ = sess
	a.layout.Status.SetSessionCost(info.TotalCost)
	a.layout.Status.SetPromptTokens(info.PromptTokens)
	if info.ContextWindow > 0 && a.cfg.ContextWindow == 0 {
		a.layout.Status.SetContextWindow(info.ContextWindow)
	}
	a.redrawActive()
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
			if err := a.ctrl.AddEndpoint(name, baseURL, apiKey); err != nil {
				a.layout.Status.SetError("save failed: " + err.Error())
			} else {
				a.layout.Status.SetMessage(fmt.Sprintf("endpoint %q added", name))
			}
		},
		// onDelEndpoint
		func(name string) {
			if err := a.ctrl.RemoveEndpoint(name); err != nil {
				a.layout.Status.SetError("save failed: " + err.Error())
			} else {
				a.layout.Status.SetMessage(fmt.Sprintf("endpoint %q removed", name))
			}
		},
		// onSelectModel — value is "endpointName\x00modelName\x00contextWindow"
		func(val string) {
			parts := strings.SplitN(val, "\x00", 3)
			if len(parts) < 2 {
				return
			}
			epName, model := parts[0], parts[1]
			var cw int64
			if len(parts) == 3 {
				fmt.Sscanf(parts[2], "%d", &cw)
			}
			a.ctrl.SwitchModel(epName, model, cw)
			a.layout.Status.SetDefault(model, session.StatusIdle)
		},
		// onResumeSession
		func(id string) {
			a.layout.HidePalette()
			a.tapp.SetFocus(a.layout.Input.TextArea)
			a.resumeSession(id)
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
		// getSessions
		func() []paletteSessionInfo {
			rows, err := a.ctrl.ListSessions()
			if err != nil {
				return nil
			}
			out := make([]paletteSessionInfo, len(rows))
			for i, r := range rows {
				out[i] = paletteSessionInfo{ID: r.ID, Title: r.Title, UpdatedAt: r.UpdatedAt}
			}
			return out
		},
		// getHotkeyItems
		func() []PaletteItem {
			p := a.layout.Palette
			return []PaletteItem{
				{
					Label:  "Ctrl+P",
					Sub:    "command palette",
					Action: func() { p.Close() },
				},
				{
					Label: "Tab",
					Sub:   "toggle focus: input ↔ chat",
					Action: func() {
						p.Close()
						if a.tapp.GetFocus() == a.layout.Input.TextArea {
							a.tapp.SetFocus(a.layout.Chat.TextView)
						} else {
							a.tapp.SetFocus(a.layout.Input.TextArea)
						}
					},
				},
				{
					Label:  "Escape",
					Sub:    "focus input",
					Action: func() { p.Close() },
				},
				{
					Label:  "Ctrl+C",
					Sub:    "copy message (idle) / interrupt agent (running)",
					Action: func() { p.Close() },
				},
				{
					Label:  "Ctrl+D",
					Sub:    "quit",
					Action: func() { p.Close(); a.Stop() },
				},
			}
		},
	)
}

func (a *App) openPalette() {
	p := a.layout.Palette
	p.menuItems = topLevelItems()
	p.menuItems[0].Action = func() { p.switchMode(paletteModeResumeSession) }
	p.menuItems[1].Action = func() { p.Close(); a.compactConversation() }
	p.menuItems[2].Action = func() { p.switchMode(paletteModeAddEndpoint) }
	p.menuItems[3].Action = func() { p.switchMode(paletteModeDelEndpoint) }
	p.menuItems[5].Action = func() { p.switchMode(paletteModeHotkeys) }
	p.menuItems[4].Action = func() {
		p.switchMode(paletteModeSelectModel)
		go func() {
			var items []PaletteItem
			for _, ep := range a.cfg.Endpoints {
				models, _ := a.ctrl.ListModels(a.ctrl.Context(), ep)
				for _, m := range models {
					items = append(items, PaletteItem{
						Label: m.ID,
						Sub:   ep.Name,
						Value: fmt.Sprintf("%s\x00%s\x00%d", ep.Name, m.ID, m.ContextWindow),
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
	p.Open()
	a.layout.ShowPalette()
	a.tapp.SetFocus(p)
}

// redrawActive refreshes the chat and status for the current session.
// Must only be called from the tview event loop.
func (a *App) redrawActive() {
	sess, ok := a.ctrl.ActiveSession()
	if !ok {
		return
	}
	msgs, status := sess.Snapshot()
	summary, seqs := sess.GetCompact()
	a.layout.Chat.SetCompact(summary, seqs)
	a.layout.Chat.Render(msgs)
	a.layout.Status.SetDefault(a.cfg.Model, status)
}
