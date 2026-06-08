package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	sdk "github.com/charliek/shed/sdk"

	"github.com/charliek/shed-extensions/internal/protocol"
)

func TestDockerHandlerGet(t *testing.T) {
	backend := &mockDockerBackend{
		cred: &DockerCredential{
			ServerURL: "us-docker.pkg.dev",
			Username:  "_json_key",
			Secret:    "gcloud-token-123",
		},
	}

	var responded sdk.Envelope
	respondCalled := make(chan struct{}, 1)

	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.DockerGetRequest{Operation: protocol.DockerOpGet, ServerURL: "us-docker.pkg.dev"}
			payload, _ := json.Marshal(req)
			env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
			env.Shed = &sdk.ShedInfo{Name: "test-shed"}
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

	client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case <-respondCalled:
		mu.Lock()
		defer mu.Unlock()

		var resp protocol.DockerGetResponse
		if err := json.Unmarshal(responded.Payload, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if resp.ServerURL != "us-docker.pkg.dev" {
			t.Errorf("ServerURL: got %q, want %q", resp.ServerURL, "us-docker.pkg.dev")
		}
		if resp.Username != "_json_key" {
			t.Errorf("Username: got %q, want %q", resp.Username, "_json_key")
		}
		if resp.Secret != "gcloud-token-123" {
			t.Errorf("Secret: got %q, want %q", resp.Secret, "gcloud-token-123")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	}
}

func TestDockerHandlerGetError(t *testing.T) {
	backend := &mockDockerBackend{
		err: &dockerError{msg: "registry not allowed", code: protocol.DockerCodeNotAllowed},
	}

	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.DockerGetRequest{Operation: protocol.DockerOpGet, ServerURL: "blocked.io"}
			payload, _ := json.Marshal(req)
			env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
			env.Shed = &sdk.ShedInfo{Name: "test-shed"}
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			var env sdk.Envelope
			json.NewDecoder(r.Body).Decode(&env)
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- env.Payload
		}
	}))
	defer srv.Close()

	client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var errResp protocol.DockerErrorResponse
		if err := json.Unmarshal(payload, &errResp); err != nil {
			t.Fatalf("unmarshal error response: %v", err)
		}
		if errResp.Code != protocol.DockerCodeNotAllowed {
			t.Errorf("code: got %q, want %q", errResp.Code, protocol.DockerCodeNotAllowed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for error response")
	}
}

// TestDockerHandlerGetAuditResult pins how each get failure is recorded in the
// audit log: CREDENTIALS_NOT_FOUND for an allowed registry is a successful
// anonymous pull ("anonymous"), while an allowlist deny and genuine faults stay
// "error". This is what keeps a public-image pull from showing as a red error
// in the shed-desktop activity feed.
func TestDockerHandlerGetAuditResult(t *testing.T) {
	cases := []struct {
		name       string
		backendErr error
		wantResult string
	}{
		{
			name:       "credentials not found is audited as anonymous",
			backendErr: &dockerError{msg: "no credentials found", code: protocol.DockerCodeNotFound},
			wantResult: auditResultAnonymous,
		},
		{
			name:       "registry not allowed stays an error",
			backendErr: &dockerError{msg: "registry not allowed", code: protocol.DockerCodeNotAllowed},
			wantResult: auditResultError,
		},
		{
			name:       "helper failure stays an error",
			backendErr: &dockerError{msg: "boom", code: protocol.DockerCodeHelperFailed},
			wantResult: auditResultError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &mockDockerBackend{err: tc.backendErr}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					req := protocol.DockerGetRequest{Operation: protocol.DockerOpGet, ServerURL: "https://index.docker.io/v1/"}
					payload, _ := json.Marshal(req)
					env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
					env.Shed = &sdk.ShedInfo{Name: "test-shed"}
					data, _ := json.Marshal(env)

					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					flusher := w.(http.Flusher)
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
					<-r.Context().Done()

				case http.MethodPost:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer srv.Close()

			client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
			logger := slog.Default()
			audit := &AuditLogger{logger: logger}
			entries, unsub := audit.Subscribe(4)
			defer unsub()

			handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go handler.Run(ctx)

			select {
			case entry := <-entries:
				if entry.Operation != protocol.DockerOpGet {
					t.Fatalf("operation: got %q, want %q", entry.Operation, protocol.DockerOpGet)
				}
				if entry.Result != tc.wantResult {
					t.Errorf("result: got %q, want %q", entry.Result, tc.wantResult)
				}
				if entry.Detail != "https://index.docker.io/v1/" {
					t.Errorf("detail: got %q, want the registry URL", entry.Detail)
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for audit entry")
			}
		})
	}
}

func TestDockerHandlerPing(t *testing.T) {
	backend := &mockDockerBackend{}
	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.DockerPingRequest{Operation: protocol.DockerOpPing}
			payload, _ := json.Marshal(req)
			env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			var env sdk.Envelope
			json.NewDecoder(r.Body).Decode(&env)
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- env.Payload
		}
	}))
	defer srv.Close()

	client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var resp protocol.DockerPingResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			t.Fatalf("unmarshal ping response: %v", err)
		}
		if resp.Status != "ok" {
			t.Errorf("status: got %q, want %q", resp.Status, "ok")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ping response")
	}
}

func TestDockerHandlerStatus(t *testing.T) {
	backend := &mockDockerBackend{}
	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.DockerStatusRequest{Operation: protocol.DockerOpStatus}
			payload, _ := json.Marshal(req)
			env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
			env.Shed = &sdk.ShedInfo{Name: "test-shed"}
			data, _ := json.Marshal(env)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			<-r.Context().Done()

		case http.MethodPost:
			var env sdk.Envelope
			json.NewDecoder(r.Body).Decode(&env)
			w.WriteHeader(http.StatusNoContent)
			respondCalled <- env.Payload
		}
	}))
	defer srv.Close()

	client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var resp protocol.DockerStatusResponse
		if err := json.Unmarshal(payload, &resp); err != nil {
			t.Fatalf("unmarshal status response: %v", err)
		}
		if !resp.Connected {
			t.Error("expected connected=true")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for status response")
	}
}

func TestDockerHandlerList(t *testing.T) {
	backend := &mockDockerBackend{
		list: map[string]string{
			"gcr.io":  "user1",
			"ghcr.io": "user2",
		},
	}

	var responded sdk.Envelope
	respondCalled := make(chan struct{}, 1)

	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.DockerListRequest{Operation: protocol.DockerOpList}
			payload, _ := json.Marshal(req)
			env := sdk.NewEnvelope(protocol.NamespaceDockerCredentials, sdk.MessageTypeRequest, payload)
			env.Shed = &sdk.ShedInfo{Name: "test-shed"}
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

	client := sdk.NewHostClient(sdk.WithServerURL(srv.URL))
	logger := slog.Default()
	audit := &AuditLogger{logger: logger}

	handler := NewDockerHandler(backend, client, &noopGate{}, audit, "test-server", logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case <-respondCalled:
		mu.Lock()
		defer mu.Unlock()

		var resp protocol.DockerListResponse
		if err := json.Unmarshal(responded.Payload, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(resp.Registries) != 2 {
			t.Errorf("registry count: got %d, want 2", len(resp.Registries))
		}
		if resp.Registries["gcr.io"] != "user1" {
			t.Errorf("gcr.io username: got %q, want %q", resp.Registries["gcr.io"], "user1")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	}
}
