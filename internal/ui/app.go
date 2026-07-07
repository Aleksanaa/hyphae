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
	tapp        *tview.Application
	layout      *Layout
	ctrl        *controller.Controller
	cfg         *config.Config
	shutdown    func() // cancels the controller context and closes the store
	tabContents map[string]*TabContent
}

// activeContent returns the TabContent for the currently active session, or nil.
func (a *App) activeContent() *TabContent {
	return a.tabContents[a.ctrl.ActiveID()]
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
	tc.Input = NewInputView(func(text string) {
		a.ctrl.SendMessage(text)
		a.redrawActive()
	})
	tc.Approval = NewApprovalView()
	tc.DiffView = NewDiffView()
	tc.SelectView = NewSelectView()

	tc.Chat.SetStatusExpandCallback(func(sessionIdx int) {
		if sess, ok := a.ctrl.ActiveSession(); ok {
			sess.ToggleThinkingExpanded(sessionIdx)
			a.redrawActive()
		}
	})
	tc.Chat.SetToolGroupExpandCallback(func(sessionIdx int) {
		if sess, ok := a.ctrl.ActiveSession(); ok {
			sess.ToggleToolGroupExpanded(sessionIdx)
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

// registerTab wires a TabContent for an existing session ID and registers it
// in both the app map and the layout. Callers set status and switch to the tab.
func (a *App) registerTab(id string) *TabContent {
	tc := a.newTabContent()
	a.tabContents[id] = tc
	a.layout.AddTabContent(id, tc)
	return tc
}

// openNewTab creates a fresh session, registers its tab with default status,
// and returns the new session ID. The caller is responsible for switching to it.
func (a *App) openNewTab() string {
	sess := a.ctrl.NewSession()
	tc := a.registerTab(sess.ID)
	tc.Status.SetDefault(a.cfg.Model, session.StatusIdle)
	if a.cfg.ContextWindow > 0 {
		tc.Status.SetContextWindow(a.cfg.ContextWindow)
	}
	return sess.ID
}

// New wires up and returns a ready-to-run App.
func New(cfg *config.Config) *App {
	ctrl, shutdown := controller.NewFromConfig(cfg)

	a := &App{
		tapp:        tview.NewApplication(),
		cfg:         cfg,
		ctrl:        ctrl,
		shutdown:    shutdown,
		tabContents: make(map[string]*TabContent),
	}

	palette := NewCommandPalette()
	tabs := NewTabBar(
		func(id string) { a.switchTab(id) },
		func(id string) { a.closeTab(id) },
		func() { a.newSession() },
		func(id string, insertAt int) { a.ctrl.ReorderSession(id, insertAt); a.syncTabs() },
	)
	layout := NewLayout(tabs, palette)
	a.layout = layout

	a.setupPalette()

	id := a.openNewTab()
	layout.ShowTab(id)
	a.syncTabs()

	if cfg.Model != "" && (cfg.ContextWindow == 0 || cfg.InputPrice == 0) {
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
	a.tapp.SetRoot(layout.Root, true).SetFocus(a.tabContents[id].Input.TextArea)

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
		if isActive && tc != nil {
			tc.Status.SetContextWindow(ev.ContextWindow)
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
		sess.UpdateStatus(text)
		a.redrawActive()

	case controller.EvThinkingUpdate:
		if !isActive || !hasSess || tc == nil {
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
	if _, exists := a.tabContents[id]; exists {
		a.switchTab(id)
		return
	}

	sess, info, err := a.ctrl.ResumeSession(id)
	if err != nil {
		return
	}

	tc := a.registerTab(sess.ID)
	tc.Status.SetDefault(a.cfg.Model, session.StatusIdle)
	if info.ContextWindow > 0 && a.cfg.ContextWindow == 0 {
		tc.Status.SetContextWindow(info.ContextWindow)
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
			a.ctrl.SwitchModel(epName, model, cw)
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
	p.menuItems[3].Action = func() { p.switchMode(paletteModeAddEndpoint) }
	p.menuItems[4].Action = func() { p.switchMode(paletteModeDelEndpoint) }
	p.menuItems[6].Action = func() { p.switchMode(paletteModeHotkeys) }
	p.menuItems[5].Action = func() {
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

// syncTabs refreshes the tab bar from the current in-memory session list.
func (a *App) syncTabs() {
	sessions := a.ctrl.OpenSessions()
	activeID := a.ctrl.ActiveID()
	tabs := make([]TabInfo, len(sessions))
	for i, s := range sessions {
		tabs[i] = TabInfo{
			ID:      s.ID,
			Title:   s.Title,
			Running: s.GetStatus().IsActive(),
		}
	}
	a.layout.Tabs.Sync(tabs, activeID)
}

// switchTab switches to an already-open session tab and redraws.
func (a *App) switchTab(id string) {
	if !a.ctrl.SwitchSession(id) {
		return
	}
	tc := a.tabContents[id]
	if tc == nil {
		return
	}
	a.layout.ShowTab(id)
	tc.Status.SetSessionCost(a.ctrl.SessionCost(id))
	tc.Status.SetPromptTokens(a.ctrl.LastPromptTokens(id))
	a.tapp.SetFocus(tc.Input.TextArea)
	a.redrawActive()
}

// closeTab persists and removes a session tab. If it was active and no other
// session remains, a new one is created automatically.
func (a *App) closeTab(id string) {
	wasActive := a.ctrl.ActiveID() == id
	a.ctrl.CloseSession(id)
	a.layout.RemoveTab(id)
	delete(a.tabContents, id)
	if wasActive {
		if _, ok := a.ctrl.ActiveSession(); !ok {
			a.openNewTab()
		}
		a.switchTab(a.ctrl.ActiveID())
	} else {
		a.redrawActive()
	}
}

// cycleTab moves to the next (+1) or previous (-1) tab.
func (a *App) cycleTab(delta int) {
	sessions := a.ctrl.OpenSessions()
	if len(sessions) < 2 {
		return
	}
	activeID := a.ctrl.ActiveID()
	for i, s := range sessions {
		if s.ID == activeID {
			next := sessions[(i+delta+len(sessions))%len(sessions)]
			a.switchTab(next.ID)
			return
		}
	}
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
