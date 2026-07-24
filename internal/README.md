# Architecture

hyphae is a terminal coding agent. The `internal/` tree is a strict layered
architecture: each layer depends only on layers below it. The leaf packages
(`config`, `session`, `store`) import nothing from this project except the
stdlib-only `strutil` foundation, which sits below every layer.

```
          strutil                    ← foundation; stdlib-only string helpers
             ↑
config    session    store          ← leaves; import only stdlib + strutil
             ↑
           agent                     ← agent → session
             ↑
         controller                  ← controller → agent, session, store, config
             ↑
            ui                        ← ui → controller, session, config
```

No layer reaches upward and there are no cycles. `cmd/hyphae` wires it together:
build a `config.Config`, `controller.NewFromConfig`, then hand the controller to
`ui.NewApp`.

The mental model is **App → Controller → Agent → Session**, with `Store` off to
the side for persistence:

- **`session`** — the in-memory conversation state (the "document").
- **`agent`** — one LLM ↔ tool loop that mutates a session and emits events.
- **`controller`** — owns lifecycle, orchestration, billing, persistence; the
  single seam between the UI and everything below it.
- **`ui`** — tview/tcell TUI; subscribes to controller events, renders sessions.
- **`store`** — SQLite persistence.
- **`config`** — endpoints + default model, loaded/saved as JSON.

---

## Packages

### `config` (leaf)
`Config` holds the list of `Endpoint{Name, BaseURL, APIKey}`, the
`ActiveEndpointName`, and the default `Model` id. `Load`/`Save` read and write a
JSON file. This is *only* the persisted default for **new** sessions — it is not
the source of truth for what a running session uses (see Model locality below).

### `session` (leaf)
The conversation document and its lifecycle. Pure data + mutation; knows nothing
about LLMs, endpoints, the DB, or display.

- `Message` is one **peer item** in an occurrence-ordered list: a user message, a
  thinking block, a tool status, or an assistant answer (`Role`). Streaming
  deltas land here immediately so the UI can redraw mid-turn (`AppendTextDelta`,
  `AppendThinkingDelta`, `SetToolResult`, `FinalizeMessage`, …).
- `Message` carries **no UI display state** — collapsing, aggregation and
  ordering are the UI's job. The session stores a stable, flat list of peers.
- `Session` also tracks `Status`, plan mode, compact summary/seqs, and a
  "cold-resumed" flag. `Snapshot()` returns `([]Message, Status)` atomically.
- `Manager` is the in-memory set of open sessions plus the active-session id.
  Tab/focus selection is *not* here — that is a UI concern; the manager only
  knows which session is "active" for event routing.

### `agent` (→ session)
One `Agent` drives one session through a single API turn and the tool loop.

- `New(providerType, baseURL, apiKey, model)` builds a goai `provider.LanguageModel`
  bound to a model (`compat` for openai-compatible endpoints, or native `anthropic`/
  `google`). Each agent serves exactly one session, so the `run`-tool starlark
  `namespace` (persistent globals) is a plain field, not a per-session map.
- `Send(ctx, sess)` runs `loop`: call the model, stream `text_delta` /
  `reasoning_delta` into the session, execute tool calls (with approval gating
  for writes and `ask_user`), record results, repeat until the model returns a
  final answer. It returns a channel of `agent.Event` describing what happened
  (deltas, tool start/done, usage updates, approval/select requests, done/error).
- The agent both **mutates the session** (content) and **emits an event stream**
  (status/usage/control). This dual write is an inherent streaming tradeoff, not
  an accident — deltas must hit the session immediately, while the controller
  needs the event stream for status, billing, and approvals.
- Tools live in `tools.go` (+ `script.go` for the starlark `run` sandbox,
  `diff.go`, `webfetch.go`, `websearch.go`). `requiresApproval` marks the
  side-effecting ones.

### `controller` (→ agent, session, store, config)
The orchestration layer and the UI's only handle to the system. Owns:

- **Lifecycle** (`session.go`): `NewSession`, `ResumeSession`, `SwitchSession`,
  `CloseSession`. `ResumeSession` is the DB-row ⇆ `session.Message` bridge
  (loads rows, rebuilds peer items, handles legacy folded rows) — the only place
  that translates between the two leaves, since neither leaf may know the other.
- **Turns** (`turn.go`): `SendMessage` picks the session's agent
  (`agentFor`), starts it, and `processAgentEvents` translates
  `agent.Event` → `controller.Event` for the UI, applies status transitions to
  the session, and accumulates cost/token usage.
- **Compaction** (`compact.go`): summarize history and record the compact point.
- **Model / config** (`config.go`, `modelsdev.go`): endpoint CRUD, `ListModels`,
  `SwitchModel`, and pricing/context enrichment. `modelsdev.go` is external I/O
  against models.dev — it lives here (orchestration) and must never leak into the
  stdlib-only `session` leaf.
- **Persistence bridge** (`PersistSession`): writes one DB row per message.
- **Events**: `emit`/`Events()` with a non-blocking forwarder goroutine so the
  producer never blocks on a slow UI. `Event.Kind` (`Ev*`) is the UI's whole
  vocabulary.

`Controller` holds all per-session mutable state keyed by session id:
`sessionModels`, `sessionAgents`, `sessionCosts`, `lastPromptTokens`. There is
deliberately **no** global "current model" or shared agent.

### `store` (leaf)
SQLite via a small hand-rolled migration list. `SessionRow` is the per-session
metadata (model, endpoint, pricing, cost, compact summary/seqs, plan mode);
messages persist as rows with a `tool_calls` child table. `LoadedMessage` /
`LoadedToolCall` are the read shapes the controller reassembles. The store knows
nothing about `session.Message` — the controller does the translation.

### `ui` (→ controller, session, config)
tview/tcell front end. `app.go` owns the tab layout, the event loop that drains
`controller.Events()`, and redraw scheduling. `chat.go` renders a session's peer
list into boxes, and **all** grouping/collapsing/aggregation happens here (the
non-UI layers stay ignorant of display). Other files are focused widgets:
`status.go` (bottom bar), `input.go`, `palette.go`, `approval.go`, `selectview.go`,
`diff*.go`, `markdown.go`, `tabs.go`/`tabcontent.go`, `welcome.go`, `theme.go`.

---

## Cross-cutting flows

### A turn
UI `SendMessage(text)` → controller appends the user message, resolves
`agentFor(activeID)`, calls `agent.Send` → agent streams deltas into the session
and emits events → `processAgentEvents` maps them to `controller.Event`s and
updates status/billing → UI redraws the active session on `EvRedraw` and updates
the status bar on the other `Ev*` events.

### Persistence
Mutations stay in memory; the controller persists asynchronously
(`go PersistSession`) on close and at key points. `PersistSession` serializes per
session (a `persistMu` per id) to avoid seq races, and writes one row per
non-partial message plus its tool calls. Resume reverses this in `ResumeSession`.

### Model locality (per-session)
Every session owns its own `Model` (`sessionModels[id]`) **and** its own
`*agent.Agent` (`sessionAgents[id]`). Turns run on `agentFor(sessionID)`, never a
shared agent.

- `defaultModel()` = config identity (`{ActiveEndpointName, Model}`) — the
  starting point for a **new** session only; pricing/context are filled in
  per-session by `EnrichSessionAsync`.
- `SwitchModel(m)` mutates only the **active** session (its model + agent) and
  updates config as the default for future new sessions. It never touches other
  open sessions.
- `EnrichSessionAsync(sessionID)` is session-scoped: it fetches models.dev by
  that session's model id, writes back only if the identity still matches (guards
  a mid-fetch switch), updates **only pricing/context** (never identity), and
  emits `EvContextWindow` only if that session is still active.
- **Display is per-tab too.** Each tab has its own `StatusBar`; the model string
  is set once per tab (creation / resume / switch). Redraws call
  `StatusBar.SetStatus(status)` (status only) — never `SetDefault(cfg.Model, …)`,
  which would re-stamp the global default onto whatever tab is drawing and make a
  switch in one session appear to change others.

---

## Design principles

- **Leaves import nothing from this project.** External I/O (models.dev, DB, HTTP
  tools) lives in `agent`/`controller`, never in `config`/`session`/`store`.
- **UI owns display arrangement.** Ordering, collapsing, and aggregation are
  rendering choices; the model stores a stable occurrence-ordered peer list.
- **No global "current" anything.** Per-session state is keyed by session id in
  the controller; there is no shared model or agent.
- **No content with tool_calls.** `appendHistory` must never emit an assistant
  message carrying both `content` and `tool_calls` — some providers 400 on it.
- **Prefer concrete helpers over interface abstractions** to unify similar
  handler blocks; keep the seams simple and explicit.
