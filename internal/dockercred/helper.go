// Package dockercred implements the guest-side Docker credential helper client.
// It translates Docker credential helper protocol operations into message bus
// requests to the host agent. This is used by the docker-credential-shed
// one-shot binary.
package dockercred

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/charliek/shed/sdk"

	"github.com/charliek/shed-extensions/internal/protocol"
)

// Helper translates Docker credential helper operations into message bus requests.
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
		return nil, fmt.Errorf("host error [%s]: %s", errResp.Code, errResp.Error)
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
