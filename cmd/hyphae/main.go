package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/aleksana/hyphae/internal/agent"
	"github.com/aleksana/hyphae/internal/config"
	"github.com/aleksana/hyphae/internal/ui"
)

func main() {
	var (
		workDir    = flag.String("dir", "", "working directory (defaults to cwd)")
		listModels = flag.Bool("list-models", false, "list available models and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "hyphae — interactive coding agent\n\n")
		fmt.Fprintf(os.Stderr, "Usage: hyphae [flags]\n\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nConfig file: $XDG_CONFIG_HOME/hyphae/config.toml\n")
	}
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	if *workDir != "" {
		cfg.WorkDir = *workDir
	}

	if *listModels {
		ep := cfg.ActiveEndpoint()
		if ep.APIKey == "" {
			fmt.Fprintln(os.Stderr, "no endpoint configured — add one via Ctrl+P in the app")
			os.Exit(1)
		}
		ag := agent.New(ep.BaseURL, ep.APIKey, "")
		models, err := ag.ListModels(context.Background())
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
