package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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

const (
	// tokenRefreshWindow is how long before expiry an on-demand Token re-mints.
	tokenRefreshWindow = 2 * time.Hour
	// The proactive refresh loop re-mints at ~50% of the time remaining until
	// expiry, jittered, clamped to [minRefreshDelay, maxRefreshDelay]; it uses
	// defaultRefreshDelay until a token has been minted.
	defaultRefreshDelay = time.Hour
	minRefreshDelay     = time.Minute
	maxRefreshDelay     = 12 * time.Hour
	// jitterFraction de-synchronizes a fleet re-minting together: the delay is
	// spread ±jitterFraction around its base.
	jitterFraction = 0.25
)

// minter is the subset of CredentialMinter that credentialSource needs; an
// interface so tests can inject a fake without a live SSH server.
type minter interface {
	Mint(ctx context.Context, t ServerTarget) (string, time.Time, error)
}

// inflightMint coordinates concurrent mints: only one runs at a time, joiners
// wait on done then read token/err. This is the single-flight that both keeps a
// re-mint from being duplicated and lets the network call run off s.mu (so a
// proactive refresh doesn't block a Token caller serving the still-valid token).
type inflightMint struct {
	done  chan struct{}
	token string
	err   error
}

// credentialSource is an sdk.TokenProvider backed by the SSH credential minter:
// it caches a minted token and re-mints on demand (near expiry, or after a 401
// via Invalidate) and proactively (refreshLoop). A host-key pin mismatch is
// TERMINAL — a possible MITM — so the source fails closed and never serves a
// token for that server again, rather than downgrading to a weaker credential.
type credentialSource struct {
	ctx    context.Context
	mint   minter
	target ServerTarget

	mu          sync.Mutex
	token       string
	expiry      time.Time
	terminalErr error
	inflight    *inflightMint
}

func newCredentialSource(ctx context.Context, m minter, t ServerTarget) *credentialSource {
	return &credentialSource{ctx: ctx, mint: m, target: t}
}

// Token returns the current credentials token, minting or re-minting as needed.
func (s *credentialSource) Token() (string, error) {
	s.mu.Lock()
	if s.terminalErr != nil {
		err := s.terminalErr
		s.mu.Unlock()
		return "", err
	}
	if s.token != "" && !s.staleLocked() {
		tok := s.token
		s.mu.Unlock()
		return tok, nil
	}
	call := s.obtainLocked()
	s.mu.Unlock()
	<-call.done
	return call.token, call.err
}

// Invalidate clears the cached token so the next Token re-mints. Called by the
// SDK after a 401 (the server rejected the cached token).
func (s *credentialSource) Invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = ""
}

// refresh proactively re-mints, best-effort (errors surface on the next Token).
// Driven by refreshLoop so an idle server's token stays fresh.
func (s *credentialSource) refresh() {
	s.mu.Lock()
	if s.terminalErr != nil {
		s.mu.Unlock()
		return
	}
	call := s.obtainLocked()
	s.mu.Unlock()
	<-call.done
}

// staleLocked reports whether the cached token is within tokenRefreshWindow of
// expiry (a zero expiry is a non-expiring token). The caller holds s.mu.
func (s *credentialSource) staleLocked() bool {
	return !s.expiry.IsZero() && !time.Now().Before(s.expiry.Add(-tokenRefreshWindow))
}

// obtainLocked returns the in-flight mint, starting one (off s.mu, in a goroutine)
// if none is running so N concurrent callers share ONE mint. The caller holds s.mu.
func (s *credentialSource) obtainLocked() *inflightMint {
	if s.inflight != nil {
		return s.inflight
	}
	call := &inflightMint{done: make(chan struct{})}
	s.inflight = call
	go s.doMint(call)
	return call
}

// doMint performs the mint off s.mu, then stores the result under it. A host-key
// pin mismatch is recorded as terminal (fail closed) so it is never retried.
func (s *credentialSource) doMint(call *inflightMint) {
	tok, exp, err := s.mint.Mint(s.ctx, s.target)
	s.mu.Lock()
	s.inflight = nil
	switch {
	case err == nil:
		s.token, s.expiry = tok, exp
		call.token = tok // error paths leave call.token "" (zero)
	case errors.Is(err, sdk.ErrHostKeyMismatch):
		s.terminalErr = fmt.Errorf("refusing to broker %q: SSH host key pin mismatch (possible MITM): %w", s.target.Name, err)
		err = s.terminalErr
	}
	call.err = err
	s.mu.Unlock()
	close(call.done)
}

// refreshLoop proactively re-mints at ~50% of the time until expiry (jittered),
// so an idle server's token stays fresh and a fleet re-minting after a server
// restart is spread out rather than thundering. Stops when ctx is done.
func (s *credentialSource) refreshLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.nextRefreshDelay()):
		}
		if ctx.Err() != nil {
			return
		}
		s.refresh()
	}
}

// nextRefreshDelay is ~50% of the time until the cached token expires, jittered
// ±25%, clamped to [minRefreshDelay, maxRefreshDelay]; defaultRefreshDelay when
// no token has been minted yet.
func (s *credentialSource) nextRefreshDelay() time.Duration {
	s.mu.Lock()
	exp := s.expiry
	s.mu.Unlock()

	base := defaultRefreshDelay
	if !exp.IsZero() {
		if remaining := time.Until(exp); remaining > 0 {
			base = remaining / 2
		} else {
			base = minRefreshDelay
		}
	}
	// (2*rand-1) is uniform in [-1,1), so this spreads d by ±jitterFraction of base.
	d := base + time.Duration((2*rand.Float64()-1)*jitterFraction*float64(base))
	return min(max(d, minRefreshDelay), maxRefreshDelay)
}
