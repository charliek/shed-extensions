package hostclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

func TestSubscribe(t *testing.T) {
	env := protocol.NewEnvelope("test-ns", protocol.MessageTypeRequest, json.RawMessage(`{"op":"test"}`))
	envJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/listeners/test-ns/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		fmt.Fprintf(w, "data: %s\n\n", envJSON)
		flusher.Flush()

		// Keep connection open until client disconnects
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := New(WithServerURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := client.Subscribe(ctx, "test-ns")

	select {
	case received := <-ch:
		if received == nil {
			t.Fatal("received nil envelope")
		}
		if received.ID != env.ID {
			t.Errorf("ID mismatch: got %q, want %q", received.ID, env.ID)
		}
		if received.Namespace != "test-ns" {
			t.Errorf("namespace mismatch: got %q, want %q", received.Namespace, "test-ns")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for envelope")
	}
}

func TestSubscribeMultipleEvents(t *testing.T) {
	envelopes := make([]*protocol.Envelope, 3)
	for i := range envelopes {
		envelopes[i] = protocol.NewEnvelope("test-ns", protocol.MessageTypeRequest, json.RawMessage(fmt.Sprintf(`{"index":%d}`, i)))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		for _, env := range envelopes {
			data, _ := json.Marshal(env)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		<-r.Context().Done()
	}))
	defer srv.Close()

	client := New(WithServerURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := client.Subscribe(ctx, "test-ns")

	for i, want := range envelopes {
		select {
		case got := <-ch:
			if got.ID != want.ID {
				t.Errorf("event %d: ID mismatch: got %q, want %q", i, got.ID, want.ID)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestRespond(t *testing.T) {
	env := protocol.NewResponse("req-123", "test-ns", json.RawMessage(`{"result":"ok"}`))
	env.Shed = &protocol.ShedInfo{Name: "my-shed"}

	var received protocol.Envelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/listeners/test-ns/respond" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := New(WithServerURL(srv.URL))

	err := client.Respond(context.Background(), "test-ns", env)
	if err != nil {
		t.Fatalf("Respond failed: %v", err)
	}
	if received.InReplyTo != "req-123" {
		t.Errorf("InReplyTo mismatch: got %q, want %q", received.InReplyTo, "req-123")
	}
	if received.Shed == nil || received.Shed.Name != "my-shed" {
		t.Error("shed info not preserved in response")
	}
}

func TestRespondError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"shed not connected"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	client := New(WithServerURL(srv.URL))
	env := protocol.NewResponse("req-123", "test-ns", json.RawMessage(`{}`))
	env.Shed = &protocol.ShedInfo{Name: "missing-shed"}

	err := client.Respond(context.Background(), "test-ns", env)
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}
