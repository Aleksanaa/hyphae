package controller

import (
	"context"

	"github.com/aleksanaa/hyphae/internal/agent"
	"github.com/aleksanaa/hyphae/internal/config"
)

// AddEndpoint appends an endpoint to the config and saves it.
func (c *Controller) AddEndpoint(name, baseURL, apiKey string) error {
	c.cfg.Endpoints = append(c.cfg.Endpoints, config.Endpoint{
		Name:    name,
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	return c.cfg.Save()
}

// RemoveEndpoint deletes an endpoint by name from the config and saves it.
func (c *Controller) RemoveEndpoint(name string) error {
	eps := c.cfg.Endpoints
	for i, ep := range eps {
		if ep.Name == name {
			c.cfg.Endpoints = append(eps[:i], eps[i+1:]...)
			break
		}
	}
	return c.cfg.Save()
}

// SwitchModel makes m the active model: it recreates the agent, updates config
// identity, records the model for the active session, and saves. Pricing/context
// carried by m seed the display immediately; any gaps are filled from models.dev
// in the background.
func (c *Controller) SwitchModel(m Model) {
	c.cfg.ActiveEndpointName = m.Endpoint
	c.cfg.Model = m.ID
	var ep config.Endpoint
	for _, e := range c.cfg.Endpoints {
		if e.Name == m.Endpoint {
			ep = e
			break
		}
	}
	activeID := c.mgr.ActiveID()
	c.mu.Lock()
	c.ag = agent.New(ep.BaseURL, ep.APIKey, m.ID)
	c.current = m
	if activeID != "" {
		c.sessionModels[activeID] = m
	}
	c.mu.Unlock()

	if m.ContextWindow > 0 {
		c.emit(Event{Kind: EvContextWindow, ContextWindow: m.ContextWindow})
	}
	go c.FetchModelDevInfoAsync(c.ctx, m.ID)

	if c.st != nil {
		if sess, ok := c.mgr.Active(); ok {
			go c.st.UpdateSessionModel(sess.ID, m.ID, m.Endpoint)                               //nolint:errcheck
			go c.st.UpdateSessionPricing(sess.ID, m.ContextWindow, m.InputPrice, m.OutputPrice) //nolint:errcheck
		}
	}
	c.cfg.Save() //nolint:errcheck
}

// ListModels returns the models available at a given endpoint. Pricing is left
// zero; call EnrichPricing to fill context window and pricing from models.dev.
func (c *Controller) ListModels(ctx context.Context, ep config.Endpoint) ([]Model, error) {
	ag := agent.New(ep.BaseURL, ep.APIKey, "")
	raw, err := ag.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Model, len(raw))
	for i, m := range raw {
		out[i] = Model{Endpoint: ep.Name, ID: m.ID, ContextWindow: m.ContextWindow}
	}
	return out, nil
}

// EnrichPricing fills context window and pricing for models from the models.dev
// catalog (fetched once). Existing non-zero context windows are kept; pricing is
// filled where the catalog has it. Returns models for chaining.
func (c *Controller) EnrichPricing(ctx context.Context, models []Model) []Model {
	cat := agent.FetchModelDevCatalog(ctx)
	for i := range models {
		info := cat.Lookup(models[i].ID)
		if models[i].ContextWindow <= 0 {
			models[i].ContextWindow = info.ContextWindow
		}
		if info.InputPrice > 0 {
			models[i].InputPrice = info.InputPrice
		}
		if info.OutputPrice > 0 {
			models[i].OutputPrice = info.OutputPrice
		}
	}
	return models
}

// ListSessions returns a lightweight summary of sessions for the controller's work directory.
func (c *Controller) ListSessions() ([]SessionSummary, error) {
	if c.st == nil {
		return nil, nil
	}
	rows, err := c.st.ListSessions(c.mgr.WorkDir())
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, len(rows))
	for i, r := range rows {
		out[i] = SessionSummary{ID: r.ID, Title: r.Title, UpdatedAt: r.UpdatedAt}
	}
	return out, nil
}

// FetchModelDevInfoAsync fills any missing context window / pricing for modelID
// from models.dev, updating the current model (and the active session's record)
// and emitting EvContextWindow when the context window becomes known.
func (c *Controller) FetchModelDevInfoAsync(ctx context.Context, modelID string) {
	info := agent.FetchModelDevInfo(ctx, modelID)
	if info.ContextWindow <= 0 && info.InputPrice == 0 && info.OutputPrice == 0 {
		return
	}
	c.mu.Lock()
	changed := false
	if info.ContextWindow > 0 && c.current.ContextWindow != info.ContextWindow {
		c.current.ContextWindow = info.ContextWindow
		changed = true
	}
	if info.InputPrice > 0 && c.current.InputPrice != info.InputPrice {
		c.current.InputPrice = info.InputPrice
		changed = true
	}
	if info.OutputPrice > 0 && c.current.OutputPrice != info.OutputPrice {
		c.current.OutputPrice = info.OutputPrice
		changed = true
	}
	m := c.current
	if activeID := c.mgr.ActiveID(); activeID != "" {
		c.sessionModels[activeID] = m
	}
	c.mu.Unlock()

	if !changed {
		return
	}
	if info.ContextWindow > 0 {
		c.emit(Event{Kind: EvContextWindow, ContextWindow: info.ContextWindow})
	}
	if c.st != nil {
		if sess, ok := c.mgr.Active(); ok {
			go c.st.UpdateSessionPricing(sess.ID, m.ContextWindow, m.InputPrice, m.OutputPrice) //nolint:errcheck
		}
	}
}
