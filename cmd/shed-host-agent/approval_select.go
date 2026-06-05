package main

import "fmt"

// gateFor builds the approval gate for one credential extension from its
// approval policy. Platform-neutral so the darwin Touch ID path and the !darwin
// stub stay behind their build tags (newApprovalGate). namespace/op identify the
// extension when delegating to shed-desktop. An empty/unknown policy fails closed
// (deny-all).
func gateFor(namespace, op string, approval ApprovalConfig, desktop *DesktopServer) ApprovalGate {
	switch approval.EffectivePolicy() {
	case PolicyApproveAll:
		return &noopGate{}
	case PolicyBiometrics, PolicyBiometricsOrPassword:
		// Native Touch ID (SSH only; config.Validate rejects it for AWS/Docker).
		return newApprovalGate(approval)
	case PolicyShedDesktop:
		if desktop == nil {
			// Misconfiguration: delegation requested but desktop.enabled is false.
			// Fail closed rather than silently fall back.
			return &denyGate{}
		}
		return &desktopGate{server: desktop, namespace: namespace, op: op}
	default: // PolicyDenyAll and anything unexpected
		return &denyAllGate{}
	}
}

// denyGate fails every request closed — used when shed-desktop delegation is
// selected but the desktop channel isn't enabled.
type denyGate struct{}

func (g *denyGate) Approve(_, _, _ string) (ApprovalOutcome, error) {
	return ApprovalOutcome{}, fmt.Errorf("approval.policy is shed-desktop but desktop.enabled is false")
}
func (g *denyGate) Method() string { return PolicyShedDesktop }
