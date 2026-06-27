package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/aleksana/hyphae/internal/config"
	"github.com/aleksana/hyphae/internal/llm"
	"github.com/aleksana/hyphae/internal/ui"
)

func main() {
	var (
		apiKey     = flag.String("key", "", "opencode API key (overrides OPENCODE_API_KEY)")
		baseURL    = flag.String("url", "", "API base URL (overrides config)")
		model      = flag.String("model", "", "model name (overrides config)")
		workDir    = flag.String("dir", "", "working directory (defaults to cwd)")
		listModels = flag.Bool("list-models", false, "list available models and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "hyphae — interactive coding agent\n\n")
		fmt.Fprintf(os.Stderr, "Usage: hyphae [flags]\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nConfig file: $XDG_CONFIG_HOME/hyphae/config.toml\n")
		fmt.Fprintf(os.Stderr, "Env vars:    OPENCODE_API_KEY, HYPANE_MODEL, HYPANE_BASE_URL\n")
	}
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	if *apiKey != "" {
		cfg.APIKey = *apiKey
	}
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if *model != "" {
		cfg.Model = *model
	}
	if *workDir != "" {
		cfg.WorkDir = *workDir
	}

	if *listModels {
		if cfg.APIKey == "" {
			fmt.Fprintln(os.Stderr, "OPENCODE_API_KEY is not set")
			os.Exit(1)
		}
		client := llm.New(cfg.BaseURL, cfg.APIKey, "")
		models, err := client.ListModels(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		sort.Strings(models)
		for _, m := range models {
			fmt.Println(m)
		}
		return
	}

	app := ui.New(cfg)
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
