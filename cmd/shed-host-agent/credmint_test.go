package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/charliek/shed/sdk"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// writeTestKey generates an ed25519 key, writes its private half (OpenSSH PEM)
// to dir/name, and returns the path plus the SSH public key.
func writeTestKey(t *testing.T, dir, name string) (string, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return path, pub
}

func TestLoadSSHSigner(t *testing.T) {
	dir := t.TempDir()
	path, pub := writeTestKey(t, dir, "id_ed25519")

	signer, err := loadSSHSigner(path)
	if err != nil {
		t.Fatalf("loadSSHSigner: %v", err)
	}
	if got, want := ssh.FingerprintSHA256(signer.PublicKey()), ssh.FingerprintSHA256(pub); got != want {
		t.Errorf("loaded signer key = %q, want %q", got, want)
	}
}

func TestLoadSSHSignerErrors(t *testing.T) {
	if _, err := loadSSHSigner(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a missing key file")
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("not a key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSSHSigner(bad); err == nil {
		t.Error("expected an error for an unparseable key")
	}
}

func TestKnownHostsPin(t *testing.T) {
	dir := t.TempDir()
	_, hostPub := writeTestKey(t, dir, "hostkey")
	const host, port = "mini3", 2222

	// Write a known_hosts line exactly as OpenSSH/shed would, keyed by [host]:port.
	addr := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	khPath := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(khPath, []byte(knownhosts.Line([]string{addr}, hostPub)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	pin, err := knownHostsPin(khPath, host, port)
	if err != nil {
		t.Fatalf("knownHostsPin: %v", err)
	}
	if want := ssh.FingerprintSHA256(hostPub); pin != want {
		t.Errorf("pin = %q, want %q", pin, want)
	}
}

func TestKnownHostsPinErrors(t *testing.T) {
	dir := t.TempDir()
	_, hostPub := writeTestKey(t, dir, "hostkey")

	// A known_hosts that pins a different host than the one we query.
	khPath := filepath.Join(dir, "known_hosts")
	addr := knownhosts.Normalize(net.JoinHostPort("other", "2222"))
	if err := os.WriteFile(khPath, []byte(knownhosts.Line([]string{addr}, hostPub)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := knownHostsPin(filepath.Join(dir, "absent"), "mini3", 2222); err == nil {
		t.Error("expected an error for a missing known_hosts file")
	}
	if _, err := knownHostsPin(khPath, "mini3", 2222); err == nil {
		t.Error("expected an error when the host has no pinned key")
	}
}

func TestKnownHostsPinSkipsRevoked(t *testing.T) {
	dir := t.TempDir()
	_, hostPub := writeTestKey(t, dir, "hostkey")
	const host, port = "mini3", 2222
	addr := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))

	// A @revoked line for the exact host must NOT be returned as a usable pin.
	khPath := filepath.Join(dir, "known_hosts")
	line := "@revoked " + knownhosts.Line([]string{addr}, hostPub) + "\n"
	if err := os.WriteFile(khPath, []byte(line), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := knownHostsPin(khPath, host, port); err == nil {
		t.Error("a @revoked host key must not be used as a pin")
	}
}

type mintResult struct {
	tok string
	exp time.Time
	err error
}

// fakeMinter returns canned results in sequence (repeating the last) and counts
// calls, so credentialSource can be tested without a live SSH server.
type fakeMinter struct {
	mu      sync.Mutex
	calls   int
	results []mintResult
}

func (f *fakeMinter) Mint(context.Context, ServerTarget) (string, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	if i >= len(f.results) {
		i = len(f.results) - 1
	}
	f.calls++
	r := f.results[i]
	return r.tok, r.exp, r.err
}

func TestCredentialSourceCachesAndReMints(t *testing.T) {
	far := time.Now().Add(24 * time.Hour)
	fm := &fakeMinter{results: []mintResult{{tok: "tok1", exp: far}, {tok: "tok2", exp: far}}}
	s := newCredentialSource(context.Background(), fm, ServerTarget{Name: "s"})

	if got, _ := s.Token(); got != "tok1" {
		t.Fatalf("Token = %q, want tok1", got)
	}
	if got, _ := s.Token(); got != "tok1" {
		t.Errorf("cached Token = %q, want tok1", got)
	}
	if fm.calls != 1 {
		t.Errorf("mint calls = %d, want 1 (second Token served from cache)", fm.calls)
	}
	s.Invalidate()
	if got, _ := s.Token(); got != "tok2" {
		t.Errorf("post-invalidate Token = %q, want tok2 (re-mint)", got)
	}
	if fm.calls != 2 {
		t.Errorf("mint calls = %d, want 2", fm.calls)
	}
}

func TestCredentialSourcePinMismatchTerminal(t *testing.T) {
	fm := &fakeMinter{results: []mintResult{{err: fmt.Errorf("bootstrap: %w", sdk.ErrHostKeyMismatch)}}}
	s := newCredentialSource(context.Background(), fm, ServerTarget{Name: "s"})

	if _, err := s.Token(); err == nil {
		t.Fatal("expected a terminal error on a host-key pin mismatch")
	}
	// A pin mismatch is terminal: the second call fails closed WITHOUT re-minting.
	if _, err := s.Token(); err == nil {
		t.Error("expected the terminal error to persist")
	}
	if fm.calls != 1 {
		t.Errorf("mint calls = %d, want 1 (a pin mismatch must never be retried)", fm.calls)
	}
}

func TestCredentialSourceReMintsNearExpiry(t *testing.T) {
	near := time.Now().Add(tokenRefreshWindow / 2) // inside the refresh window
	far := time.Now().Add(24 * time.Hour)
	fm := &fakeMinter{results: []mintResult{{tok: "near", exp: near}, {tok: "fresh", exp: far}}}
	s := newCredentialSource(context.Background(), fm, ServerTarget{Name: "s"})

	if got, _ := s.Token(); got != "near" {
		t.Fatalf("Token = %q, want near", got)
	}
	// The cached token is within tokenRefreshWindow of expiry → the next Token re-mints.
	if got, _ := s.Token(); got != "fresh" {
		t.Errorf("Token = %q, want fresh (near-expiry re-mint)", got)
	}
	if fm.calls != 2 {
		t.Errorf("mint calls = %d, want 2", fm.calls)
	}
}

// minterFunc adapts a function to the minter interface.
type minterFunc func(context.Context, ServerTarget) (string, time.Time, error)

func (f minterFunc) Mint(ctx context.Context, t ServerTarget) (string, time.Time, error) {
	return f(ctx, t)
}

func TestCredentialSourceSingleFlight(t *testing.T) {
	release := make(chan struct{})
	var calls int32
	mf := minterFunc(func(context.Context, ServerTarget) (string, time.Time, error) {
		atomic.AddInt32(&calls, 1)
		<-release // hold the mint open while concurrent callers pile up
		return "tok", time.Now().Add(24 * time.Hour), nil
	})
	s := newCredentialSource(context.Background(), mf, ServerTarget{Name: "s"})

	const n = 8
	var wg sync.WaitGroup
	got := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = s.Token()
		}(i)
	}
	time.Sleep(50 * time.Millisecond) // let all n goroutines join the single mint
	close(release)
	wg.Wait()

	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("mint calls = %d, want 1 (single-flight collapses concurrent callers)", c)
	}
	for i, tok := range got {
		if tok != "tok" {
			t.Errorf("got[%d] = %q, want tok", i, tok)
		}
	}
}

func TestCredentialSourceProactiveRefresh(t *testing.T) {
	far := time.Now().Add(24 * time.Hour)
	fm := &fakeMinter{results: []mintResult{{tok: "first", exp: far}, {tok: "second", exp: far}}}
	s := newCredentialSource(context.Background(), fm, ServerTarget{Name: "s"})

	if tok, _ := s.Token(); tok != "first" {
		t.Fatalf("Token = %q, want first", tok)
	}
	s.refresh() // proactive re-mint, even though the cached token is still valid
	if fm.calls != 2 {
		t.Errorf("mint calls = %d, want 2 (proactive refresh re-mints)", fm.calls)
	}
	if tok, _ := s.Token(); tok != "second" {
		t.Errorf("post-refresh Token = %q, want second", tok)
	}
}
