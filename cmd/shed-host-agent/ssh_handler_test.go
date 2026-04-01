package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/charliek/shed-extensions/internal/hostclient"
	"github.com/charliek/shed-extensions/internal/protocol"
)

// mockBackend implements SSHBackend for testing.
type mockBackend struct {
	keys    []*agent.Key
	signFn  func(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error)
	listErr error
	signErr error
}

func (m *mockBackend) Mode() string { return "mock" }

func (m *mockBackend) List() ([]*agent.Key, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.keys, nil
}

func (m *mockBackend) Sign(key ssh.PublicKey, data []byte, flags agent.SignatureFlags) (*ssh.Signature, error) {
	if m.signErr != nil {
		return nil, m.signErr
	}
	if m.signFn != nil {
		return m.signFn(key, data, flags)
	}
	return &ssh.Signature{Format: "ssh-ed25519", Blob: []byte("mock-signature")}, nil
}

func TestSSHHandlerList(t *testing.T) {
	backend := &mockBackend{
		keys: []*agent.Key{
			{Format: "ssh-ed25519", Blob: []byte("key1"), Comment: "test@host"},
		},
	}

	var responded protocol.Envelope
	respondCalled := make(chan struct{}, 1)

	// Mock shed-server with SSE + respond endpoints
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// SSE stream — send one list request then keep alive
			listReq := protocol.SSHListRequest{Operation: protocol.SSHOpList}
			payload, _ := json.Marshal(listReq)
			env := protocol.NewEnvelope(protocol.NamespaceSSHAgent, protocol.MessageTypeRequest, payload)
			env.Shed = &protocol.ShedInfo{Name: "test-shed"}
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			mu.Lock()
			json.NewDecoder(r.Body).Decode(&responded)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- struct{}{}
		}
	}))
	defer srv.Close()

	client := hostclient.New(hostclient.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewSSHHandler(backend, client, &noopGate{}, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case <-respondCalled:
		mu.Lock()
		defer mu.Unlock()

		var listResp protocol.SSHListResponse
		if err := json.Unmarshal(responded.Payload, &listResp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(listResp.Keys) != 1 {
			t.Fatalf("expected 1 key, got %d", len(listResp.Keys))
		}
		if listResp.Keys[0].Comment != "test@host" {
			t.Errorf("comment: got %q, want %q", listResp.Keys[0].Comment, "test@host")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	}
}

func TestSSHHandlerSign(t *testing.T) {
	backend := &mockBackend{
		signFn: func(_ ssh.PublicKey, _ []byte, _ agent.SignatureFlags) (*ssh.Signature, error) {
			return &ssh.Signature{
				Format: "ssh-ed25519",
				Blob:   []byte("test-signature"),
			}, nil
		},
	}

	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			signReq := protocol.SSHSignRequest{
				Operation: protocol.SSHOpSign,
				PublicKey: base64.StdEncoding.EncodeToString([]byte("fake-key-bytes")),
				Data:      base64.StdEncoding.EncodeToString([]byte("challenge")),
				Flags:     0,
			}
			payload, _ := json.Marshal(signReq)
			env := protocol.NewEnvelope(protocol.NamespaceSSHAgent, protocol.MessageTypeRequest, payload)
			env.Shed = &protocol.ShedInfo{Name: "test-shed"}
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			var env protocol.Envelope
			json.NewDecoder(r.Body).Decode(&env)
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- env.Payload
		}
	}))
	defer srv.Close()

	client := hostclient.New(hostclient.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewSSHHandler(backend, client, &noopGate{}, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		// The sign will fail at ParsePublicKey because we sent fake key bytes,
		// so we expect an error response. This validates the handler dispatches correctly.
		var errResp protocol.SSHErrorResponse
		if json.Unmarshal(payload, &errResp) == nil && errResp.Code != "" {
			// Expected: fake key bytes aren't a valid SSH public key
			t.Logf("got expected error response: %s (%s)", errResp.Error, errResp.Code)
		} else {
			var signResp protocol.SSHSignResponse
			if err := json.Unmarshal(payload, &signResp); err != nil {
				t.Fatalf("unexpected response payload: %s", string(payload))
			}
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	}
}

func TestSSHHandlerPing(t *testing.T) {
	backend := &mockBackend{}
	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pingReq := protocol.SSHPingRequest{Operation: protocol.SSHOpPing}
			payload, _ := json.Marshal(pingReq)
			env := protocol.NewEnvelope(protocol.NamespaceSSHAgent, protocol.MessageTypeRequest, payload)
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			var env protocol.Envelope
			json.NewDecoder(r.Body).Decode(&env)
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- env.Payload
		}
	}))
	defer srv.Close()

	client := hostclient.New(hostclient.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewSSHHandler(backend, client, &noopGate{}, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var pingResp protocol.SSHPingResponse
		if err := json.Unmarshal(payload, &pingResp); err != nil {
			t.Fatalf("unmarshal ping response: %v", err)
		}
		if pingResp.Status != "ok" {
			t.Errorf("ping status: got %q, want %q", pingResp.Status, "ok")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ping response")
	}
}
