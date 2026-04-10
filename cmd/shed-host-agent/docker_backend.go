package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// DockerBackend resolves Docker registry credentials on the host.
type DockerBackend interface {
	// GetCredentials returns credentials for the given registry.
	GetCredentials(ctx context.Context, serverURL string) (*DockerCredential, error)

	// ListCredentials returns a map of allowed registry hostnames to usernames.
	ListCredentials(ctx context.Context) (map[string]string, error)

	// Status returns the allowlist mode and registry count.
	Status() (allowAll bool, registryCount int)
}

type DockerCredential struct {
	ServerURL string
	Username  string
	Secret    string
}

type helperExecutor interface {
	execHelper(ctx context.Context, helperName, serverURL string) (*DockerCredential, error)
}

// dockerConfig represents the relevant parts of ~/.docker/config.json.
type dockerConfig struct {
	CredHelpers map[string]string          `json:"credHelpers"`
	CredsStore  string                     `json:"credsStore"`
	Auths       map[string]dockerAuthEntry `json:"auths"`
}

type dockerAuthEntry struct {
	Auth string `json:"auth"` // base64(user:pass)
}

// dockerHelperBackend resolves credentials by reading the host's Docker config
// and shelling out to credential helpers.
type dockerHelperBackend struct {
	configPath string
	allowed    map[string]bool
	allowAll   bool
	executor   helperExecutor
	logger     *slog.Logger
}

// NewDockerBackend creates a Docker backend that reads from the host's Docker
// credential store. It returns an error only if an explicit config_path is
// specified but cannot be resolved; a missing default config is not an error.
func NewDockerBackend(cfg DockerConfig, logger *slog.Logger) (DockerBackend, error) {
	configPath := cfg.ConfigPath
	if configPath == "" {
		configPath = findDockerConfig()
	} else {
		if strings.HasPrefix(configPath, "~/") {
			home := userHomeDir()
			configPath = filepath.Join(home, configPath[2:])
		}
	}

	if configPath != "" {
		if _, err := os.Stat(configPath); err != nil && cfg.ConfigPath != "" {
			return nil, fmt.Errorf("docker config not found at %s: %w", configPath, err)
		}
	}

	allowed := make(map[string]bool, len(cfg.Registries))
	for _, r := range cfg.Registries {
		allowed[normalizeRegistry(r)] = true
	}

	b := &dockerHelperBackend{
		configPath: configPath,
		allowed:    allowed,
		allowAll:   cfg.AllowAll,
		logger:     logger,
	}
	b.executor = b // default: real execution

	registryInfo := "none"
	if cfg.AllowAll {
		registryInfo = "all (allow_all)"
	} else if len(cfg.Registries) > 0 {
		registryInfo = strings.Join(cfg.Registries, ", ")
	}

	logger.Info("Docker backend initialized",
		"config", configPath,
		"registries", registryInfo,
	)

	return b, nil
}

func (b *dockerHelperBackend) GetCredentials(ctx context.Context, serverURL string) (*DockerCredential, error) {
	normalized := normalizeRegistry(serverURL)

	if !b.allowAll && !b.allowed[normalized] {
		return nil, &dockerError{
			msg:  fmt.Sprintf("registry %q not in allowlist", serverURL),
			code: protocol.DockerCodeNotAllowed,
		}
	}

	cfg, err := b.readConfig()
	if err != nil {
		return nil, &dockerError{
			msg:  fmt.Sprintf("reading docker config: %s", err),
			code: protocol.DockerCodeInternal,
		}
	}

	// Try credHelpers first (per-registry helper).
	// Look up both raw and normalized forms since Docker config may store
	// keys as "https://index.docker.io/v1/" while the guest sends "index.docker.io".
	if helper, ok := lookupConfigMap(cfg.CredHelpers, serverURL, normalized); ok {
		return b.executor.execHelper(ctx, helper, serverURL)
	}

	// Try credsStore (default helper)
	if cfg.CredsStore != "" {
		cred, err := b.executor.execHelper(ctx, cfg.CredsStore, serverURL)
		if err == nil {
			return cred, nil
		}
		b.logger.Debug("default credsStore helper failed, trying auths fallback",
			"helper", cfg.CredsStore, "error", err)
	}

	// Fall back to inline auths
	if auth, ok := lookupConfigMap(cfg.Auths, serverURL, normalized); ok && auth.Auth != "" {
		return decodeInlineAuth(serverURL, auth.Auth)
	}

	return nil, &dockerError{
		msg:  fmt.Sprintf("no credentials found for %q", serverURL),
		code: protocol.DockerCodeNotFound,
	}
}

func (b *dockerHelperBackend) ListCredentials(ctx context.Context) (map[string]string, error) {
	cfg, err := b.readConfig()
	if err != nil {
		return nil, fmt.Errorf("reading docker config: %w", err)
	}

	result := make(map[string]string)

	// Collect from credHelpers
	for serverURL := range cfg.CredHelpers {
		if b.allowAll || b.allowed[normalizeRegistry(serverURL)] {
			result[serverURL] = "(credential helper)"
		}
	}

	// Collect from inline auths
	for serverURL, auth := range cfg.Auths {
		if b.allowAll || b.allowed[normalizeRegistry(serverURL)] {
			if auth.Auth != "" {
				cred, err := decodeInlineAuth(serverURL, auth.Auth)
				if err == nil {
					result[serverURL] = cred.Username
					continue
				}
			}
			result[serverURL] = "(auth entry)"
		}
	}

	return result, nil
}

func (b *dockerHelperBackend) Status() (bool, int) {
	return b.allowAll, len(b.allowed)
}

func (b *dockerHelperBackend) readConfig() (*dockerConfig, error) {
	if b.configPath == "" {
		return &dockerConfig{}, nil
	}

	data, err := os.ReadFile(b.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &dockerConfig{}, nil
		}
		return nil, err
	}

	var cfg dockerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", b.configPath, err)
	}

	return &cfg, nil
}

// execHelper shells out to docker-credential-<helper> get with serverURL on stdin.
func (b *dockerHelperBackend) execHelper(ctx context.Context, helperName, serverURL string) (*DockerCredential, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	bin := "docker-credential-" + helperName
	cmd := exec.CommandContext(ctx, bin, "get")
	cmd.Stdin = strings.NewReader(serverURL)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, &dockerError{
			msg:  fmt.Sprintf("%s failed: %s (stderr: %s)", bin, err, strings.TrimSpace(stderr.String())),
			code: protocol.DockerCodeHelperFailed,
		}
	}

	var cred struct {
		ServerURL string `json:"ServerURL"`
		Username  string `json:"Username"`
		Secret    string `json:"Secret"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &cred); err != nil {
		return nil, &dockerError{
			msg:  fmt.Sprintf("parsing %s output: %s", bin, err),
			code: protocol.DockerCodeHelperFailed,
		}
	}

	return &DockerCredential{
		ServerURL: cred.ServerURL,
		Username:  cred.Username,
		Secret:    cred.Secret,
	}, nil
}

// findDockerConfig returns the path to the Docker config.json, checking
// $DOCKER_CONFIG first, then falling back to ~/.docker/config.json.
func findDockerConfig() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		p := filepath.Join(dir, "config.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	home := userHomeDir()
	p := filepath.Join(home, ".docker", "config.json")
	if _, err := os.Stat(p); err == nil {
		return p
	}

	return ""
}

// normalizeRegistry strips protocol prefix and trailing slashes to produce
// a canonical hostname for allowlist matching.
func normalizeRegistry(s string) string {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, "/v1")
	s = strings.TrimSuffix(s, "/v2")
	return s
}

// lookupConfigMap searches a Docker config map using both the raw and normalized
// registry key, handling the mismatch between Docker's stored keys (e.g.
// "https://index.docker.io/v1/") and the guest-provided hostname ("index.docker.io").
func lookupConfigMap[V any](m map[string]V, raw, normalized string) (V, bool) {
	if v, ok := m[raw]; ok {
		return v, true
	}
	if raw != normalized {
		if v, ok := m[normalized]; ok {
			return v, true
		}
	}
	// Try normalizing the map keys to match
	for k, v := range m {
		if normalizeRegistry(k) == normalized {
			return v, true
		}
	}
	var zero V
	return zero, false
}

func decodeInlineAuth(serverURL, encoded string) (*DockerCredential, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding auth for %s: %w", serverURL, err)
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid auth format for %s", serverURL)
	}

	return &DockerCredential{
		ServerURL: serverURL,
		Username:  parts[0],
		Secret:    parts[1],
	}, nil
}

type dockerError struct {
	msg  string
	code string
}

func (e *dockerError) Error() string { return e.msg }
