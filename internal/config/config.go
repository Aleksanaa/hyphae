package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config holds all runtime settings.
type Config struct {
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
	Model   string `toml:"model"`
	WorkDir string `toml:"work_dir"`
}

// Load reads config from file then applies environment overrides.
func Load() (*Config, error) {
	cfg := &Config{
		BaseURL: "https://opencode.ai/zen/go/v1",
	}

	path := configPath()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, err
		}
	}

	if v := os.Getenv("OPENCODE_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("HYPANE_BASE_URL"); v != "" {
		cfg.BaseURL = v
	}
	if v := os.Getenv("HYPANE_MODEL"); v != "" {
		cfg.Model = v
	}

	// Default working directory to cwd
	if cfg.WorkDir == "" {
		cfg.WorkDir, _ = os.Getwd()
	}

	return cfg, nil
}

func configPath() string {
	if v := os.Getenv("HYPANE_CONFIG"); v != "" {
		return v
	}
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "hypane", "config.toml")
}
