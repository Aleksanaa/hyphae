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
	Endpoints          []Endpoint `toml:"endpoint"`
	ActiveEndpointName string     `toml:"active_endpoint"`
	Model              string     `toml:"model"`
	Theme              string     `toml:"theme"` // bubbletint tint ID; empty = built-in default
}

// ActiveEndpoint returns the endpoint matching ActiveEndpointName, falling back to the first.
func (c *Config) ActiveEndpoint() Endpoint {
	if c.ActiveEndpointName != "" {
		for _, ep := range c.Endpoints {
			if ep.Name == c.ActiveEndpointName {
				return ep
			}
		}
	}
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

// GlobalInstructionsPath returns the path to the global user-instructions file
// (<UserConfigDir>/hyphae/AGENTS.md). These instructions apply to every session.
func GlobalInstructionsPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "hyphae", "AGENTS.md")
}

// SkillsDir returns the directory holding global skills
// (<UserConfigDir>/hyphae/skills).
func SkillsDir() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "hyphae", "skills")
}
