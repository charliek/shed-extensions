package main

// ApprovalGate controls biometric/Touch ID approval for credential operations.
type ApprovalGate interface {
	// Enabled returns true if approval is configured and active.
	Enabled() bool

	// Approve requests user approval for an operation. Returns nil if approved.
	Approve(shedName, reason string) error
}

// noopGate always approves — used when approval is disabled or on non-macOS.
type noopGate struct{}

func (g *noopGate) Enabled() bool             { return false }
func (g *noopGate) Approve(_, _ string) error { return nil }
