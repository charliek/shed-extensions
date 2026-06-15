package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeShedConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestControlTokenProviderMintsAndCaches(t *testing.T) {
	cfg := writeShedConfig(t, `
servers:
  prod:
    api_url: https://prod.example:8443
    host: prod.example
    ssh_port: 2222
`)
	far := time.Now().Add(24 * time.Hour)
	fm := &fakeMinter{results: []mintResult{{tok: "ctl-1", exp: far}}}
	p := newControlTokenProvider(context.Background(), fm, cfg)

	tok, exp, err := p.Token("prod")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "ctl-1" {
		t.Errorf("token = %q, want ctl-1", tok)
	}
	if !exp.Equal(far) {
		t.Errorf("expiry = %v, want %v", exp, far)
	}
	// A second call is served from the cached per-server source — no re-mint.
	if _, _, err := p.Token("prod"); err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if fm.calls != 1 {
		t.Errorf("mint calls = %d, want 1 (second served from cache)", fm.calls)
	}
}

func TestControlTokenProviderErrors(t *testing.T) {
	cfg := writeShedConfig(t, `
servers:
  open-no-ssh:
    host: open1.example
    http_port: 8080
  open-http:
    host: open2.example
    http_port: 8080
    ssh_port: 2222
`)
	fm := &fakeMinter{results: []mintResult{{tok: "x", exp: time.Now().Add(time.Hour)}}}
	p := newControlTokenProvider(context.Background(), fm, cfg)

	if _, _, err := p.Token("missing"); err == nil {
		t.Error("expected an error for an unknown server")
	}
	// No SSH endpoint at all -> can't bootstrap a mint.
	if _, _, err := p.Token("open-no-ssh"); err == nil {
		t.Error("expected an error for a server with no ssh endpoint")
	}
	// Has an ssh endpoint but is plain http (open mode) -> not mintable. This is the
	// case the new https gate adds; the old SSHPort>0 gate would have let it through
	// and attempted a doomed mint.
	if _, _, err := p.Token("open-http"); err == nil {
		t.Error("expected an error for an open (http) server with an ssh endpoint")
	}
	if fm.calls != 0 {
		t.Errorf("mint calls = %d, want 0 (all error before minting)", fm.calls)
	}
}
