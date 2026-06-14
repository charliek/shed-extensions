package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	sdk "github.com/charliek/shed/sdk"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// CredentialMinter bootstraps the host-agent's own credentials-scoped token over
// a server's SSH _bootstrap channel, using the agent's SSH identity key. This
// replaces a pasted credentials_token: the agent authenticates with a key that
// is already on the server's allowlist (the same key `shed server add` used),
// and the server mints a short-TTL token bound to that key.
type CredentialMinter struct {
	signerPath     string // the agent's SSH identity key (e.g. ~/.ssh/id_ed25519)
	knownHostsPath string // where the server's SSH host key is pinned (~/.shed/known_hosts)
}

// NewCredentialMinter builds a minter from the SSH identity key path and the
// known_hosts file that pins server host keys. Both paths are tilde-expanded.
func NewCredentialMinter(signerPath, knownHostsPath string) *CredentialMinter {
	return &CredentialMinter{
		signerPath:     expandTilde(signerPath),
		knownHostsPath: expandTilde(knownHostsPath),
	}
}

// Mint bootstraps a fresh credentials token for t over its SSH endpoint and
// returns the token with its expiry. The host key is verified against the pin
// already in known_hosts (the same trust `shed server add` established), so this
// never trusts an unpinned server.
func (m *CredentialMinter) Mint(ctx context.Context, t ServerTarget) (string, time.Time, error) {
	signer, err := loadSSHSigner(m.signerPath)
	if err != nil {
		return "", time.Time{}, err
	}
	pin, err := knownHostsPin(m.knownHostsPath, t.SSHHost, t.SSHPort)
	if err != nil {
		return "", time.Time{}, err
	}
	addr := net.JoinHostPort(t.SSHHost, strconv.Itoa(t.SSHPort))
	bundle, err := sdk.Bootstrap(ctx, addr, signer, pin, "credentials", "host-agent")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("bootstrapping credentials token for %q: %w", t.Name, err)
	}
	return bundle.Token, bundle.ExpiresAt, nil
}

// loadSSHSigner reads and parses an unencrypted private key into a signer. A
// passphrase-protected key is rejected with a clear error (the daemon has no way
// to prompt) — the agent's identity key must be usable non-interactively.
func loadSSHSigner(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ssh identity key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("ssh identity key %s is passphrase-protected; the host-agent needs an unencrypted key", path)
		}
		return nil, fmt.Errorf("parsing ssh identity key %s: %w", path, err)
	}
	return signer, nil
}

// knownHostsPin returns the SHA-256 fingerprint ("SHA256:...") of the host key
// pinned for host:port in the known_hosts file, in the form sdk.Bootstrap
// expects. It is the same trust anchor `shed server add` wrote via AddKnownHost.
// Returns an error when the file is unreadable or has no entry for the endpoint.
func knownHostsPin(knownHostsPath, host string, port int) (string, error) {
	data, err := os.ReadFile(knownHostsPath)
	if err != nil {
		return "", fmt.Errorf("reading known_hosts %s: %w", knownHostsPath, err)
	}
	// knownhosts.Normalize yields the stored form: "[host]:port" for a non-22
	// port, bare "host" for port 22 — matching how OpenSSH (and shed) records it.
	want := knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(port)))
	for len(data) > 0 {
		marker, hosts, pubKey, _, rest, err := ssh.ParseKnownHosts(data)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parsing known_hosts %s: %w", knownHostsPath, err)
		}
		data = rest
		// Skip marked lines: a @revoked key must never be used as a pin, and a
		// @cert-authority line is a CA, not a host-key pin.
		if marker != "" {
			continue
		}
		for _, h := range hosts {
			if h == want {
				return ssh.FingerprintSHA256(pubKey), nil
			}
		}
	}
	return "", fmt.Errorf("no host key pinned for %s in %s (run `shed server add` first)", want, knownHostsPath)
}
