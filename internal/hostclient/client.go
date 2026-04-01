// Package hostclient provides an SSE client for shed-server's plugin API.
// It subscribes to a namespace's message stream and sends responses back.
package hostclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

const (
	defaultServerURL    = "http://localhost:8080"
	maxReconnectBackoff = 30 * time.Second
	initialBackoff      = 1 * time.Second
)

// Client connects to shed-server's plugin API to receive and respond to messages.
type Client struct {
	serverURL  string
	httpClient *http.Client
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithServerURL sets the shed-server URL.
func WithServerURL(url string) Option {
	return func(c *Client) {
		c.serverURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithLogger sets a custom logger.
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

// New creates a new Client with the given options.
func New(opts ...Option) *Client {
	c := &Client{
		serverURL:  defaultServerURL,
		httpClient: &http.Client{},
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Subscribe connects to the SSE stream for the given namespace and returns a
// channel of envelopes. The channel is closed when the context is cancelled
// or the connection is permanently lost. Reconnects automatically on transient
// failures with exponential backoff.
func (c *Client) Subscribe(ctx context.Context, namespace string) <-chan *protocol.Envelope {
	ch := make(chan *protocol.Envelope, 32)
	go c.subscribeLoop(ctx, namespace, ch)
	return ch
}

func (c *Client) subscribeLoop(ctx context.Context, namespace string, ch chan<- *protocol.Envelope) {
	defer close(ch)

	backoff := initialBackoff
	for {
		err := c.streamMessages(ctx, namespace, ch)
		if ctx.Err() != nil {
			return
		}
		c.logger.Warn("SSE connection lost, reconnecting",
			"namespace", namespace,
			"error", err,
			"backoff", backoff,
		)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxReconnectBackoff)
	}
}

func (c *Client) streamMessages(ctx context.Context, namespace string, ch chan<- *protocol.Envelope) error {
	url := fmt.Sprintf("%s/api/plugins/listeners/%s/messages", c.serverURL, namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	c.logger.Info("SSE connected", "namespace", namespace)

	scanner := bufio.NewScanner(resp.Body)
	var dataBuf bytes.Buffer

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimSpace(data)
			dataBuf.WriteString(data)
			continue
		}

		// Empty line signals end of event
		if line == "" && dataBuf.Len() > 0 {
			var env protocol.Envelope
			if err := json.Unmarshal(dataBuf.Bytes(), &env); err != nil {
				c.logger.Warn("failed to parse SSE event", "error", err)
			} else {
				select {
				case ch <- &env:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			dataBuf.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stream: %w", err)
	}
	return errors.New("stream closed by server")
}

// Respond sends a response envelope back to shed-server for routing to the
// originating shed.
func (c *Client) Respond(ctx context.Context, namespace string, env *protocol.Envelope) error {
	url := fmt.Sprintf("%s/api/plugins/listeners/%s/respond", c.serverURL, namespace)

	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshaling response: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
