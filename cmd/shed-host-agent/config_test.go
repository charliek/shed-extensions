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

func TestLoadConfigAWS(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	content := `
server: http://localhost:8080
aws:
  source_profile: staging
  default_role: arn:aws:iam::123456789012:role/dev
  session_duration: 2h
  cache_refresh_before: 10m
  sheds:
    my-service:
      role: arn:aws:iam::123456789012:role/my-service
    tests:
      role: arn:aws:iam::123456789012:role/readonly
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.AWS.SourceProfile != "staging" {
		t.Errorf("aws.source_profile: got %q, want %q", cfg.AWS.SourceProfile, "staging")
	}
	if cfg.AWS.DefaultRole != "arn:aws:iam::123456789012:role/dev" {
		t.Errorf("aws.default_role: got %q", cfg.AWS.DefaultRole)
	}
	if cfg.AWS.SessionDuration != "2h" {
		t.Errorf("aws.session_duration: got %q, want %q", cfg.AWS.SessionDuration, "2h")
	}
	if cfg.AWS.CacheRefreshBefore != "10m" {
		t.Errorf("aws.cache_refresh_before: got %q, want %q", cfg.AWS.CacheRefreshBefore, "10m")
	}
	if len(cfg.AWS.Sheds) != 2 {
		t.Fatalf("aws.sheds: got %d entries, want 2", len(cfg.AWS.Sheds))
	}
	if cfg.AWS.Sheds["my-service"].Role != "arn:aws:iam::123456789012:role/my-service" {
		t.Errorf("aws.sheds.my-service.role: got %q", cfg.AWS.Sheds["my-service"].Role)
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
	// AWS defaults
	if cfg.AWS.SourceProfile != "default" {
		t.Errorf("aws.source_profile default: got %q, want %q", cfg.AWS.SourceProfile, "default")
	}
	if cfg.AWS.SessionDuration != "1h" {
		t.Errorf("aws.session_duration default: got %q, want %q", cfg.AWS.SessionDuration, "1h")
	}
	if cfg.AWS.CacheRefreshBefore != "5m" {
		t.Errorf("aws.cache_refresh_before default: got %q, want %q", cfg.AWS.CacheRefreshBefore, "5m")
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
