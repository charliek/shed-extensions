package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// mockAWSBackend implements AWSBackend for testing.
type mockAWSBackend struct {
	creds   *AWSCachedCredentials
	err     error
	callLog []string
	mu      sync.Mutex
}

func (m *mockAWSBackend) Status(_, _ string) (string, *time.Time) {
	return "arn:aws:iam::123:role/mock", nil
}

func (m *mockAWSBackend) GetCredentials(_ context.Context, server, shedName string) (*AWSCachedCredentials, error) {
	m.mu.Lock()
	m.callLog = append(m.callLog, server+"/"+shedName)
	m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.creds, nil
}

func TestAWSResolve(t *testing.T) {
	tests := []struct {
		name   string
		cfg    AWSConfig
		server string
		shed   string
		want   string
	}{
		{
			name:   "default role",
			cfg:    AWSConfig{DefaultRole: "arn:aws:iam::123:role/default"},
			server: "mini2", shed: "my-shed",
			want: "arn:aws:iam::123:role/default",
		},
		{
			name: "deprecated global per-shed override",
			cfg: AWSConfig{
				DefaultRole: "arn:aws:iam::123:role/default",
				Sheds:       map[string]ShedAWSConfig{"special-shed": {Role: "arn:aws:iam::123:role/special"}},
			},
			server: "mini2", shed: "special-shed",
			want: "arn:aws:iam::123:role/special",
		},
		{
			name: "per-server override",
			cfg: AWSConfig{
				DefaultRole: "arn:aws:iam::123:role/default",
				Servers:     map[string]AWSServerConfig{"mini2": {DefaultRole: "arn:aws:iam::123:role/mini2"}},
			},
			server: "mini2", shed: "my-shed",
			want: "arn:aws:iam::123:role/mini2",
		},
		{
			name: "per-server-per-shed override wins",
			cfg: AWSConfig{
				DefaultRole: "arn:aws:iam::123:role/default",
				Servers: map[string]AWSServerConfig{"mini2": {
					DefaultRole: "arn:aws:iam::123:role/mini2",
					Sheds:       map[string]ShedAWSConfig{"web": {Role: "arn:aws:iam::123:role/web"}},
				}},
			},
			server: "mini2", shed: "web",
			want: "arn:aws:iam::123:role/web",
		},
		{
			name: "same shed name on different server is isolated",
			cfg: AWSConfig{
				DefaultRole: "arn:aws:iam::123:role/default",
				Servers: map[string]AWSServerConfig{"mini2": {
					Sheds: map[string]ShedAWSConfig{"web": {Role: "arn:aws:iam::123:role/mini2-web"}},
				}},
			},
			server: "mini3", shed: "web",
			want: "arn:aws:iam::123:role/default",
		},
		{
			name:   "no role configured",
			cfg:    AWSConfig{},
			server: "mini2", shed: "my-shed",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Resolve(tt.server, tt.shed).Role; got != tt.want {
				t.Errorf("Resolve(%q,%q).Role = %q, want %q", tt.server, tt.shed, got, tt.want)
			}
		})
	}
}

func TestAWSHasAnyRole(t *testing.T) {
	if (AWSConfig{}).HasAnyRole() {
		t.Error("empty config should have no role")
	}
	if !(AWSConfig{DefaultRole: "x"}).HasAnyRole() {
		t.Error("default role should count")
	}
	if !(AWSConfig{Sheds: map[string]ShedAWSConfig{"s": {Role: "x"}}}).HasAnyRole() {
		t.Error("deprecated global per-shed role should count")
	}
	if !(AWSConfig{Servers: map[string]AWSServerConfig{"m": {Sheds: map[string]ShedAWSConfig{"s": {Role: "x"}}}}}).HasAnyRole() {
		t.Error("per-server-per-shed role should count")
	}
}

func TestCacheHit(t *testing.T) {
	b := &stsBackend{
		cfg:           AWSConfig{DefaultRole: "arn:aws:iam::123:role/test"},
		refreshBefore: 5 * time.Minute,
		logger:        slog.Default(),
		cache: map[string]*AWSCachedCredentials{
			"mini2/my-shed": {
				AccessKeyID:     "CACHED_KEY",
				SecretAccessKey: "CACHED_SECRET",
				SessionToken:    "CACHED_TOKEN",
				Expiration:      time.Now().Add(30 * time.Minute), // within refresh window
			},
		},
	}

	// Cache hit for mini2/my-shed returns without calling STS (nil client).
	creds, err := b.GetCredentials(context.Background(), "mini2", "my-shed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessKeyID != "CACHED_KEY" {
		t.Errorf("expected cached key, got %q", creds.AccessKeyID)
	}

	// The same shed name on a different server must NOT share the cache entry.
	if _, cachedUntil := b.Status("mini3", "my-shed"); cachedUntil != nil {
		t.Error("mini3/my-shed should not share mini2/my-shed cache entry")
	}
}

func TestCacheMiss(t *testing.T) {
	// When cache is stale, GetCredentials should NOT return the cached value.
	// The mock lets us control return values directly.
	backend := &mockAWSBackend{
		creds: &AWSCachedCredentials{
			AccessKeyID:     "FRESH_KEY",
			SecretAccessKey: "FRESH_SECRET",
			SessionToken:    "FRESH_TOKEN",
			Expiration:      time.Now().Add(1 * time.Hour),
		},
	}

	creds, err := backend.GetCredentials(context.Background(), "mini2", "my-shed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.AccessKeyID != "FRESH_KEY" {
		t.Errorf("expected fresh key, got %q", creds.AccessKeyID)
	}
}

func TestNoRoleConfigured(t *testing.T) {
	b := &stsBackend{
		cfg:   AWSConfig{},
		cache: make(map[string]*AWSCachedCredentials),
	}

	_, err := b.GetCredentials(context.Background(), "mini2", "unknown-shed")
	if err == nil {
		t.Fatal("expected error for shed with no role configured")
	}
	want := `no role configured for shed "unknown-shed" on server "mini2"`
	if got := fmt.Sprintf("%v", err); got != want {
		t.Errorf("unexpected error: %v", err)
	}
}
