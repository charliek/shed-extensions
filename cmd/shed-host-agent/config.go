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

// expandTilde expands a leading "~/" in path to the user's home directory.
func expandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(userHomeDir(), path[2:])
	}
	return path
}

// serverShedKey builds the composite key used to scope per-shed state (the AWS
// credential cache, Touch ID approvals) to a single server, so identical shed
// names on different servers stay isolated.
func serverShedKey(server, shed string) string { return server + "/" + shed }

// Config is the top-level configuration for shed-host-agent.
type Config struct {
	// Server is the legacy single-server URL. It is used only when Discovery
	// is absent (nil). When Discovery is set, this field is ignored.
	Server string `yaml:"server"`
	// Discovery enables multi-server mode: the agent discovers shed servers
	// from shed's own CLI config and brokers for all (or a selected subset) of
	// them from a single process. nil (block omitted) => legacy single-server.
	Discovery *DiscoveryConfig `yaml:"discovery"`
	SSH       SSHConfig        `yaml:"ssh"`
	AWS       AWSConfig        `yaml:"aws"`
	Docker    DockerConfig     `yaml:"docker"`
	Logging   LogConfig        `yaml:"logging"`
	Desktop   DesktopConfig    `yaml:"desktop"`
}

// DiscoveryConfig selects which shed servers a single agent process watches and
// how it reacts to changes in the discovery source.
type DiscoveryConfig struct {
	// Servers selects which discovered servers to watch: the scalar "all"
	// (default) or a YAML list of server names.
	Servers ServerSelector `yaml:"servers"`
	// Source overrides the shed CLI config path (default ~/.shed/config.yaml).
	Source string `yaml:"source"`
	// Watch controls live reloading of the discovery source:
	// "fsnotify" (default), "poll", or "off".
	Watch string `yaml:"watch"`
	// PollInterval is the reconcile cadence when Watch == "poll" (default 10s).
	PollInterval string `yaml:"poll_interval"`
	// Debounce coalesces rapid fsnotify events when Watch == "fsnotify"
	// (default 500ms).
	Debounce string `yaml:"debounce"`
}

// ServerSelector chooses which discovered servers to watch. It unmarshals from
// either the scalar "all" (every discovered server) or a YAML list of names.
type ServerSelector struct {
	All   bool
	Names []string
}

// UnmarshalYAML accepts either a scalar ("all" or a single name) or a sequence
// of server names.
func (s *ServerSelector) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" || value.Value == "all" {
			s.All = true
			return nil
		}
		s.Names = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		var names []string
		if err := value.Decode(&names); err != nil {
			return err
		}
		// Keep an explicit empty list non-nil so it is distinguishable from an
		// omitted selector (nil) — `servers: []` means "watch none", not "all".
		if names == nil {
			names = []string{}
		}
		s.Names = names
		return nil
	default:
		return fmt.Errorf("discovery.servers must be \"all\" or a list of server names")
	}
}

// Selected reports whether a discovered server name should be watched. An
// omitted selector (nil Names) selects every server; an explicit empty list
// (`servers: []`) selects none.
func (s ServerSelector) Selected(name string) bool {
	if s.All || s.Names == nil {
		return true
	}
	for _, n := range s.Names {
		if n == name {
			return true
		}
	}
	return false
}

// ServerTarget is a resolved shed server this agent brokers for. Name is the
// identity key (the discovery name, or empty in single-server mode); URL is the
// shed-server broker base URL (http://host:port).
type ServerTarget struct {
	Name string
	URL  string
}

// ResolveTargets computes the desired set of servers to watch. In single-server
// mode (Discovery nil) it returns a single unnamed target from Config.Server,
// ignoring the discovered list. In discovery mode it filters discovered
// servers by the selector and dedups by name.
func (c Config) ResolveTargets(discovered []ServerTarget) []ServerTarget {
	if c.Discovery == nil {
		// Single-server mode: one unnamed target. An empty name keeps the
		// `server` field out of audit logs and desktop events (omitempty), and
		// scopes per-shed state under "/<shed>" — there is only one server.
		return []ServerTarget{{Name: "", URL: c.Server}}
	}
	out := make([]ServerTarget, 0, len(discovered))
	seen := make(map[string]bool, len(discovered))
	for _, t := range discovered {
		if !c.Discovery.Servers.Selected(t.Name) || seen[t.Name] {
			continue
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	return out
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

// DockerConfig controls the Docker registry credential handler behavior. The
// top-level fields are the defaults; Servers carries per-server (and per-shed)
// overrides layered over them. ConfigPath is process-global.
type DockerConfig struct {
	Registries []string `yaml:"registries"`  // registry hostnames to allow (default)
	AllowAll   bool     `yaml:"allow_all"`   // bypass allowlist (default)
	ConfigPath string   `yaml:"config_path"` // override Docker config.json path
	// Servers holds per-server overrides, each optionally with per-shed nesting.
	Servers map[string]DockerServerConfig `yaml:"servers"`
}

// DockerServerConfig holds per-server Docker overrides. AllowAll is a pointer so
// an unset value inherits the default rather than forcing false.
type DockerServerConfig struct {
	Registries []string                    `yaml:"registries"`
	AllowAll   *bool                       `yaml:"allow_all"`
	Sheds      map[string]DockerShedConfig `yaml:"sheds"`
}

// DockerShedConfig holds per-shed Docker overrides.
type DockerShedConfig struct {
	Registries []string `yaml:"registries"`
	AllowAll   *bool    `yaml:"allow_all"`
}

// ResolvedDocker is the effective Docker policy for a single (server, shed) pair.
type ResolvedDocker struct {
	Registries []string
	AllowAll   bool
}

// Resolve layers Docker overrides for a (server, shed) pair, most specific wins:
// top-level defaults -> Servers[server] -> Servers[server].Sheds[shed]. A
// non-nil Registries list replaces (does not merge) the inherited list.
func (d DockerConfig) Resolve(server, shed string) ResolvedDocker {
	r := ResolvedDocker{Registries: d.Registries, AllowAll: d.AllowAll}
	sv, ok := d.Servers[server]
	if !ok {
		return r
	}
	if sv.Registries != nil {
		r.Registries = sv.Registries
	}
	if sv.AllowAll != nil {
		r.AllowAll = *sv.AllowAll
	}
	if sc, ok := sv.Sheds[shed]; ok {
		if sc.Registries != nil {
			r.Registries = sc.Registries
		}
		if sc.AllowAll != nil {
			r.AllowAll = *sc.AllowAll
		}
	}
	return r
}

// AWSConfig controls the AWS credential handler behavior. The top-level fields
// are the defaults; Servers carries per-server (and per-shed) overrides layered
// over them. SourceProfile and CacheRefreshBefore are process-global (a single
// STS client is built from SourceProfile) and are not overridable per server.
type AWSConfig struct {
	SourceProfile      string `yaml:"source_profile"`
	DefaultRole        string `yaml:"default_role"`
	SessionDuration    string `yaml:"session_duration"`
	CacheRefreshBefore string `yaml:"cache_refresh_before"`
	// Sheds is a deprecated global per-shed override map (applies regardless of
	// server). Prefer Servers.<name>.sheds.<name>. Kept for back-compat.
	Sheds map[string]ShedAWSConfig `yaml:"sheds"`
	// Servers holds per-server overrides, each optionally with per-shed nesting.
	Servers map[string]AWSServerConfig `yaml:"servers"`
}

// AWSServerConfig holds per-server AWS overrides.
type AWSServerConfig struct {
	DefaultRole     string                   `yaml:"default_role"`
	SessionDuration string                   `yaml:"session_duration"`
	Sheds           map[string]ShedAWSConfig `yaml:"sheds"`
}

// ShedAWSConfig holds per-shed AWS overrides.
type ShedAWSConfig struct {
	Role            string `yaml:"role"`
	SessionDuration string `yaml:"session_duration"`
}

// ResolvedAWS is the effective AWS policy for a single (server, shed) pair.
type ResolvedAWS struct {
	Role            string // assumed-role ARN ("" => no role configured)
	SessionDuration string // "" => fall back to the backend default
}

// Resolve layers AWS overrides for a (server, shed) pair, most specific wins:
// top-level defaults -> deprecated global Sheds[shed] -> Servers[server] ->
// Servers[server].Sheds[shed].
func (a AWSConfig) Resolve(server, shed string) ResolvedAWS {
	r := ResolvedAWS{Role: a.DefaultRole, SessionDuration: a.SessionDuration}
	if sc, ok := a.Sheds[shed]; ok {
		if sc.Role != "" {
			r.Role = sc.Role
		}
		if sc.SessionDuration != "" {
			r.SessionDuration = sc.SessionDuration
		}
	}
	if sv, ok := a.Servers[server]; ok {
		if sv.DefaultRole != "" {
			r.Role = sv.DefaultRole
		}
		if sv.SessionDuration != "" {
			r.SessionDuration = sv.SessionDuration
		}
		if sc, ok := sv.Sheds[shed]; ok {
			if sc.Role != "" {
				r.Role = sc.Role
			}
			if sc.SessionDuration != "" {
				r.SessionDuration = sc.SessionDuration
			}
		}
	}
	return r
}

// HasAnyRole reports whether any role is configured at any level — used to
// decide whether the AWS handler should start at all.
func (a AWSConfig) HasAnyRole() bool {
	if a.DefaultRole != "" {
		return true
	}
	for _, s := range a.Sheds {
		if s.Role != "" {
			return true
		}
	}
	for _, sv := range a.Servers {
		if sv.DefaultRole != "" {
			return true
		}
		for _, s := range sv.Sheds {
			if s.Role != "" {
				return true
			}
		}
	}
	return false
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

	path = expandTilde(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	cfg.Logging.Path = expandTilde(cfg.Logging.Path)
	cfg.Desktop.SocketPath = expandTilde(cfg.Desktop.SocketPath)

	// Apply discovery defaults and expand its source path (only when the
	// discovery block is present — its absence means legacy single-server).
	if cfg.Discovery != nil {
		cfg.Discovery.applyDefaults()
	}

	return cfg, nil
}

// DefaultDiscoverySource is the shed CLI client config the agent discovers
// servers from when discovery is enabled without an explicit source.
const DefaultDiscoverySource = "~/.shed/config.yaml"

// applyDefaults fills unset discovery fields and expands ~ in the source path.
func (d *DiscoveryConfig) applyDefaults() {
	if d.Source == "" {
		d.Source = DefaultDiscoverySource
	}
	d.Source = expandTilde(d.Source)
	if d.Watch == "" {
		d.Watch = "fsnotify"
	}
	if d.PollInterval == "" {
		d.PollInterval = "10s"
	}
	if d.Debounce == "" {
		d.Debounce = "500ms"
	}
}
