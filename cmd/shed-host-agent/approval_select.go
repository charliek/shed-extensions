package main

import "fmt"

// selectApprovalGate chooses the approval gate (platform-neutral so the
// darwin Touch ID path and the !darwin stub stay untouched behind their build
// tags). With ssh.approval.method "shed-desktop" it delegates to a connected
// shed-desktop app; otherwise it falls back to newApprovalGate (Touch ID on
// darwin, no-op elsewhere).
func selectApprovalGate(cfg Config, desktop *DesktopServer) ApprovalGate {
	if cfg.SSH.Approval.Method == "shed-desktop" {
		if desktop == nil {
			// Misconfiguration: delegation requested but desktop.enabled is
			// false. Fail closed rather than silently fall back.
			return &denyGate{}
		}
		return &desktopGate{server: desktop}
	}
	return newApprovalGate(cfg.SSH.Approval)
}

// denyGate fails every request closed — used when shed-desktop delegation is
// selected but the desktop channel isn't enabled.
type denyGate struct{}

func (g *denyGate) Enabled() bool { return true }
func (g *denyGate) Approve(_, _, _ string) error {
	return fmt.Errorf("ssh.approval.method is shed-desktop but desktop.enabled is false")
}
func (g *denyGate) Method() string { return "shed-desktop" }
