// Package rc implements the guest-side RC Session Convention v2 — the logic the
// shed-ext-rc binary uses to create, classify, list, and tear down remote-control
// tmux sessions (named rc-<slug>) inside a shed. It is the canonical implementation
// of docs/reference/rc-session-convention.md (owned by shed-remote-agent); tools
// invoke shed-ext-rc over SSH and consume the neutral JSON DTO it prints.
//
// This file holds the pure, side-effect-free core (slug/command/env/classification);
// tmux execution lives in tmux.go, trust pre-seeding in trust.go, and the high-level
// subcommand orchestration in ops.go.
package rc

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Kind is the RC session kind. v2 renamed the v1 values (agent/repl); there is no
// aliasing — an unrecognized value reads as legacy/unmanaged.
type Kind string

const (
	// KindClaudeBroker runs `claude remote-control` (the multiplexer/broker).
	KindClaudeBroker Kind = "claude-broker"
	// KindClaudeRC runs an interactive `claude` REPL with `/rc`.
	KindClaudeRC Kind = "claude-rc"
	// KindShell runs a plain login bash.
	KindShell Kind = "shell"
)

// DefaultKind is the create-time default (distinct from the legacy/unmanaged
// fallback, which is KindClaudeBroker).
const DefaultKind = KindClaudeRC

// IsValidKind reports whether k is a recognized v2 kind.
func IsValidKind(k Kind) bool {
	return k == KindClaudeBroker || k == KindClaudeRC || k == KindShell
}

// IsClaudeKind reports whether the kind runs claude (and so gates on workspace
// trust). Everything except shell.
func IsClaudeKind(k Kind) bool { return k != KindShell }

// AcceptsTypedInput reports whether the kind's pane accepts a typed kickoff line
// (claude-rc → a prompt, shell → a command). claude-broker's input is the remote
// URL, not the pane.
func AcceptsTypedInput(k Kind) bool { return k != KindClaudeBroker }

// State is the pane-derived liveness of a session. Never stored — always classified
// from a capture-pane on demand.
type State string

const (
	StateStarting     State = "starting"
	StateReady        State = "ready"
	StateReconnecting State = "reconnecting"
	StateNeedsTrust   State = "needs-trust"
	StateNeedsAuth    State = "needs-auth"
	StateDead         State = "dead"
)

const (
	// TmuxPrefix is the reserved tmux session-name prefix for RC sessions.
	TmuxPrefix = "rc-"
	// SchemaVersion is stamped into SHED_RC_V at create.
	SchemaVersion = 2
	// MinManagedVersion is the lowest SHED_RC_V a reader still understands.
	// Deliberately decoupled from SchemaVersion so a future additive bump does
	// not force-drop older managed sessions.
	MinManagedVersion = 2
	// ToolName is this binary's stable provenance token (no '/').
	ToolName = "shed-ext-rc"
)

// SHED_RC_* env keys — the on-session metadata store (RC Session Convention v2).
const (
	envV           = "SHED_RC_V"
	envID          = "SHED_RC_ID"
	envDisplayName = "SHED_RC_DISPLAY_NAME"
	envKind        = "SHED_RC_KIND"
	envWorkdir     = "SHED_RC_WORKDIR"
	envCreatedBy   = "SHED_RC_CREATED_BY"
	envCreatedAt   = "SHED_RC_CREATED_AT"
	envTarget      = "SHED_RC_TARGET"
	envPrefix      = "SHED_RC_"
)

// Session is the neutral, target-agnostic DTO the binary prints. Optional fields are
// omitted (absent, not null) when unknown — `managed` is always present. Mirrors
// shed-remote-agent's rcSessionDtoSchema; a golden fixture cross-checks both.
type Session struct {
	Slug        string `json:"slug"`
	TmuxSession string `json:"tmux_session"`
	Kind        Kind   `json:"kind"`
	State       State  `json:"state"`
	Managed     bool   `json:"managed"`
	DisplayName string `json:"display_name,omitempty"`
	Workdir     string `json:"workdir,omitempty"`
	URL         string `json:"url,omitempty"`
	ID          string `json:"id,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	TargetLabel string `json:"target_label,omitempty"`
}

// ListResponse is the `list` subcommand's stdout shape.
type ListResponse struct {
	RCSessions []Session `json:"rc_sessions"`
}

// TmuxName returns the tmux session name for a slug.
func TmuxName(slug string) string { return TmuxPrefix + slug }

const slugAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GenSlug returns a 6-char slug from the confusable-free alphabet (no 0/o, 1/l/i)
// so it survives being read from a QR or typed URL.
func GenSlug() (string, error) {
	var b strings.Builder
	for range 6 {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(slugAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generating slug: %w", err)
		}
		b.WriteByte(slugAlphabet[n.Int64()])
	}
	return b.String(), nil
}

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidCallerSlug reports whether a caller-supplied slug matches the grammar.
func ValidCallerSlug(slug string) bool { return slugRe.MatchString(slug) }

// HasControlChars reports whether s contains a control character (incl. newline,
// CR, tab). SHED_RC_* values and typed lines must be single-line.
func HasControlChars(s string) bool {
	for _, r := range s {
		if r <= 0x1f || r == 0x7f {
			return true
		}
	}
	return false
}

// shellQuote wraps s in single quotes, escaping embedded single quotes with the
// POSIX `'\”` trick, so it is a single safe shell token.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// InnerCommand builds the command the tmux session runs for a kind. interactiveShell
// wraps the claude kinds in `bash -ic` so a login rc-file loads PATH (nvm/asdf)
// before claude is exec'd (native machines); sheds bake claude into the system path.
func InnerCommand(kind Kind, displayName string, interactiveShell bool) string {
	var cmd string
	switch kind {
	case KindClaudeBroker:
		cmd = "claude remote-control --name " + shellQuote(displayName) + " --spawn same-dir"
	case KindClaudeRC:
		cmd = "claude --name " + shellQuote(displayName) + " /rc"
	case KindShell:
		return "bash -l"
	default:
		return "bash -l"
	}
	if interactiveShell {
		return "bash -ic " + shellQuote(cmd)
	}
	return cmd
}

var (
	brokerURLRe = regexp.MustCompile(`https?://claude\.ai/code\?environment=env_[A-Za-z0-9_-]+`)
	replURLRe   = regexp.MustCompile(`https?://claude\.ai/code/session_[A-Za-z0-9_-]+`)

	notTrustedRe   = regexp.MustCompile(`(?i)Workspace not trusted`)
	safetyCheckRe  = regexp.MustCompile(`(?i)Quick safety check`)
	trustFolderRe  = regexp.MustCompile(`(?i)Yes,\s*I trust this folder`)
	needsAuthRe    = regexp.MustCompile(`(?i)requires a claude\.ai subscription|not logged in|claude auth login`)
	reconnectingRe = regexp.MustCompile(`\bReconnecting\b`)
	connectedRe    = regexp.MustCompile(`\bConnected\b`)
	rcConnectingRe = regexp.MustCompile(`(?i)Remote Control connecting`)
	rcActiveRe     = regexp.MustCompile(`(?i)Remote Control active`)
)

func extractURL(kind Kind, pane string) string {
	switch kind {
	case KindClaudeBroker:
		return brokerURLRe.FindString(pane)
	case KindClaudeRC:
		return replURLRe.FindString(pane)
	default:
		return ""
	}
}

// IsTrustPrompt reports whether the pane is showing claude's first-run
// workspace-trust prompt (used by accept-trust to verify before sending Enter).
func IsTrustPrompt(pane string) bool {
	return notTrustedRe.MatchString(pane) || safetyCheckRe.MatchString(pane) ||
		trustFolderRe.MatchString(pane)
}

// ClassifyPane derives (state, url) from a captured pane for a kind. Mirrors
// shed-remote-agent's classifyPane.
func ClassifyPane(kind Kind, pane string) (State, string) {
	if kind != KindShell {
		if IsTrustPrompt(pane) {
			return StateNeedsTrust, extractURL(kind, pane)
		}
		if needsAuthRe.MatchString(pane) {
			return StateNeedsAuth, extractURL(kind, pane)
		}
	}

	switch kind {
	case KindClaudeBroker:
		url := extractURL(KindClaudeBroker, pane)
		if reconnectingRe.MatchString(pane) {
			return StateReconnecting, url
		}
		if connectedRe.MatchString(pane) && url != "" {
			return StateReady, url
		}
		if url != "" {
			return StateReady, url
		}
		return StateStarting, ""
	case KindClaudeRC:
		url := extractURL(KindClaudeRC, pane)
		if rcConnectingRe.MatchString(pane) && url == "" {
			return StateStarting, ""
		}
		if rcActiveRe.MatchString(pane) && url != "" {
			return StateReady, url
		}
		if url != "" {
			return StateReady, url
		}
		return StateStarting, ""
	default: // shell
		if strings.TrimSpace(pane) != "" {
			return StateReady, ""
		}
		return StateStarting, ""
	}
}

var canonicalIntRe = regexp.MustCompile(`^\d+$`)

// isManagedVersion reports whether a raw SHED_RC_V value denotes a managed session:
// a canonical positive integer >= MinManagedVersion. A v1 (or malformed) value is
// legacy/unmanaged.
func isManagedVersion(raw string) bool {
	if !canonicalIntRe.MatchString(raw) {
		return false
	}
	n, err := strconv.Atoi(raw)
	return err == nil && n >= MinManagedVersion
}

// parseKind maps a managed session's SHED_RC_KIND to a Kind, defaulting an
// unrecognized value to claude-broker (the renamed analog of the pre-convention
// default). Distinct from DefaultKind (the create-time default).
func parseKind(raw string) Kind {
	k := Kind(raw)
	if IsValidKind(k) {
		return k
	}
	return KindClaudeBroker
}
