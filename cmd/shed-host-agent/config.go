package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// userHomeDir returns the user's home directory, falling back to /tmp if unavailable.
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("could not determine home directory, using /tmp", "error", err)
		return "/tmp"
	}
	return home
}

// Config is the top-level configuration for shed-host-agent.
type Config struct {
	Server  string        `yaml:"server"`
	SSH     SSHConfig     `yaml:"ssh"`
	AWS     AWSConfig     `yaml:"aws"`
	Docker  DockerConfig  `yaml:"docker"`
	Logging LogConfig     `yaml:"logging"`
	Desktop DesktopConfig `yaml:"desktop"`
}

// DesktopConfig controls the shed-desktop approval delegation channel. When
// Enabled, the agent exposes a local Unix-domain socket that a connected
// shed-desktop app uses to receive the all-namespace audit/event stream and
// to answer SSH approval requests (with ssh.approval.method: shed-desktop).
// Disabled by default — with it off, none of this code path runs.
type DesktopConfig struct {
	Enabled    bool   `yaml:"enabled"`
	SocketPath string `yaml:"socket_path"`
	TimeoutMS  int    `yaml:"timeout_ms"` // per-request approval budget; default 25000
}

// DockerConfig controls the Docker registry credential handler behavior.
type DockerConfig struct {
	Registries []string `yaml:"registries"`  // registry hostnames to allow
	AllowAll   bool     `yaml:"allow_all"`   // bypass allowlist
	ConfigPath string   `yaml:"config_path"` // override Docker config.json path
}

// AWSConfig controls the AWS credential handler behavior.
type AWSConfig struct {
	SourceProfile      string                   `yaml:"source_profile"`
	DefaultRole        string                   `yaml:"default_role"`
	SessionDuration    string                   `yaml:"session_duration"`
	CacheRefreshBefore string                   `yaml:"cache_refresh_before"`
	Sheds              map[string]ShedAWSConfig `yaml:"sheds"`
}

// ShedAWSConfig holds per-shed AWS role overrides.
type ShedAWSConfig struct {
	Role string `yaml:"role"`
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
	Method     string `yaml:"method"`      // "biometrics-or-password" (default), "biometrics"
	SessionTTL string `yaml:"session_ttl"` // e.g., "4h"
}

// resolveAllowPassword maps the approval method to whether LocalAuthentication
// may fall back to Apple Watch / account password (LAPolicyDeviceOwnerAuthentication).
// Empty and unknown values default to true so approval works in clamshell mode and
// on Macs without a biometric sensor. Only "biometrics" disables the fallback.
func resolveAllowPassword(method string) bool {
	return method != "biometrics"
}

// LogConfig controls audit logging.
type LogConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	home := userHomeDir()
	return Config{
		Server: "http://localhost:8080",
		SSH: SSHConfig{
			Approval: ApprovalConfig{
				Policy:     "per-session",
				Method:     "biometrics-or-password",
				SessionTTL: "4h",
			},
		},
		AWS: AWSConfig{
			SourceProfile:      "default",
			SessionDuration:    "1h",
			CacheRefreshBefore: "5m",
		},
		Logging: LogConfig{
			Enabled: true,
			Path:    filepath.Join(home, ".local", "share", "shed", "extensions-audit.log"),
		},
		Desktop: DesktopConfig{
			Enabled:    false,
			SocketPath: filepath.Join(home, "Library", "Application Support", "shed", "host-agent.sock"),
			TimeoutMS:  25000,
		},
	}
}

// LoadConfig reads and parses the config file, applying defaults for missing fields.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()

	// Expand ~ in path
	if strings.HasPrefix(path, "~/") {
		home := userHomeDir()
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
		home := userHomeDir()
		cfg.Logging.Path = filepath.Join(home, cfg.Logging.Path[2:])
	}

	// Expand ~ in desktop socket path
	if strings.HasPrefix(cfg.Desktop.SocketPath, "~/") {
		home := userHomeDir()
		cfg.Desktop.SocketPath = filepath.Join(home, cfg.Desktop.SocketPath[2:])
	}

	return cfg, nil
}
