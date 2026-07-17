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
//
// Tabs are owned entirely by the UI: the App keeps the ordered tab list and
// which tab is shown. The session manager is an unordered set and knows nothing
// about tab arrangement. activeTabID is the shown tab; for a session tab it is
// kept in sync with the controller's active session (which routes events), and
// for a non-session tab the controller has no active session.
type App struct {
	tapp        *tview.Application
	layout      *Layout
	ctrl        *controller.Controller
	cfg         *config.Config
	shutdown    func() // cancels the controller context and closes the store
	tabs        []*Tab // ordered tab strip; UI-owned arrangement
	tabByID     map[string]*Tab
	activeTabID string
}

// activeContent returns the TabContent for the active tab, or nil if the active
// tab is not backed by a session.
func (a *App) activeContent() *TabContent {
	return a.sessionContent(a.activeTabID)
}

// sessionContent returns the TabContent for tab id if it is a session tab, else nil.
func (a *App) sessionContent(id string) *TabContent {
	t := a.tabByID[id]
	if t == nil {
		return nil
	}
	if st, ok := t.body.(*sessionTab); ok {
		return st.tc
	}
	return nil
}

// tabIndex returns the position of tab id in the ordered list, or -1.
func (a *App) tabIndex(id string) int {
	for i, t := range a.tabs {
		if t.id == id {
			return i
		}
	}
	return -1
}

// newTabContent creates a fully wired TabContent for a new session tab.
func (a *App) newTabContent() *TabContent {
	tc := &TabContent{}

	tc.Chat = NewChatView()
	tc.Scrollbar = NewScrollbar(
		func() int { return tc.Chat.TotalLines },
		func() int { _, _, _, h := tc.Chat.GetInnerRect(); return h },
		func() int { y, _ := tc.Chat.GetScrollOffset(); return y },
		func(y int) { tc.Chat.ScrollTo(y, 0) },
	)
	tc.Status = NewStatusBar()
	tc.Status.SetStatusClickFunc(func() { a.openPalette() })
	tc.Status.SetModelClickFunc(func() { a.openModelSelect() })
	tc.Input = NewInputView(func(text string) {
		a.ctrl.SendMessage(text)
		a.redrawActive()
	})
	tc.Approval = NewApprovalView()
	tc.DiffView = NewDiffView()
	tc.SelectView = NewSelectView()
	tc.PlanMode = NewPlanModeView(func() { a.togglePlanMode() })

	tc.Chat.SetStatusExpandCallback(func(sessionIdx int) {
		if sess, ok := a.ctrl.ActiveSession(); ok {
			sess.ToggleExpanded(sessionIdx)
			a.redrawActive()
		}
	})

	tc.Chat.TextView.SetFocusFunc(func() { tc.Chat.SetFocused(true) })
	tc.Chat.TextView.SetBlurFunc(func() { tc.Chat.SetFocused(false) })
	tc.Input.TextArea.SetFocusFunc(func() { tc.Input.SetFocused(true) })
	tc.Input.TextArea.SetBlurFunc(func() { tc.Input.SetFocused(false) })
	tc.Input.TextArea.SetChangedFunc(func() { tc.Status.Reset() })

	chatRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(tc.Chat, 0, 1, false).
		AddItem(tc.Scrollbar, 1, 0, false)

	tc.body = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(chatRow, 0, 1, false).
		AddItem(tc.Approval, 0, 0, false).
		AddItem(tc.DiffView, 0, 0, false).
		AddItem(tc.SelectView, 0, 0, false).
		AddItem(tc.PlanMode, 0, 0, false).
		AddItem(tc.Input, 6, 0, true)

	tc.Root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tc.body, 0, 1, true).
		AddItem(tc.Status, 1, 0, false)

	return tc
}

// persistActive fires off a background persist of the current session's messages.
func (a *App) persistActive() {
	if sess, ok := a.ctrl.ActiveSession(); ok {
		id := sess.ID
		go a.ctrl.PersistSession(sess, a.ctrl.SessionCost(id), a.ctrl.LastPromptTokens(id))
	}
}

// addTab inserts a tab at the front of the strip and registers its body page.
func (a *App) addTab(t *Tab) {
	a.tabs = append([]*Tab{t}, a.tabs...)
	a.tabByID[t.id] = t
	a.layout.AddTab(t.id, t.body.Root())
}

// registerSessionTab wires a TabContent for a session and adds it as a tab.
// Callers set status and switch to the tab.
func (a *App) registerSessionTab(sess *session.Session) *TabContent {
	tc := a.newTabContent()
	a.addTab(&Tab{id: sess.ID, body: &sessionTab{sess: sess, tc: tc}})
	return tc
}

// openNewTab creates a fresh session, registers its tab with default status,
// and returns the new session ID. The caller is responsible for switching to it.
func (a *App) openNewTab() string {
	sess := a.ctrl.NewSession()
	tc := a.registerSessionTab(sess)
	tc.Status.SetDefault(a.cfg.Model, session.StatusIdle)
	a.seedContextWindow(tc, 0)
	return sess.ID
}

// seedContextWindow applies the best-known context window to a freshly created
// tab's status bar so the usage bar appears immediately: the session's stored
// value (stored), falling back to the controller's current-model value. This
// matters because EvContextWindow only fires when the value changes, so a tab
// opened after the initial models.dev fetch would otherwise never receive it.
// Returns true if a value was applied.
func (a *App) seedContextWindow(tc *TabContent, stored int64) bool {
	cw := stored
	if cw == 0 {
		cw = a.ctrl.ContextWindow()
	}
	if cw > 0 {
		tc.Status.SetContextWindow(cw)
		return true
	}
	return false
}

// New wires up and returns a ready-to-run App.
func New(cfg *config.Config) *App {
	ctrl, shutdown := controller.NewFromConfig(cfg)

	a := &App{
		tapp:     tview.NewApplication(),
		cfg:      cfg,
		ctrl:     ctrl,
		shutdown: shutdown,
		tabByID:  make(map[string]*Tab),
	}

	palette := NewCommandPalette()
	tabs := NewTabBar(
		func(id string) { a.switchTab(id) },
		func(id string) { a.closeTab(id) },
		func() { a.newSession() },
		func(id string, insertAt int) { a.reorderTab(id, insertAt); a.syncTabs() },
	)
	layout := NewLayout(tabs, palette)
	a.layout = layout

	a.setupPalette()

	id := a.openNewTab()
	a.activeTabID = id
	layout.ShowTab(id)
	a.syncTabs()

	if cfg.Model != "" {
		go ctrl.FetchModelDevInfoAsync(ctrl.Context(), cfg.Model)
	}

	a.tapp.EnableMouse(true)
	a.tapp.SetMouseCapture(func(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
		atc := a.activeContent()
		if atc != nil {
			_, iy, _, ih := atc.Chat.GetInnerRect()
			_, my := event.Position()
			if my < iy || my >= iy+ih {
				atc.Chat.ClearHover()
			}
			atc.Status.SetSelActive(atc.Chat.HasSelection())
		}
		return event, action
	})
	a.tapp.SetInputCapture(a.handleGlobalKey)
	a.tapp.SetRoot(layout.Root, true).SetFocus(a.sessionContent(id).Input.TextArea)

	go func() {
		for ev := range ctrl.Events() {
			a.tapp.QueueUpdateDraw(func() { a.handleControllerEvent(ev) })
		}
	}()

	return a
}

// Run starts the event loop.
func (a *App) Run() error {
	if tc := a.activeContent(); tc != nil {
		if len(a.cfg.Endpoints) == 0 {
			tc.Status.SetError("no endpoint configured — press Ctrl+P to add one")
		} else if a.cfg.Model == "" {
			tc.Status.SetError("no model selected — press Ctrl+P to select one")
		}
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

	// tc is the active tab's content; only valid when isActive.
	var tc *TabContent
	if isActive {
		tc = a.activeContent()
	}

	switch ev.Kind {
	case controller.EvRedraw:
		if isActive {
			a.redrawActive()
		}

	case controller.EvStatusMsg:
		if isActive && tc != nil {
			tc.Status.SetMessage(ev.Text)
		}

	case controller.EvStatusErr:
		if isActive && tc != nil {
			tc.Status.SetError("error: " + ev.Text)
		}

	case controller.EvTokensUpdate:
		if isActive && tc != nil {
			tc.Status.SetPromptTokens(ev.PromptTokens)
			tc.Status.SetSessionCost(ev.SessionCost)
		}

	case controller.EvContextWindow:
		if atc := a.activeContent(); atc != nil {
			atc.Status.SetContextWindow(ev.ContextWindow)
		}

	case controller.EvConnecting:
		if !isActive || !hasSess || tc == nil {
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
		tc.Chat.SetLiveStatus(text)
		a.redrawActive()

	case controller.EvThinkingUpdate:
		if !isActive || !hasSess || tc == nil {
			break
		}
		tc.Chat.SetLiveStatus(fmt.Sprintf(
			"[%s]apex[-][%s] is thinking... (%ds)[-]", TC.ApexDim, TC.Muted, ev.ThinkingSecs))
		a.redrawActive()

	case controller.EvThinkingDone:
		if isActive && tc != nil {
			tc.Chat.SetLiveStatus("")
			a.redrawActive()
		}

	case controller.EvDone:
		if isActive {
			a.redrawActive()
		} else {
			// Background session finished — clear its running dot.
			a.syncTabs()
		}

	case controller.EvError:
		if !isActive || tc == nil || !hasSess {
			break
		}
		msgs, _ := sess.Snapshot()
		tc.Chat.Render(msgs)
		tc.Status.SetError(ev.Text)

	case controller.EvToolApproval:
		if !isActive || tc == nil || !hasSess {
			break
		}
		tool := ev.Tool
		respCh := ev.RespCh
		msgs, status := sess.Snapshot()
		tc.Chat.Render(msgs)
		tc.Status.SetDefault(a.cfg.Model, status)

		if tool.DiffPatch != "" {
			files := []DiffFileChange{{
				Path:  tool.FilePath,
				Lines: ParseUnifiedDiff(tool.DiffPatch),
			}}
			tc.DiffView.Show(tool.Name, tool.Reasoning, files)
			tc.ShowDiffView()
			a.tapp.SetFocus(tc.DiffView)
			tc.DiffView.SetCallbacks(
				func() {
					tc.HideDiffView()
					a.tapp.SetFocus(tc.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: true}
				},
				func(reason string) {
					tc.HideDiffView()
					a.tapp.SetFocus(tc.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: false, DenyReason: reason}
				},
			)
		} else {
			tc.Approval.Show(tool.Name, tool.Input, tool.Reasoning)
			tc.ShowApproval()
			a.tapp.SetFocus(tc.Approval)
			tc.Approval.SetCallbacks(
				func() {
					tc.HideApproval()
					a.tapp.SetFocus(tc.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: true}
				},
				func(reason string) {
					tc.HideApproval()
					a.tapp.SetFocus(tc.Input.TextArea)
					respCh <- controller.ApprovalResult{Allowed: false, DenyReason: reason}
				},
			)
		}

	case controller.EvSelectPrompt:
		if !isActive || tc == nil || !hasSess {
			break
		}
		tool := ev.Tool
		respCh := ev.SelectRespCh
		msgs, status := sess.Snapshot()
		tc.Chat.Render(msgs)
		tc.Status.SetDefault(a.cfg.Model, status)

		tc.SelectView.Show(tool.SelectQuestion, tool.SelectOptions)
		_, _, chatW, _ := tc.Chat.GetInnerRect()
		tc.ShowSelect(tc.SelectView.Height(chatW + 1))
		a.tapp.SetFocus(tc.SelectView)
		tc.SelectView.SetCallback(func(answer string) {
			tc.HideSelect()
			a.tapp.SetFocus(tc.Input.TextArea)
			respCh <- answer
		})
	}
}

// handleGlobalKey intercepts application-level shortcuts.
func (a *App) handleGlobalKey(event *tcell.EventKey) *tcell.EventKey {
	tc := a.activeContent()

	if tc != nil && tc.SelectView.IsVisible() {
		switch event.Key() {
		case tcell.KeyEscape:
			tc.SelectView.Cancel()
			return nil
		case tcell.KeyTab, tcell.KeyBacktab:
			return nil
		}
	}

	if tc != nil && tc.Approval.IsVisible() {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			if tc.Approval.GetSelected() == "allow" {
				tc.Approval.SetSelected("deny")
			} else {
				tc.Approval.SetSelected("allow")
			}
			a.tapp.SetFocus(tc.Approval)
			return nil
		case tcell.KeyEscape:
			tc.Approval.Deny("")
			return nil
		}
	}

	if tc != nil && tc.DiffView.IsVisible() {
		switch event.Key() {
		case tcell.KeyTab, tcell.KeyBacktab:
			if tc.DiffView.GetSelected() == "allow" {
				tc.DiffView.SetSelected("deny")
			} else {
				tc.DiffView.SetSelected("allow")
			}
			a.tapp.SetFocus(tc.DiffView)
			return nil
		case tcell.KeyEscape:
			tc.DiffView.Deny("")
			return nil
		}
	}

	if a.layout.Palette.IsVisible() {
		p := a.layout.Palette
		switch event.Key() {
		case tcell.KeyCtrlP, tcell.KeyEscape:
			a.paletteBack()
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
	case event.Key() == tcell.KeyLeft && event.Modifiers()&tcell.ModAlt != 0:
		a.cycleTab(-1)
		return nil

	case event.Key() == tcell.KeyRight && event.Modifiers()&tcell.ModAlt != 0:
		a.cycleTab(1)
		return nil

	case event.Key() == tcell.KeyCtrlP:
		a.openPalette()
		return nil

	case event.Key() == tcell.KeyCtrlD:
		a.Stop()
		return nil

	case event.Key() == tcell.KeyCtrlC:
		if tc == nil {
			return nil
		}
		if tc.Chat.HasSelection() {
			text := tc.Chat.SelectedText()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
					tc.Status.SetError(err.Error())
				}
			}
		} else if sel, _, _ := tc.Input.GetSelection(); sel != "" {
			if err := clipboard.WriteAll(sel); err != nil {
				tc.Status.SetError(err.Error())
			}
		} else if a.ctrl.IsRunning() {
			a.ctrl.Cancel()
			if tc.Approval.IsVisible() {
				tc.HideApproval()
			}
			if tc.DiffView.IsVisible() {
				tc.HideDiffView()
			}
			if tc.SelectView.IsVisible() {
				tc.HideSelect()
			}
			a.tapp.SetFocus(tc.Input.TextArea)
			a.redrawActive()
		} else {
			text := tc.Chat.HoveredContent()
			if text != "" {
				if err := clipboard.WriteAll(text); err != nil {
					tc.Status.SetError(err.Error())
				}
			}
		}
		return nil

	case event.Key() == tcell.KeyTab:
		if tc == nil {
			return nil
		}
		if a.tapp.GetFocus() == tc.Input.TextArea {
			a.tapp.SetFocus(tc.Chat.TextView)
		} else {
			a.tapp.SetFocus(tc.Input.TextArea)
		}
		return nil

	case event.Key() == tcell.KeyEscape:
		if tc != nil {
			a.tapp.SetFocus(tc.Input.TextArea)
		}
		return nil
	}
	return event
}

// togglePlanMode flips plan mode for the active session and updates the indicator.
func (a *App) togglePlanMode() {
	sess, ok := a.ctrl.ActiveSession()
	if !ok {
		return
	}
	on := !sess.IsPlanMode()
	sess.SetPlanMode(on)
	a.ctrl.SaveSessionPlanMode(sess.ID, on)
	if tc := a.activeContent(); tc != nil {
		if on {
			tc.ShowPlanMode()
		} else {
			tc.HidePlanMode()
		}
	}
	a.redrawActive()
}

// compactConversation runs the compact workflow via the controller.
func (a *App) compactConversation() {
	if err := a.ctrl.Compact(); err != nil {
		if tc := a.activeContent(); tc != nil {
			tc.Status.SetError(err.Error())
		}
		return
	}
	a.redrawActive()
}

// resumeSession persists the current session (if any), then loads and activates
// a session from storage. If already open in a tab, just switches to it.
func (a *App) resumeSession(id string) {
	a.persistActive()

	// If already open, just switch to its existing tab.
	if _, exists := a.tabByID[id]; exists {
		a.switchTab(id)
		return
	}

	sess, info, err := a.ctrl.ResumeSession(id)
	if err != nil {
		return
	}

	// Restore per-session model if it differs from the current default.
	model := a.cfg.Model
	if info.Model != "" {
		a.ctrl.SwitchModel(info.ActiveEndpoint, info.Model, info.ContextWindow, info.InputPrice, info.OutputPrice)
		model = info.Model
	}

	tc := a.registerSessionTab(sess)
	tc.Status.SetDefault(model, session.StatusIdle)
	if !a.seedContextWindow(tc, info.ContextWindow) && info.Model == "" && model != "" {
		// Old session with no stored model and no known CW yet; fetch it for the current model.
		go a.ctrl.FetchModelDevInfoAsync(a.ctrl.Context(), model)
	}
	if info.PlanMode {
		tc.ShowPlanMode()
	}
	a.switchTab(sess.ID)
}

// newSession persists the current session (if any) and switches to a blank one.
func (a *App) newSession() {
	a.persistActive()
	a.switchTab(a.openNewTab())
}

// setupPalette wires palette callbacks.
func (a *App) setupPalette() {
	p := a.layout.Palette
	p.SetBackFunc(func() { a.paletteBack() })
	p.SetCallbacks(
		// onClose
		func() {
			a.layout.HidePalette()
			if tc := a.activeContent(); tc != nil {
				a.tapp.SetFocus(tc.Input.TextArea)
			}
		},
		// onAddEndpoint
		func(name, baseURL, apiKey string) {
			if err := a.ctrl.AddEndpoint(name, baseURL, apiKey); err != nil {
				if tc := a.activeContent(); tc != nil {
					tc.Status.SetError("save failed: " + err.Error())
				}
			} else {
				if tc := a.activeContent(); tc != nil {
					tc.Status.SetMessage(fmt.Sprintf("endpoint %q added", name))
				}
			}
		},
		// onDelEndpoint
		func(name string) {
			if err := a.ctrl.RemoveEndpoint(name); err != nil {
				if tc := a.activeContent(); tc != nil {
					tc.Status.SetError("save failed: " + err.Error())
				}
			} else {
				if tc := a.activeContent(); tc != nil {
					tc.Status.SetMessage(fmt.Sprintf("endpoint %q removed", name))
				}
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
			a.ctrl.SwitchModel(epName, model, cw, 0, 0)
			if tc := a.activeContent(); tc != nil {
				tc.Status.SetDefault(model, session.StatusIdle)
			}
		},
		// onResumeSession
		func(id string) {
			a.layout.HidePalette()
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
			activeID := a.ctrl.ActiveID()
			var out []paletteSessionInfo
			for _, r := range rows {
				if r.ID == activeID {
					continue
				}
				out = append(out, paletteSessionInfo{ID: r.ID, Title: r.Title, UpdatedAt: r.UpdatedAt})
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
						tc := a.activeContent()
						if tc == nil {
							return
						}
						if a.tapp.GetFocus() == tc.Input.TextArea {
							a.tapp.SetFocus(tc.Chat.TextView)
						} else {
							a.tapp.SetFocus(tc.Input.TextArea)
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
					Label:  "Alt+←/→",
					Sub:    "cycle tabs",
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
	p.menuItems[1].Action = func() { p.Close(); a.newSession() }
	p.menuItems[2].Action = func() { p.Close(); a.compactConversation() }
	p.menuItems[3].Action = func() { p.Close(); a.togglePlanMode() }
	p.menuItems[4].Action = func() { p.switchMode(paletteModeAddEndpoint) }
	p.menuItems[5].Action = func() { p.switchMode(paletteModeDelEndpoint) }
	p.menuItems[7].Action = func() { p.switchMode(paletteModeHotkeys) }
	p.menuItems[6].Action = a.enterSelectModel
	p.Open()
	a.layout.ShowPalette()
	a.tapp.SetFocus(p)
}

// enterSelectModel switches the (already open) palette into model-selection mode
// and loads the model list asynchronously. It backs both the palette's "Select
// model" menu entry and the status-bar model click.
func (a *App) enterSelectModel() {
	a.layout.Palette.switchMode(paletteModeSelectModel)
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

// openModelSelect opens the palette directly in model-selection mode.
func (a *App) openModelSelect() {
	a.openPalette()
	a.enterSelectModel()
}

// paletteBack steps the palette back one level: a sub-mode returns to the menu,
// and the menu closes. Shared by Esc and the backdrop click.
func (a *App) paletteBack() {
	p := a.layout.Palette
	if p.GetMode() != paletteModeMenu {
		p.SwitchMode(paletteModeMenu)
		a.tapp.SetFocus(p)
	} else {
		p.Close()
	}
}

// syncTabs refreshes the tab bar from the UI-owned tab list and active tab.
func (a *App) syncTabs() {
	tabs := make([]TabInfo, len(a.tabs))
	for i, t := range a.tabs {
		tabs[i] = TabInfo{
			ID:      t.id,
			Title:   t.body.Title(),
			Running: t.body.Running(),
		}
	}
	a.layout.Tabs.Sync(tabs, a.activeTabID)
}

// switchTab shows tab id and redraws. For a session tab it also makes the
// session active in the controller so events route to it; a non-session tab
// clears the controller's active session.
func (a *App) switchTab(id string) {
	t := a.tabByID[id]
	if t == nil {
		return
	}
	a.activeTabID = id
	a.layout.ShowTab(id)

	if st, ok := t.body.(*sessionTab); ok {
		a.ctrl.SwitchSession(id)
		st.tc.Status.SetSessionCost(a.ctrl.SessionCost(id))
		st.tc.Status.SetPromptTokens(a.ctrl.LastPromptTokens(id))
		a.tapp.SetFocus(st.tc.Input.TextArea)
	} else {
		a.ctrl.ClearActive()
		a.tapp.SetFocus(t.body.Root())
	}
	a.redrawActive()
}

// reorderTab moves tab id before position insertAt in the UI tab list. This is a
// purely visual rearrangement; the session manager is unaffected.
func (a *App) reorderTab(id string, insertAt int) {
	from := a.tabIndex(id)
	if from < 0 {
		return
	}
	if insertAt < 0 {
		insertAt = 0
	}
	if insertAt > len(a.tabs) {
		insertAt = len(a.tabs)
	}
	t := a.tabs[from]
	a.tabs = append(a.tabs[:from], a.tabs[from+1:]...)
	if insertAt > from {
		insertAt--
	}
	a.tabs = append(a.tabs[:insertAt], append([]*Tab{t}, a.tabs[insertAt:]...)...)
}

// removeTab drops tab id from the UI list, map, and layout.
func (a *App) removeTab(id string) {
	if i := a.tabIndex(id); i >= 0 {
		a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	}
	delete(a.tabByID, id)
	a.layout.RemoveTab(id)
}

// closeTab persists and removes a session tab, then focuses a neighbouring tab.
// If it was active and no tab remains, a fresh session is opened automatically.
func (a *App) closeTab(id string) {
	t := a.tabByID[id]
	if t == nil {
		return
	}
	wasActive := a.activeTabID == id
	idx := a.tabIndex(id)
	if st, ok := t.body.(*sessionTab); ok {
		a.ctrl.CloseSession(st.sess.ID)
	}
	a.removeTab(id)

	if !wasActive {
		a.redrawActive()
		return
	}
	if len(a.tabs) == 0 {
		a.switchTab(a.openNewTab())
		return
	}
	if idx >= len(a.tabs) {
		idx = len(a.tabs) - 1
	}
	a.switchTab(a.tabs[idx].id)
}

// cycleTab moves to the next (+1) or previous (-1) tab in visual order.
func (a *App) cycleTab(delta int) {
	if len(a.tabs) < 2 {
		return
	}
	i := a.tabIndex(a.activeTabID)
	if i < 0 {
		return
	}
	next := a.tabs[(i+delta+len(a.tabs))%len(a.tabs)]
	a.switchTab(next.id)
}

// redrawActive refreshes the chat and status for the current session.
// Must only be called from the tview event loop.
func (a *App) redrawActive() {
	a.syncTabs()
	tc := a.activeContent()
	if tc == nil {
		return
	}
	sess, ok := a.ctrl.ActiveSession()
	if !ok {
		return
	}
	msgs, status := sess.Snapshot()
	summary, seqs := sess.GetCompact()
	tc.Chat.SetCompact(summary, seqs)
	tc.Chat.Render(msgs)
	tc.Status.SetDefault(a.cfg.Model, status)
}
