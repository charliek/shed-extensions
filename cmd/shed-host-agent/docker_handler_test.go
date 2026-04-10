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

	handler := NewDockerHandler(backend, client, audit, logger)

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
		err: &dockerError{msg: "registry not allowed", code: "REGISTRY_NOT_ALLOWED"},
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

	handler := NewDockerHandler(backend, client, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var errResp protocol.DockerErrorResponse
		if err := json.Unmarshal(payload, &errResp); err != nil {
			t.Fatalf("unmarshal error response: %v", err)
		}
		if errResp.Code != "REGISTRY_NOT_ALLOWED" {
			t.Errorf("code: got %q, want %q", errResp.Code, "REGISTRY_NOT_ALLOWED")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for error response")
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

	handler := NewDockerHandler(backend, client, audit, logger)

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

	handler := NewDockerHandler(backend, client, audit, logger)

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

	handler := NewDockerHandler(backend, client, audit, logger)

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
