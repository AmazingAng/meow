// Package config stores the CLI's server URL and Device Key.
//
// Precedence: MEOW_SERVER / MEOW_KEY environment variables, then the config
// file (~/.config/meow/config.json, or $MEOW_CONFIG).
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Config struct {
	Server string `json:"server,omitempty"`
	Key    string `json:"key,omitempty"`
	// Installed maps an agent ("claude", "codex", "cursor", "grokbot") to the meow executable
	// "meow setup" wrote into its config, so uninstall can find it even when
	// the binary isn't named meow or has since moved.
	Installed map[string]string `json:"installed,omitempty"`
}

func Path() (string, error) {
	if p := os.Getenv("MEOW_CONFIG"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "meow", "config.json"), nil
}

// LoadFile reads the config file only; a missing file yields an empty Config.
func LoadFile() (Config, error) {
	var c Config
	p, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Load applies environment overrides on top of the config file.
func Load() (Config, error) {
	c, err := LoadFile()
	if v := os.Getenv("MEOW_SERVER"); v != "" {
		c.Server = v
	}
	if v := os.Getenv("MEOW_KEY"); v != "" {
		c.Key = v
	}
	return c, err
}

// Save writes the config file readable only by the current user.
func Save(c Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
