package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for shed-host-agent.
type Config struct {
	Server  string    `yaml:"server"`
	SSH     SSHConfig `yaml:"ssh"`
	Logging LogConfig `yaml:"logging"`
}

// SSHConfig controls the SSH agent handler behavior.
type SSHConfig struct {
	Mode     string         `yaml:"mode"` // "agent-forward", "local-keys", or "" (auto)
	Approval ApprovalConfig `yaml:"approval"`
}

// ApprovalConfig controls biometric/Touch ID approval gates.
type ApprovalConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Policy     string `yaml:"policy"`      // "per-request", "per-session", "per-shed"
	SessionTTL string `yaml:"session_ttl"` // e.g., "4h"
}

// LogConfig controls audit logging.
type LogConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Server: "http://localhost:8080",
		SSH: SSHConfig{
			Approval: ApprovalConfig{
				Policy:     "per-session",
				SessionTTL: "4h",
			},
		},
		Logging: LogConfig{
			Enabled: true,
			Path:    filepath.Join(home, ".local", "share", "shed", "extensions-audit.log"),
		},
	}
}

// LoadConfig reads and parses the config file, applying defaults for missing fields.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	// Expand ~ in path
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	// Expand ~ in log path
	if strings.HasPrefix(cfg.Logging.Path, "~/") {
		home, _ := os.UserHomeDir()
		cfg.Logging.Path = filepath.Join(home, cfg.Logging.Path[2:])
	}

	return cfg, nil
}
