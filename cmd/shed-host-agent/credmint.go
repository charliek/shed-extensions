package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
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

	mu     sync.Mutex
	signer ssh.Signer // cached after the first successful load (parsed once, used for every mint)
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
	signer, err := m.cachedSigner()
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

// cachedSigner returns the agent's SSH identity signer, parsing the key file on
// the first call and caching it (one shared signer mints for every server). It
// caches only on success, so a key that appears later can still be picked up.
func (m *CredentialMinter) cachedSigner() (ssh.Signer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.signer != nil {
		return m.signer, nil
	}
	s, err := loadSSHSigner(m.signerPath)
	if err != nil {
		return nil, err
	}
	m.signer = s
	return s, nil
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

// tokenRefreshWindow is how long before a credentials token's expiry the source
// proactively re-mints, so a request never races expiry.
const tokenRefreshWindow = 2 * time.Hour

// minter is the subset of CredentialMinter that credentialSource needs; an
// interface so tests can inject a fake without a live SSH server.
type minter interface {
	Mint(ctx context.Context, t ServerTarget) (string, time.Time, error)
}

// credentialSource is an sdk.TokenProvider backed by the SSH credential minter:
// it caches a minted token and re-mints on demand (near expiry, or after a 401
// via Invalidate). A host-key pin mismatch is TERMINAL — a possible MITM — so the
// source fails closed and never serves a token for that server again, rather than
// silently downgrading to a weaker credential.
type credentialSource struct {
	ctx    context.Context
	mint   minter
	target ServerTarget

	mu          sync.Mutex
	token       string
	expiry      time.Time
	terminalErr error
}

func newCredentialSource(ctx context.Context, m minter, t ServerTarget) *credentialSource {
	return &credentialSource{ctx: ctx, mint: m, target: t}
}

// Token returns the current credentials token, minting or re-minting as needed.
func (s *credentialSource) Token() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminalErr != nil {
		return "", s.terminalErr
	}
	// Serve the cached token until it is within tokenRefreshWindow of expiry (a
	// zero expiry means a non-expiring token — only Invalidate re-mints it).
	if s.token != "" && (s.expiry.IsZero() || time.Now().Before(s.expiry.Add(-tokenRefreshWindow))) {
		return s.token, nil
	}
	return s.mintLocked()
}

// Invalidate clears the cached token so the next Token re-mints. Called by the
// SDK after a 401 (the server rejected the cached token).
func (s *credentialSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

// mintLocked re-mints and caches the token; the caller must hold s.mu. A host-key
// pin mismatch is recorded as terminal (fail closed) so it is never retried.
func (s *credentialSource) mintLocked() (string, error) {
	tok, exp, err := s.mint.Mint(s.ctx, s.target)
	if err != nil {
		if errors.Is(err, sdk.ErrHostKeyMismatch) {
			s.terminalErr = fmt.Errorf("refusing to broker %q: SSH host key pin mismatch (possible MITM): %w", s.target.Name, err)
			return "", s.terminalErr
		}
		return "", err // transient (unreachable / auth) — a later Token may retry
	}
	s.token, s.expiry = tok, exp
	return tok, nil
}
