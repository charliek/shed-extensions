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

	"github.com/charliek/shed-extensions/internal/hostclient"
	"github.com/charliek/shed-extensions/internal/protocol"
)

func TestAWSHandlerGetCredentials(t *testing.T) {
	backend := &mockAWSBackend{
		creds: &AWSCachedCredentials{
			AccessKeyID:     "ASIAIOSFODNN7EXAMPLE",
			SecretAccessKey: "wJalrXUtnFEMI/K7MDENG",
			SessionToken:    "FwoGZXIvYXdzE...",
			Expiration:      time.Date(2026, 3, 31, 19, 0, 0, 0, time.UTC),
		},
	}

	var responded protocol.Envelope
	respondCalled := make(chan struct{}, 1)

	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.AWSCredentialsRequest{Operation: protocol.AWSOpGetCredentials}
			payload, _ := json.Marshal(req)
			env := protocol.NewEnvelope(protocol.NamespaceAWSCredentials, protocol.MessageTypeRequest, payload)
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

	handler := NewAWSHandler(backend, client, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case <-respondCalled:
		mu.Lock()
		defer mu.Unlock()

		var credsResp protocol.AWSCredentialsResponse
		if err := json.Unmarshal(responded.Payload, &credsResp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if credsResp.AccessKeyID != "ASIAIOSFODNN7EXAMPLE" {
			t.Errorf("AccessKeyID: got %q, want %q", credsResp.AccessKeyID, "ASIAIOSFODNN7EXAMPLE")
		}
		if credsResp.Expiration != "2026-03-31T19:00:00Z" {
			t.Errorf("Expiration: got %q, want %q", credsResp.Expiration, "2026-03-31T19:00:00Z")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for response")
	}
}

func TestAWSHandlerPing(t *testing.T) {
	backend := &mockAWSBackend{}
	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pingReq := protocol.AWSPingRequest{Operation: protocol.AWSOpPing}
			payload, _ := json.Marshal(pingReq)
			env := protocol.NewEnvelope(protocol.NamespaceAWSCredentials, protocol.MessageTypeRequest, payload)
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

	handler := NewAWSHandler(backend, client, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var pingResp protocol.AWSPingResponse
		if err := json.Unmarshal(payload, &pingResp); err != nil {
			t.Fatalf("unmarshal ping response: %v", err)
		}
		if pingResp.Status != "ok" {
			t.Errorf("status: got %q, want %q", pingResp.Status, "ok")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for ping response")
	}
}

func TestAWSHandlerError(t *testing.T) {
	backend := &mockAWSBackend{
		err: fmt.Errorf("sts:AssumeRole failed"),
	}

	respondCalled := make(chan json.RawMessage, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			req := protocol.AWSCredentialsRequest{Operation: protocol.AWSOpGetCredentials}
			payload, _ := json.Marshal(req)
			env := protocol.NewEnvelope(protocol.NamespaceAWSCredentials, protocol.MessageTypeRequest, payload)
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

	handler := NewAWSHandler(backend, client, audit, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go handler.Run(ctx)

	select {
	case payload := <-respondCalled:
		var errResp protocol.AWSErrorResponse
		if err := json.Unmarshal(payload, &errResp); err != nil {
			t.Fatalf("unmarshal error response: %v", err)
		}
		if errResp.Code != protocol.AWSCodeAssumeRoleFailed {
			t.Errorf("code: got %q, want %q", errResp.Code, protocol.AWSCodeAssumeRoleFailed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for error response")
	}
}
