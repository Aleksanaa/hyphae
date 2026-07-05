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

// SwitchModel updates the active endpoint and model, recreates the agent,
// resets per-model pricing, and saves the config. Fetches updated pricing
// from models.dev in the background.
func (c *Controller) SwitchModel(epName, modelID string, cw int64) {
	c.cfg.ActiveEndpointName = epName
	c.cfg.Model = modelID
	var ep config.Endpoint
	for _, e := range c.cfg.Endpoints {
		if e.Name == epName {
			ep = e
			break
		}
	}
	c.mu.Lock()
	c.ag = agent.New(ep.BaseURL, ep.APIKey, modelID)
	c.mu.Unlock()

	c.cfg.ContextWindow = 0
	c.cfg.InputPrice = 0
	c.cfg.OutputPrice = 0
	if cw > 0 {
		c.cfg.ContextWindow = cw
		c.emit(Event{Kind: EvContextWindow, ContextWindow: cw})
	}
	go c.FetchModelDevInfoAsync(c.ctx, modelID)

	if c.st != nil {
		if sess, ok := c.mgr.Active(); ok {
			go c.st.UpdateSessionPricing(sess.ID, c.cfg.ContextWindow, c.cfg.InputPrice, c.cfg.OutputPrice) //nolint:errcheck
		}
	}
	c.cfg.Save() //nolint:errcheck
}

// ListModels returns models available at a given endpoint.
func (c *Controller) ListModels(ctx context.Context, ep config.Endpoint) ([]ModelInfo, error) {
	ag := agent.New(ep.BaseURL, ep.APIKey, "")
	raw, err := ag.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, len(raw))
	for i, m := range raw {
		out[i] = ModelInfo{ID: m.ID, ContextWindow: m.ContextWindow}
	}
	return out, nil
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

// FetchModelDevInfoAsync fetches context window and pricing for modelID from
// models.dev and emits EvContextWindow when data is available.
func (c *Controller) FetchModelDevInfoAsync(ctx context.Context, modelID string) {
	info := agent.FetchModelDevInfo(ctx, modelID)
	if info.ContextWindow <= 0 && info.InputPrice == 0 && info.OutputPrice == 0 {
		return
	}
	changed := false
	if info.ContextWindow > 0 && c.cfg.ContextWindow != info.ContextWindow {
		c.cfg.ContextWindow = info.ContextWindow
		c.emit(Event{Kind: EvContextWindow, ContextWindow: info.ContextWindow})
		changed = true
	}
	if info.InputPrice > 0 && c.cfg.InputPrice != info.InputPrice {
		c.cfg.InputPrice = info.InputPrice
		changed = true
	}
	if info.OutputPrice > 0 && c.cfg.OutputPrice != info.OutputPrice {
		c.cfg.OutputPrice = info.OutputPrice
		changed = true
	}
	if changed {
		c.cfg.Save() //nolint:errcheck
		if c.st != nil {
			if sess, ok := c.mgr.Active(); ok {
				go c.st.UpdateSessionPricing(sess.ID, c.cfg.ContextWindow, c.cfg.InputPrice, c.cfg.OutputPrice) //nolint:errcheck
			}
		}
	}
}
