// Package awsproxy implements the guest-side AWS container credential endpoint.
// It serves the HTTP format the AWS SDK expects and translates requests into
// message bus calls to the host agent. The proxy is a passthrough — it does
// not cache credentials.
package awsproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

const (
	defaultPublishURL = "http://127.0.0.1:498/v1/publish"
	requestTimeout    = 3 * time.Second
)

// publishRequest is the body sent to the shed-agent /v1/publish endpoint.
type publishRequest struct {
	Namespace string          `json:"namespace"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// awsSDKResponse is the exact JSON format the AWS SDK expects from a
// container credential endpoint. Field names must match exactly.
type awsSDKResponse struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
}

// awsSDKErrorResponse is returned on errors with an actionable message.
type awsSDKErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// Proxy handles AWS credential requests from the SDK and translates them
// into message bus requests.
type Proxy struct {
	publishURL string
	httpClient *http.Client
	logger     *slog.Logger
}

// Option configures a Proxy.
type Option func(*Proxy)

// WithPublishURL sets the shed-agent publish endpoint URL.
func WithPublishURL(url string) Option {
	return func(p *Proxy) {
		p.publishURL = url
	}
}

// WithLogger sets a custom logger.
func WithLogger(logger *slog.Logger) Option {
	return func(p *Proxy) {
		p.logger = logger
	}
}

// New creates a new Proxy with the given options.
func New(opts ...Option) *Proxy {
	p := &Proxy{
		publishURL: defaultPublishURL,
		httpClient: &http.Client{Timeout: requestTimeout},
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// HandleCredentials is the HTTP handler for GET /credentials.
func (p *Proxy) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req := protocol.AWSCredentialsRequest{Operation: protocol.AWSOpGetCredentials}
	payload, err := json.Marshal(req)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "failed to marshal request", "")
		return
	}

	respPayload, err := p.publish(r.Context(), payload)
	if err != nil {
		p.logger.Error("credential request failed", "error", err)
		p.writeError(w, http.StatusServiceUnavailable,
			"credential request timed out",
			"shed-host-agent not reachable. Is it running on your Mac?",
		)
		return
	}

	// Check for error response from host
	var errResp protocol.AWSErrorResponse
	if json.Unmarshal(respPayload, &errResp) == nil && errResp.Code != "" {
		p.logger.Error("host returned error", "error", errResp.Error, "code", errResp.Code)
		p.writeError(w, http.StatusServiceUnavailable, errResp.Error, "")
		return
	}

	var creds protocol.AWSCredentialsResponse
	if err := json.Unmarshal(respPayload, &creds); err != nil {
		p.logger.Error("failed to parse credentials response", "error", err)
		p.writeError(w, http.StatusBadGateway, "invalid response from host agent", "")
		return
	}

	// Translate to the AWS SDK expected format (PascalCase fields)
	sdkResp := awsSDKResponse{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		Token:           creds.SessionToken,
		Expiration:      creds.Expiration,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sdkResp); err != nil {
		p.logger.Error("failed to write response", "error", err)
	}
}

// publish sends a request to the shed-agent publish endpoint and returns the
// response envelope's payload.
func (p *Proxy) publish(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	req := publishRequest{
		Namespace: protocol.NamespaceAWSCredentials,
		Type:      string(protocol.MessageTypeRequest),
		Payload:   payload,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling publish request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.publishURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("publishing to bus: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("publish failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var env protocol.Envelope
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("parsing response envelope: %w", err)
	}

	return env.Payload, nil
}

// Ping sends a health check ping and returns nil if the host agent responds.
func (p *Proxy) Ping(timeout time.Duration) error {
	req := protocol.AWSPingRequest{Operation: protocol.AWSOpPing}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling ping: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_, err = p.publish(ctx, payload)
	return err
}

func (p *Proxy) writeError(w http.ResponseWriter, status int, msg, hint string) {
	resp := awsSDKErrorResponse{
		Error:   msg,
		Message: "shed-host-agent not reachable. Is it running on your Mac?",
		Hint:    hint,
	}
	if hint == "" {
		resp.Message = msg
		resp.Hint = "Start it with: shed-host-agent --config ~/.config/shed/extensions.yaml"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		p.logger.Error("failed to write error response", "error", err)
	}
}
