package busclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

func TestPublish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req publishRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Namespace != "test-ns" {
			t.Errorf("namespace: got %q, want %q", req.Namespace, "test-ns")
		}

		env := protocol.NewResponse("mock", "test-ns", json.RawMessage(`{"result":"ok"}`))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(env)
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	payload := json.RawMessage(`{"operation":"test"}`)

	resp, err := c.Publish(context.Background(), "test-ns", payload)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result["result"] != "ok" {
		t.Errorf("result: got %q, want %q", result["result"], "ok")
	}
}

func TestPublishTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	c := New(srv.URL, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Publish(ctx, "test-ns", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestPublishServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)

	_, err := c.Publish(context.Background(), "test-ns", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := protocol.NewResponse("mock", "test-ns", json.RawMessage(`{"status":"ok"}`))
		json.NewEncoder(w).Encode(env)
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	err := c.Ping(context.Background(), "test-ns", 2*time.Second)
	if err != nil {
		t.Fatalf("Ping failed: %v", err)
	}
}
