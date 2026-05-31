package main

import (
	"context"
	"fmt"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// desktopGate delegates SSH approval decisions to a connected shed-desktop
// app over the UDS. SSH-only in v1; fail-closed when no app is connected.
// The per-request budget is owned by the DesktopServer (s.timeout).
type desktopGate struct {
	server *DesktopServer
}

func (g *desktopGate) Enabled() bool  { return true }
func (g *desktopGate) Method() string { return "shed-desktop" }

func (g *desktopGate) Approve(shedName, reason string) error {
	approved, err := g.server.RequestApproval(
		context.Background(), protocol.NamespaceSSHAgent, protocol.SSHOpSign, shedName, reason)
	if err != nil {
		return fmt.Errorf("shed-desktop approval: %w", err)
	}
	if !approved {
		return fmt.Errorf("approval denied by shed-desktop")
	}
	return nil
}
