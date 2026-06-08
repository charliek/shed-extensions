// Package dockercred implements the guest-side Docker credential helper client.
// It translates Docker credential helper protocol operations into message bus
// requests to the host agent. This is used by the docker-credential-shed
// one-shot binary.
package dockercred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/charliek/shed/sdk"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// ErrCredentialsNotFound is returned by Get when the host broker has no
// credential to serve for the requested registry — either none exists in the
// host's Docker config, or the registry is outside the configured allowlist.
// The docker-credential-shed binary translates this into Docker's standard
// "credentials not found" signal so the daemon falls back to an anonymous pull
// (which is what public images need) instead of aborting with a hard error.
var ErrCredentialsNotFound = errors.New("no credentials for registry")

type Helper struct {
	bus *sdk.BusClient
}

// Option configures a Helper.
type Option func(*Helper)

// WithPublishURL sets the shed-agent publish endpoint URL.
func WithPublishURL(url string) Option {
	return func(h *Helper) {
		h.bus.PublishURL = url
	}
}

// New creates a new Helper with the given options.
func New(opts ...Option) *Helper {
	h := &Helper{
		bus: sdk.NewBusClient(sdk.DefaultPublishURL, sdk.DefaultBusTimeout),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Get retrieves credentials for the given registry server URL.
func (h *Helper) Get(ctx context.Context, serverURL string) (*protocol.DockerGetResponse, error) {
	req := protocol.DockerGetRequest{
		Operation: protocol.DockerOpGet,
		ServerURL: serverURL,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	respPayload, err := h.bus.Publish(ctx, protocol.NamespaceDockerCredentials, payload)
	if err != nil {
		return nil, fmt.Errorf("credential request failed: %w", err)
	}

	// Check for error response
	var errResp protocol.DockerErrorResponse
	if json.Unmarshal(respPayload, &errResp) == nil && errResp.Code != "" {
		// "No credential to serve" — whether none exists (CREDENTIALS_NOT_FOUND)
		// or policy withholds it (REGISTRY_NOT_ALLOWED) — is wrapped as
		// ErrCredentialsNotFound so the caller can surface Docker's anonymous-pull
		// fallback. Genuine faults (HELPER_FAILED, INTERNAL_ERROR, READ_ONLY) stay
		// hard errors so they remain visible to the user.
		switch errResp.Code {
		case protocol.DockerCodeNotFound, protocol.DockerCodeNotAllowed:
			return nil, fmt.Errorf("host error [%s]: %s: %w", errResp.Code, errResp.Error, ErrCredentialsNotFound)
		default:
			return nil, fmt.Errorf("host error [%s]: %s", errResp.Code, errResp.Error)
		}
	}

	var resp protocol.DockerGetResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return &resp, nil
}

// List returns a map of registry server URLs to usernames for all allowed registries.
func (h *Helper) List(ctx context.Context) (map[string]string, error) {
	req := protocol.DockerListRequest{
		Operation: protocol.DockerOpList,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	respPayload, err := h.bus.Publish(ctx, protocol.NamespaceDockerCredentials, payload)
	if err != nil {
		return nil, fmt.Errorf("list request failed: %w", err)
	}

	// Check for error response
	var errResp protocol.DockerErrorResponse
	if json.Unmarshal(respPayload, &errResp) == nil && errResp.Code != "" {
		return nil, fmt.Errorf("host error [%s]: %s", errResp.Code, errResp.Error)
	}

	var resp protocol.DockerListResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	return resp.Registries, nil
}
