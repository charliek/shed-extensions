//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework LocalAuthentication -framework Foundation
#include <LocalAuthentication/LocalAuthentication.h>
#include <dispatch/dispatch.h>

static int authenticate(const char *reason, int allowPassword) {
    __block int result = 0;
    dispatch_semaphore_t sema = dispatch_semaphore_create(0);

    LAContext *context = [[LAContext alloc] init];
    NSError *error = nil;

    LAPolicy policy = allowPassword
        ? LAPolicyDeviceOwnerAuthentication
        : LAPolicyDeviceOwnerAuthenticationWithBiometrics;

    if (![context canEvaluatePolicy:policy error:&error]) {
        return -1;
    }

    NSString *nsReason = [NSString stringWithUTF8String:reason];
    [context evaluatePolicy:policy
            localizedReason:nsReason
                      reply:^(BOOL success, NSError *authError) {
        result = success ? 1 : 0;
        dispatch_semaphore_signal(sema);
    }];

    dispatch_semaphore_wait(sema, DISPATCH_TIME_FOREVER);
    return result;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// touchIDGate implements ApprovalGate using macOS Touch ID.
type touchIDGate struct {
	enabled       bool
	policy        string
	allowPassword bool
	sessionTTL    time.Duration

	mu            sync.Mutex
	lastApproval  time.Time
	shedApprovals map[string]time.Time
}

func newApprovalGate(cfg ApprovalConfig) ApprovalGate {
	if !cfg.Enabled {
		return &noopGate{}
	}

	ttl, err := time.ParseDuration(cfg.SessionTTL)
	if err != nil {
		ttl = 4 * time.Hour
	}

	return &touchIDGate{
		enabled:       true,
		policy:        cfg.Policy,
		allowPassword: resolveAllowPassword(cfg.Method),
		sessionTTL:    ttl,
		shedApprovals: make(map[string]time.Time),
	}
}

func (g *touchIDGate) Enabled() bool { return g.enabled }

// Method returns the configured approval method for audit logging.
func (g *touchIDGate) Method() string {
	if g.allowPassword {
		return "biometrics-or-password"
	}
	return "biometrics"
}

func (g *touchIDGate) Approve(server, shedName, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Per-shed approvals are keyed by server/shed so identical shed names on
	// different servers do not share an approval. per-session stays global
	// (one approval covers the whole agent for the TTL).
	shedKey := serverShedKey(server, shedName)

	// Check cached approval based on policy
	now := time.Now()
	switch g.policy {
	case "per-session":
		if !g.lastApproval.IsZero() && now.Sub(g.lastApproval) < g.sessionTTL {
			return nil
		}
	case "per-shed":
		if t, ok := g.shedApprovals[shedKey]; ok && now.Sub(t) < g.sessionTTL {
			return nil
		}
	case "per-request":
		// Always prompt
	}

	prompt := fmt.Sprintf("shed-extensions: %s (server: %s, shed: %s)", reason, server, shedName)
	cPrompt := C.CString(prompt)
	defer C.free(unsafe.Pointer(cPrompt))

	var allowPassword C.int
	if g.allowPassword {
		allowPassword = 1
	}
	result := C.authenticate(cPrompt, allowPassword)

	switch result {
	case 1:
		g.lastApproval = now
		g.shedApprovals[shedKey] = now
		return nil
	case 0:
		return fmt.Errorf("touch ID authentication denied")
	default:
		return fmt.Errorf("touch ID not available on this device")
	}
}
