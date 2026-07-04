package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const modelsDevURL = "https://models.dev/api.json"

type modelsDevCatalog map[string]modelsDevProvider

type modelsDevProvider struct {
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	Limit struct {
		Context int64 `json:"context"`
	} `json:"limit"`
	Cost *struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"cost"`
}

// ModelDevInfo holds the data fetched from models.dev for a given model.
type ModelDevInfo struct {
	ContextWindow int64
	InputPrice    float64 // USD per million input tokens; 0 if unknown
	OutputPrice   float64 // USD per million output tokens; 0 if unknown
}

// FetchModelDevInfo fetches the models.dev catalog and returns context window
// and pricing for the given model ID. Returns zero values on error or not found.
func FetchModelDevInfo(ctx context.Context, modelID string) ModelDevInfo {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return ModelDevInfo{}
	}
	req.Header.Set("User-Agent", "hyphae")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ModelDevInfo{}
	}
	defer resp.Body.Close()

	var catalog modelsDevCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return ModelDevInfo{}
	}

	return lookupModelDevInfo(catalog, modelID)
}

func lookupModelDevInfo(catalog modelsDevCatalog, modelID string) ModelDevInfo {
	id := strings.ToLower(modelID)

	if info, ok := findInCatalog(catalog, id); ok {
		return info
	}

	// Strip provider prefix (e.g. "anthropic/claude-opus-4" → "claude-opus-4")
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		if info, ok := findInCatalog(catalog, id[idx+1:]); ok {
			return info
		}
	}

	return ModelDevInfo{}
}

func findInCatalog(catalog modelsDevCatalog, id string) (ModelDevInfo, bool) {
	for _, p := range catalog {
		for name, m := range p.Models {
			if strings.ToLower(name) != id {
				continue
			}
			info := ModelDevInfo{ContextWindow: m.Limit.Context}
			if m.Cost != nil {
				info.InputPrice = m.Cost.Input
				info.OutputPrice = m.Cost.Output
			}
			return info, true
		}
	}
	return ModelDevInfo{}, false
}
