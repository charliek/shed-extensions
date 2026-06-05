package main

import (
	"context"
	"fmt"
)

// desktopGate delegates an extension's approval decisions to a connected
// shed-desktop app over the UDS. Fail-closed when no app is connected. The
// namespace/op identify the extension in the approval_request; the per-request
// budget is owned by the DesktopServer (s.timeout).
type desktopGate struct {
	server    *DesktopServer
	namespace string
	op        string
}

func (g *desktopGate) Method() string { return PolicyShedDesktop }

func (g *desktopGate) Approve(server, shedName, reason string) (ApprovalOutcome, error) {
	dec, err := g.server.RequestApproval(
		context.Background(), g.namespace, g.op, server, shedName, reason)
	if err != nil {
		return ApprovalOutcome{}, fmt.Errorf("shed-desktop approval: %w", err)
	}
	if !dec.approved {
		return ApprovalOutcome{}, fmt.Errorf("approval denied by shed-desktop")
	}
	decidedBy := dec.decidedBy
	if decidedBy == "" {
		decidedBy = "user"
	}
	return ApprovalOutcome{DecidedBy: decidedBy, Scope: dec.scope, TTL: dec.ttl}, nil
}
