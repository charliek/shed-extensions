package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
server: http://localhost:9090
ssh:
  mode: agent-forward
  approval:
    enabled: true
    policy: per-request
    session_ttl: 2h
logging:
  enabled: true
  path: /tmp/test-audit.log
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Server != "http://localhost:9090" {
		t.Errorf("server: got %q, want %q", cfg.Server, "http://localhost:9090")
	}
	if cfg.SSH.Mode != "agent-forward" {
		t.Errorf("ssh.mode: got %q, want %q", cfg.SSH.Mode, "agent-forward")
	}
	if !cfg.SSH.Approval.Enabled {
		t.Error("ssh.approval.enabled: got false, want true")
	}
	if cfg.SSH.Approval.Policy != "per-request" {
		t.Errorf("ssh.approval.policy: got %q, want %q", cfg.SSH.Approval.Policy, "per-request")
	}
	if cfg.SSH.Approval.SessionTTL != "2h" {
		t.Errorf("ssh.approval.session_ttl: got %q, want %q", cfg.SSH.Approval.SessionTTL, "2h")
	}
	if cfg.Logging.Path != "/tmp/test-audit.log" {
		t.Errorf("logging.path: got %q, want %q", cfg.Logging.Path, "/tmp/test-audit.log")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `server: http://localhost:8080`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.SSH.Mode != "" {
		t.Errorf("ssh.mode default: got %q, want empty", cfg.SSH.Mode)
	}
	if cfg.SSH.Approval.Enabled {
		t.Error("ssh.approval.enabled default: got true, want false")
	}
	if cfg.SSH.Approval.Policy != "per-session" {
		t.Errorf("ssh.approval.policy default: got %q, want %q", cfg.SSH.Approval.Policy, "per-session")
	}
	if cfg.Logging.Enabled != true {
		t.Error("logging.enabled default: got false, want true")
	}
}

func TestLoadConfigMissing(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte("{{invalid yaml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
