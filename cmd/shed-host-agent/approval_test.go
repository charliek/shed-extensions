package main

import "testing"

func TestNoopGate(t *testing.T) {
	g := &noopGate{}
	if g.Enabled() {
		t.Error("noopGate.Enabled() = true, want false")
	}
	if err := g.Approve("srv", "shed", "reason"); err != nil {
		t.Errorf("noopGate.Approve() = %v, want nil", err)
	}
	if got := g.Method(); got != "none" {
		t.Errorf("noopGate.Method() = %q, want %q", got, "none")
	}
}
