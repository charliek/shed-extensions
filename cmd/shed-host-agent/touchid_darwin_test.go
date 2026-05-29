//go:build darwin

package main

import "testing"

// TestNewApprovalGateResolvesPolicy verifies that newApprovalGate maps the
// configured method to the right LocalAuthentication policy. It must NOT call
// Approve — that would trigger a real macOS authentication prompt and block.
func TestNewApprovalGateResolvesPolicy(t *testing.T) {
	tests := []struct {
		name              string
		method            string
		wantAllowPassword bool
		wantMethod        string
	}{
		{"strict", "biometrics", false, "biometrics"},
		{"fallback", "biometrics-or-password", true, "biometrics-or-password"},
		{"empty defaults to fallback", "", true, "biometrics-or-password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := newApprovalGate(ApprovalConfig{Enabled: true, Method: tt.method})
			g, ok := gate.(*touchIDGate)
			if !ok {
				t.Fatalf("newApprovalGate returned %T, want *touchIDGate", gate)
			}
			if g.allowPassword != tt.wantAllowPassword {
				t.Errorf("allowPassword = %v, want %v", g.allowPassword, tt.wantAllowPassword)
			}
			if got := g.Method(); got != tt.wantMethod {
				t.Errorf("Method() = %q, want %q", got, tt.wantMethod)
			}
		})
	}
}

func TestNewApprovalGateDisabled(t *testing.T) {
	gate := newApprovalGate(ApprovalConfig{Enabled: false, Method: "biometrics"})
	if _, ok := gate.(*noopGate); !ok {
		t.Fatalf("newApprovalGate(disabled) returned %T, want *noopGate", gate)
	}
}
