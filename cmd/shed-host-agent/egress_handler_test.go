package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestEgressAuditEntry(t *testing.T) {
	d := egressDecision{
		Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Shed: "web", Host: "evil.com", Port: 443, ResolvedIP: "1.2.3.4",
		Protocol: "https", Verdict: "deny", Reason: "default-deny",
	}
	e := egressAuditEntry("srv", d)
	if e.Server != "srv" || e.Shed != "web" || e.Namespace != "egress" {
		t.Errorf("server/shed/ns = %q/%q/%q", e.Server, e.Shed, e.Namespace)
	}
	if e.Operation != "https" || e.Result != "deny" || e.Reason != "default-deny" {
		t.Errorf("op/result/reason = %q/%q/%q", e.Operation, e.Result, e.Reason)
	}
	if e.Detail != "evil.com:443 (1.2.3.4)" {
		t.Errorf("detail = %q, want evil.com:443 (1.2.3.4)", e.Detail)
	}
	if e.Timestamp != "2020-01-01T00:00:00Z" {
		t.Errorf("timestamp = %q", e.Timestamp)
	}
}

func TestEgressAuditEntry_NoResolvedIP(t *testing.T) {
	e := egressAuditEntry("srv", egressDecision{Shed: "web", Host: "x.com", Port: 80, Protocol: "http", Verdict: "allow"})
	if e.Detail != "x.com:80" {
		t.Errorf("detail = %q, want x.com:80 (no resolved IP)", e.Detail)
	}
}

func TestEgressSubscriber_Stream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		// Emit decisions repeatedly so the test can't lose a race with setup.
		for {
			select {
			case <-r.Context().Done():
				return
			default:
				fmt.Fprint(w, "data: {\"ts\":\"2020-01-01T00:00:00Z\",\"shed\":\"web\",\"host\":\"evil.com\",\"port\":443,\"protocol\":\"https\",\"verdict\":\"deny\",\"reason\":\"default-deny\"}\n\n")
				if fl != nil {
					fl.Flush()
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}))
	defer ts.Close()

	audit := NewAuditLogger(LogConfig{Enabled: false}, discardLogger())
	ch, unsub := audit.Subscribe(8)
	defer unsub()

	sub := NewEgressSubscriber(ServerTarget{Name: "srv", URL: ts.URL, Token: "tok"}, audit, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sub.Run(ctx)

	select {
	case e := <-ch:
		if e.Namespace != "egress" || e.Shed != "web" || e.Result != "deny" {
			t.Errorf("audit entry = %+v", e)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no egress audit entry received over the stream")
	}
}

func TestEgressHTTPClient_PinOnPlainURLFailsClosed(t *testing.T) {
	c := egressHTTPClient("http://localhost:8080", "sha256:deadbeef")
	req, _ := http.NewRequest(http.MethodGet, "http://localhost:8080/api/egress/stream", nil)
	if _, err := c.Transport.RoundTrip(req); err == nil {
		t.Error("expected fail-closed error when a TLS pin is set on a non-https URL")
	}
}
