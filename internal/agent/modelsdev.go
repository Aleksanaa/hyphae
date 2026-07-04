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
}

// FetchContextWindow fetches the models.dev catalog and returns the context
// window for the given model ID. Returns 0 if not found or on error.
func FetchContextWindow(ctx context.Context, modelID string) int64 {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "hyphae")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var catalog modelsDevCatalog
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return 0
	}

	return lookupContextWindow(catalog, modelID)
}

func lookupContextWindow(catalog modelsDevCatalog, modelID string) int64 {
	id := strings.ToLower(modelID)

	// Exact match against all provider model keys.
	for _, p := range catalog {
		for name, m := range p.Models {
			if strings.ToLower(name) == id {
				return m.Limit.Context
			}
		}
	}

	// Strip provider prefix (e.g. "anthropic/claude-opus-4" → "claude-opus-4")
	// and try again. Some OpenAI-compatible APIs prefix the model ID with the provider.
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		suffix := id[idx+1:]
		for _, p := range catalog {
			for name, m := range p.Models {
				if strings.ToLower(name) == suffix {
					return m.Limit.Context
				}
			}
		}
	}

	return 0
}
