package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// controlTokenProvider mints and caches CONTROL-scoped tokens for named servers
// on the desktop's behalf (the token.get UDS request). Minting is BROAD: it can
// mint for any server in the shed CLI config the agent's SSH key is allowlisted
// on — not only the discovery-scoped servers it brokers credentials for — so the
// desktop can reach a server's control plane without the agent watching it.
type controlTokenProvider struct {
	ctx        context.Context
	mint       minter
	sourcePath string // the shed CLI config (~/.shed/config.yaml) to resolve servers from

	mu      sync.Mutex
	sources map[string]*credentialSource // by server name, scopeControl
}

func newControlTokenProvider(ctx context.Context, m minter, sourcePath string) *controlTokenProvider {
	return &controlTokenProvider{
		ctx:        ctx,
		mint:       m,
		sourcePath: expandTilde(sourcePath),
		sources:    make(map[string]*credentialSource),
	}
}

// Token returns a control-scoped token (and its expiry) for the named server,
// minting over SSH and caching/refreshing per server. It errors when the server
// is unknown or has no SSH endpoint to mint over.
func (p *controlTokenProvider) Token(serverName string) (string, time.Time, error) {
	target, err := p.resolve(serverName)
	if err != nil {
		return "", time.Time{}, err
	}
	return p.sourceFor(serverName, target).tokenWithExpiry()
}

// resolve looks the server up by name in the shed CLI config and returns its
// target, requiring an SSH endpoint (only a secure server can mint).
func (p *controlTokenProvider) resolve(name string) (ServerTarget, error) {
	targets, err := LoadDiscoveredServers(p.sourcePath)
	if err != nil {
		return ServerTarget{}, fmt.Errorf("reading server config: %w", err)
	}
	for _, t := range targets {
		if t.Name == name {
			if t.SSHHost == "" || t.SSHPort == 0 {
				return ServerTarget{}, fmt.Errorf("server %q has no ssh_port to mint a control token over", name)
			}
			return t, nil
		}
	}
	return ServerTarget{}, fmt.Errorf("unknown server %q", name)
}

// sourceFor returns the per-server control-token source, (re)creating it when the
// server's endpoint changed since it was cached.
func (p *controlTokenProvider) sourceFor(name string, target ServerTarget) *credentialSource {
	p.mu.Lock()
	defer p.mu.Unlock()
	if src, ok := p.sources[name]; ok && src.target == target {
		return src
	}
	src := newCredentialSource(p.ctx, p.mint, target, scopeControl)
	p.sources[name] = src
	return src
}
