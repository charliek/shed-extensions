package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
