//go:build !darwin

package main

// ApprovalGate stub for non-darwin builds — Touch ID is not available.

func newApprovalGate(_ ApprovalConfig) ApprovalGate {
	return &noopGate{}
}
