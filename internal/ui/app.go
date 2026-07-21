package ui

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/sys/unix"

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
	tapp         *tview.Application
	layout       *Layout
	ctrl         *controller.Controller
	cfg          *config.Config
	shutdown     func() // cancels the controller context and closes the store
	tabs         []*Tab // ordered tab strip; UI-owned arrangement
	tabByID      map[string]*Tab
	activeTabID  string
	modelChoices []controller.Model // models shown in the last select-model listing
	suspending   atomic.Bool        // guards against overlapping suspend/resume cycles
	tstp         chan os.Signal     // catches stray SIGTSTP so only our controlled stop lands; see suspend
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
		// Auto-focus the message view on the latest message so it follows the one
		// just sent and the reply streaming in after it; the input box blurs.
		// Following ends when the user scrolls, clicks in the chat, or focuses the
		// input again (see ChatView.FollowLatest / StopFollow).
		tc.Chat.FollowLatest()
		a.tapp.SetFocus(tc.Chat.TextView)
		a.redrawActive()
	})
	tc.Approval = NewApprovalView()
	tc.DiffView = NewDiffView()
	tc.SelectView = NewSelectView()
	tc.PlanMode = NewPlanModeView(func() { a.togglePlanMode() })

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
		AddItem(tc.Input, InputHeightNormal, 0, true)

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
	// Apply the saved theme before any widget is constructed, since tview
	// snapshots colors at construction time.
	if cfg.Theme != "" {
		SetThemeByID(cfg.Theme)
	}

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
	// Adapt the input box height to the terminal height each frame.
	a.tapp.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		_, h := screen.Size()
		if tc := a.activeContent(); tc != nil {
			tc.SetInputHeightForScreen(h)
		}
		// Colour the hardware cursor from the active tint. tview drives the cursor
		// via screen.ShowCursor but never sets its colour; re-applying every frame
		// keeps it in sync across theme switches. CursorStyleDefault leaves the
		// terminal's cursor shape untouched. tcell restores the colour on exit.
		screen.SetCursorStyle(tcell.CursorStyleDefault, Theme.Cursor)
		return false
	})
	a.tapp.SetRoot(newMinSizeGate(layout.Root), true).SetFocus(a.sessionContent(id).Input.TextArea)

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

	// Catch SIGTSTP for the whole run. In raw mode the terminal delivers Ctrl+Z
	// as a key event (handled in handleGlobalKey), not a signal, so any SIGTSTP
	// that actually arrives here is a stray generated by the terminal driver
	// during the brief cooked-mode windows of a suspend/resume cycle. Swallowing
	// those keeps them from racing the Go runtime's job-control stop dance, which
	// at high Ctrl+Z rates could terminate the process with signal 20. Our own
	// suspend stops via SIGSTOP instead (see suspend), so it never depends on
	// SIGTSTP's disposition.
	a.tstp = make(chan os.Signal, 8)
	signal.Notify(a.tstp, syscall.SIGTSTP)
	go func() {
		for range a.tstp { //nolint:revive // intentionally drained and ignored
		}
	}()
	defer func() {
		signal.Stop(a.tstp)
		close(a.tstp)
	}()

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

	case controller.EvTitle:
		// A session's title was (re)generated; tab labels read sess.Title live,
		// so a tab-bar refresh is all that's needed.
		a.syncTabs()

	case controller.EvDone:
		if isActive {
			a.redrawActive()
			// Turn is idle: end follow but keep the last reply's focus border.
			if tc != nil {
				tc.Chat.SettleFollow()
			}
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
		tc.Status.SetStatus(status)

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
		tc.Status.SetStatus(status)

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

	// Ctrl+Z suspends to the background like an ordinary shell program. In raw
	// mode tcell delivers it as a key event instead of raising SIGTSTP, so we
	// raise it ourselves after restoring the terminal.
	//
	// Exception: when the message input is focused and non-empty, Ctrl+Z acts
	// as undo (the TextArea's built-in behavior). We forward the key to the
	// widget and check whether it actually undid anything — tview's undo is a
	// no-op when the stack is empty, and there is no public way to query it, so
	// we detect the change by comparing the text. If nothing was undone we fall
	// through to suspend, preserving Ctrl+Z's shell behavior.
	if event.Key() == tcell.KeyCtrlZ {
		if tc != nil && a.tapp.GetFocus() == tc.Input.TextArea && tc.Input.GetTextLength() > 0 {
			before := tc.Input.GetText()
			if h := tc.Input.TextArea.InputHandler(); h != nil {
				h(event, func(p tview.Primitive) { a.tapp.SetFocus(p) })
			}
			if tc.Input.GetText() != before {
				return nil // undo happened; consume the key
			}
		}
		a.suspend()
		return nil
	}

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

	case event.Key() == tcell.KeyTab || event.Key() == tcell.KeyEscape:
		if tc == nil {
			return nil
		}
		if a.tapp.GetFocus() == tc.Input.TextArea {
			a.tapp.SetFocus(tc.Chat.TextView)
		} else {
			// Match a mouse click on the input box: that click reaches the app's
			// SetMouseCapture, which clears the chat's hover/selection highlight.
			// Tab never triggers that path, so clear it here too.
			tc.Chat.ClearHover()
			a.tapp.SetFocus(tc.Input.TextArea)
		}
		return nil
	}
	return event
}

// suspend backgrounds the process via job control: tview restores the terminal
// out of raw mode, then we raise SIGTSTP so the shell stops us. Running `fg`
// delivers SIGCONT, the Kill call returns, and tview redraws the restored TUI.
func (a *App) suspend() {
	// A single suspend/resume cycle tears down and rebuilds tcell's terminal
	// I/O (reopen /dev/tty, respawn its goroutines). Overlapping cycles race
	// that teardown, so drop any suspend request while one is in flight —
	// e.g. a second Ctrl+Z, or the palette action firing alongside a keypress.
	if !a.suspending.CompareAndSwap(false, true) {
		return
	}
	defer a.suspending.Store(false)

	a.tapp.Suspend(func() {
		fmt.Fprint(os.Stdout, "\n"+drawTextBox([]string{
			"Hyphae has been suspended.",
			"Run `fg` or press Ctrl+Z again to resume",
		})+"\n")
		// Stop with SIGSTOP rather than SIGTSTP: SIGSTOP cannot be caught,
		// blocked, or discarded, so it reliably stops us without invoking the
		// runtime's fragile job-control stop dance. The terminal's stray
		// SIGTSTPs during the cooked-mode suspend/resume windows stay caught by
		// a.tstp (see Run) and ignored — that separation is what prevents the
		// signal-20 termination under rapid Ctrl+Z. `fg` sends SIGCONT, which
		// resumes us here regardless of which stop signal put us to sleep.
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGSTOP) //nolint:errcheck

		// Resumed. While we were stopped the terminal was in cooked mode, so
		// any keys pressed and mouse-movement reports emitted in the meantime
		// piled up in the kernel TTY input queue. Discard that backlog before
		// Suspend's screen.Resume() re-enters raw mode, otherwise tcell reads
		// it back and mis-delivers it as literal text (e.g. "^Z" and raw
		// "\e[<35;..M" mouse sequences) into the focused input box.
		flushTTYInput()
	})
}

// flushTTYInput discards any bytes queued in the controlling terminal's input
// buffer. Used on resume from suspend; see suspend for why the backlog exists.
func flushTTYInput() {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_ = unix.IoctlSetInt(int(f.Fd()), unix.TCFLSH, unix.TCIFLUSH)
}

// drawTextBox wraps the given lines in a single-line border sized to the widest
// line, one space of padding on each side. Returned with a trailing newline.
func drawTextBox(lines []string) string {
	w := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > w {
			w = n
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "┌%s┐\n", strings.Repeat("─", w+2))
	for _, l := range lines {
		fmt.Fprintf(&b, "│ %s%s │\n", l, strings.Repeat(" ", w-len([]rune(l))))
	}
	fmt.Fprintf(&b, "└%s┘\n", strings.Repeat("─", w+2))
	return b.String()
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

	// The resumed session keeps its own model (ResumeSession set up its agent and
	// records). Just reflect it in this tab — don't touch the global default or
	// any other open session.
	model := info.Model.ID
	if model == "" {
		model = a.cfg.Model
	}

	tc := a.registerSessionTab(sess)
	tc.Status.SetDefault(model, session.StatusIdle)
	// Seed the known context window; if it's not known yet, ResumeSession's
	// background enrichment will emit EvContextWindow once models.dev responds.
	a.seedContextWindow(tc, info.Model.ContextWindow)
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
		// onSelectModel — value is "endpointName\x00modelID", resolved to the
		// full Model (with context window and pricing) from the last listing.
		func(val string) {
			epName, modelID, ok := strings.Cut(val, "\x00")
			if !ok {
				return
			}
			m := controller.Model{Endpoint: epName, ID: modelID}
			for _, c := range a.modelChoices {
				if c.Endpoint == epName && c.ID == modelID {
					m = c
					break
				}
			}
			a.ctrl.SwitchModel(m)
			if tc := a.activeContent(); tc != nil {
				tc.Status.SetDefault(modelID, session.StatusIdle)
			}
		},
		// onSelectTheme
		func(id string) {
			if !SetThemeByID(id) {
				return
			}
			a.cfg.Theme = id
			if err := a.cfg.Save(); err != nil {
				if tc := a.activeContent(); tc != nil {
					tc.Status.SetError("theme save failed: " + err.Error())
				}
			}
			a.restyle()
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
				out = append(out, paletteSessionInfo{
					ID:            r.ID,
					Title:         r.Title,
					UpdatedAt:     r.UpdatedAt,
					WorkDir:       r.WorkDir,
					ContextWindow: r.ContextWindow,
					PromptTokens:  r.PromptTokens,
				})
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
							tc.Chat.ClearHover()
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
					Label:  "Ctrl+Z",
					Sub:    "suspend to background (fg to resume)",
					Action: func() { p.Close(); a.suspend() },
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

// restyle re-applies theme colors to every constructed widget after a theme
// switch, then re-renders the active chat. tview snapshots colors at
// construction time, so native chrome (box backgrounds, borders, input fields)
// must have its setters re-run; content drawn dynamically picks up the new
// palette on the next frame. Inactive tabs re-render their chat on switch.
// Must only be called from the tview event loop.
func (a *App) restyle() {
	a.layout.Tabs.Restyle()
	a.layout.Palette.Restyle()
	for _, t := range a.tabs {
		if tc := a.sessionContent(t.id); tc != nil {
			tc.Restyle()
		}
	}
	a.redrawActive()
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
	p.menuItems[6].Action = a.enterSelectModel
	p.menuItems[7].Action = func() { p.switchMode(paletteModeSelectTheme) }
	p.menuItems[8].Action = func() { p.switchMode(paletteModeHotkeys) }
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
		var models []controller.Model
		for _, ep := range a.cfg.Endpoints {
			ms, _ := a.ctrl.ListModels(a.ctrl.Context(), ep)
			models = append(models, ms...)
		}
		models = a.ctrl.EnrichPricing(a.ctrl.Context(), models)

		items := make([]PaletteItem, 0, len(models))
		for _, m := range models {
			items = append(items, PaletteItem{
				Label:  fmt.Sprintf("[%s]%s/[-]%s", TC.StatusText, m.Endpoint, m.ID),
				Sub:    formatContextWindow(m.ContextWindow),
				Detail: formatModelPricing(m.InputPrice, m.OutputPrice),
				Value:  m.Endpoint + "\x00" + m.ID,
			})
		}
		if len(items) == 0 {
			items = []PaletteItem{{Label: "no models found"}}
		}
		a.tapp.QueueUpdateDraw(func() {
			a.modelChoices = models
			a.layout.Palette.SetModelItems(items)
		})
	}()
}

// formatContextWindow renders a token count as a compact "128K context" label,
// or "" when unknown.
func formatContextWindow(tokens int64) string {
	if tokens <= 0 {
		return ""
	}
	return fmt.Sprintf("[%s]%s [%s]context", TC.Accent, humanTokens(tokens), TC.Muted)
}

// humanTokens abbreviates a token count as e.g. "128K" or "1M".
func humanTokens(t int64) string {
	switch {
	case t >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(t)/1e6), ".0") + "M"
	case t >= 1_000:
		return fmt.Sprintf("%.0f", float64(t)/1e3) + "K"
	default:
		return fmt.Sprintf("%d", t)
	}
}

// formatModelPricing renders per-1M-token pricing as a dim second line. Prices
// come from models.dev and carry a caveat since they may lag the provider.
func formatModelPricing(in, out float64) string {
	if in == 0 && out == 0 {
		return "Price: unknown"
	}
	return fmt.Sprintf("Price: [%s]$%s in[-] · [%s]$%s out[-] per 1M [%s](estimated)[-]",
		TC.ToolColor, trimPrice(in), TC.ShellColor, trimPrice(out), TC.Border)
}

// trimPrice renders a price rounded to at most 3 decimals, dropping trailing zeros
// (e.g. 3 → "3", 2.5 → "2.5", 0.075 → "0.075").
func trimPrice(v float64) string {
	s := strconv.FormatFloat(v, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
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
	// While following, a newly appended message re-grabs focus to the chat so the
	// view stays on the latest message even if the user had clicked into the input
	// (only from the input — modal dialogs keep their focus).
	grew := len(msgs) > len(tc.Chat.messages)
	summary, seqs := sess.GetCompact()
	tc.Chat.SetCompact(summary, seqs)
	tc.Chat.Render(msgs)
	tc.Status.SetStatus(status)
	if grew && tc.Chat.autoFollow && a.tapp.GetFocus() == tc.Input.TextArea {
		a.tapp.SetFocus(tc.Chat.TextView)
	}
}
