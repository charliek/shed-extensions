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
    host: prod.example
    http_port: 8080
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
  open:
    host: open.example
    http_port: 8080
  secure:
    host: secure.example
    ssh_port: 2222
`)
	fm := &fakeMinter{results: []mintResult{{tok: "x", exp: time.Now().Add(time.Hour)}}}
	p := newControlTokenProvider(context.Background(), fm, cfg)

	if _, _, err := p.Token("missing"); err == nil {
		t.Error("expected an error for an unknown server")
	}
	if _, _, err := p.Token("open"); err == nil {
		t.Error("expected an error for a server with no ssh_port")
	}
	if fm.calls != 0 {
		t.Errorf("mint calls = %d, want 0 (both error before minting)", fm.calls)
	}
}
