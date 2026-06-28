package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Endpoint is a named API endpoint with its own key.
type Endpoint struct {
	Name    string `toml:"name"`
	BaseURL string `toml:"base_url"`
	APIKey  string `toml:"api_key"`
}

// Config holds all runtime settings.
type Config struct {
	Endpoints []Endpoint `toml:"endpoint"`
	Model     string     `toml:"model"`
}

// ActiveEndpoint returns the first configured endpoint, or a zero-value one if none.
func (c *Config) ActiveEndpoint() Endpoint {
	if len(c.Endpoints) > 0 {
		return c.Endpoints[0]
	}
	return Endpoint{}
}

// Load reads config from file.
func Load() (*Config, error) {
	cfg := &Config{}

	path := configPath()
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// Save writes the current config back to the config file.
func (c *Config) Save() error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func configPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "hyphae", "config.toml")
}
