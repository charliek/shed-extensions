//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework LocalAuthentication -framework Foundation
#include <LocalAuthentication/LocalAuthentication.h>
#include <dispatch/dispatch.h>

static int authenticate(const char *reason) {
    __block int result = 0;
    dispatch_semaphore_t sema = dispatch_semaphore_create(0);

    LAContext *context = [[LAContext alloc] init];
    NSError *error = nil;

    if (![context canEvaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics error:&error]) {
        return -1;
    }

    NSString *nsReason = [NSString stringWithUTF8String:reason];
    [context evaluatePolicy:LAPolicyDeviceOwnerAuthenticationWithBiometrics
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
	enabled    bool
	policy     string
	sessionTTL time.Duration

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
		sessionTTL:    ttl,
		shedApprovals: make(map[string]time.Time),
	}
}

func (g *touchIDGate) Enabled() bool { return g.enabled }

func (g *touchIDGate) Approve(shedName, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check cached approval based on policy
	now := time.Now()
	switch g.policy {
	case "per-session":
		if !g.lastApproval.IsZero() && now.Sub(g.lastApproval) < g.sessionTTL {
			return nil
		}
	case "per-shed":
		if t, ok := g.shedApprovals[shedName]; ok && now.Sub(t) < g.sessionTTL {
			return nil
		}
	case "per-request":
		// Always prompt
	}

	prompt := fmt.Sprintf("shed-extensions: %s (shed: %s)", reason, shedName)
	cPrompt := C.CString(prompt)
	defer C.free(unsafe.Pointer(cPrompt))
	result := C.authenticate(cPrompt)

	switch result {
	case 1:
		g.lastApproval = now
		g.shedApprovals[shedName] = now
		return nil
	case 0:
		return fmt.Errorf("touch ID authentication denied")
	default:
		return fmt.Errorf("touch ID not available on this device")
	}
}
