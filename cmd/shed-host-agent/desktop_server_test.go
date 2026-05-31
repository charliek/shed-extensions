package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func startTestServer(t *testing.T, timeoutMS int) (*DesktopServer, *AuditLogger, context.CancelFunc, string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "agent.sock")
	audit := NewAuditLogger(LogConfig{Enabled: false}, testLogger())
	cfg := DesktopConfig{Enabled: true, SocketPath: sock, TimeoutMS: timeoutMS}
	s := NewDesktopServer(cfg, audit, "test", testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go s.Listen(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server socket never appeared")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return s, audit, cancel, sock
}

// dialHello connects, sends hello, consumes the hello_ack, then waits until
// the server has promoted this connection to the active consumer. The wait
// matters because the server now sends hello_ack BEFORE promoting, so a test
// that drives an approval the instant dialHello returns could otherwise race
// ahead of promotion.
func dialHello(t *testing.T, s *DesktopServer, sock string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte(`{"type":"hello","client":{"name":"t","version":"1","pid":1},"capabilities":[],"replay_events":0}` + "\n")); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	r := bufio.NewReader(conn)
	frame := readType(t, conn, r, "hello_ack")
	if accepted, _ := frame["accepted"].(bool); !accepted {
		t.Fatalf("hello_ack not accepted: %v", frame)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !s.hasConsumer() {
		if time.Now().After(deadline) {
			t.Fatal("server never promoted the consumer")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return conn, r
}

// readType reads frames until one with the wanted type (skipping ping etc).
// A read deadline keeps a missing frame from hanging the whole test run.
func readType(t *testing.T, conn net.Conn, r *bufio.Reader, want string) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read %s: %v", want, err)
		}
		var m map[string]any
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		if m["type"] == want {
			return m
		}
	}
}

func TestDesktopNoConsumerFailsClosed(t *testing.T) {
	s, _, cancel, _ := startTestServer(t, 1000)
	defer cancel()
	g := &desktopGate{server: s}
	if err := g.Approve("stbot", "ssh-ed25519"); err == nil {
		t.Fatal("expected deny (error) with no consumer connected")
	}
}

func TestDesktopApprove(t *testing.T) {
	s, _, cancel, sock := startTestServer(t, 5000)
	defer cancel()
	conn, r := dialHello(t, s, sock)
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		g := &desktopGate{server: s}
		done <- g.Approve("stbot", "ssh-ed25519")
	}()

	req := readType(t, conn, r, "approval_request")
	if req["namespace"] != "ssh-agent" || req["shed"] != "stbot" {
		t.Fatalf("unexpected request: %v", req)
	}
	respond(t, conn, req["id"].(string), "approve")
	if err := <-done; err != nil {
		t.Fatalf("approve should succeed, got %v", err)
	}
}

func TestDesktopDeny(t *testing.T) {
	s, _, cancel, sock := startTestServer(t, 5000)
	defer cancel()
	conn, r := dialHello(t, s, sock)
	defer conn.Close()

	done := make(chan error, 1)
	go func() { g := &desktopGate{server: s}; done <- g.Approve("stbot", "x") }()
	req := readType(t, conn, r, "approval_request")
	respond(t, conn, req["id"].(string), "deny")
	if err := <-done; err == nil {
		t.Fatal("deny should return an error")
	}
}

func TestDesktopTimeoutFailsClosed(t *testing.T) {
	s, _, cancel, sock := startTestServer(t, 150)
	defer cancel()
	conn, r := dialHello(t, s, sock)
	defer conn.Close()

	done := make(chan error, 1)
	go func() { g := &desktopGate{server: s}; done <- g.Approve("stbot", "x") }()
	readType(t, conn, r, "approval_request") // received, but we never respond
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("timeout should deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Approve did not time out")
	}
}

func TestDesktopAuditFanoutAllNamespaces(t *testing.T) {
	s, audit, cancel, sock := startTestServer(t, 1000)
	defer cancel()
	conn, r := dialHello(t, s, sock)
	defer conn.Close()

	// An aws event (a namespace that does NOT gate) must still reach the app.
	audit.Log("roost-dev", "aws-credentials", "get_credentials", "ok", "role/dev", "none")
	ev := readType(t, conn, r, "event")
	if ev["ns"] != "aws-credentials" || ev["shed"] != "roost-dev" || ev["result"] != "ok" {
		t.Fatalf("unexpected event: %v", ev)
	}
}

func TestDesktopLastWriterWins(t *testing.T) {
	s, _, cancel, sock := startTestServer(t, 5000)
	defer cancel()
	c1, r1 := dialHello(t, s, sock)
	defer c1.Close()
	// Second consumer supersedes the first.
	c2, r2 := dialHello(t, s, sock)
	defer c2.Close()
	// c1 must receive a superseded hello_ack (accepted:false).
	ack := readType(t, c1, r1, "hello_ack")
	if accepted, _ := ack["accepted"].(bool); accepted {
		t.Fatalf("first consumer should be superseded, got ack=%v", ack)
	}

	done := make(chan error, 1)
	go func() { g := &desktopGate{server: s}; done <- g.Approve("stbot", "x") }()
	req := readType(t, c2, r2, "approval_request") // goes to the active (second) consumer
	respond(t, c2, req["id"].(string), "approve")
	if err := <-done; err != nil {
		t.Fatalf("active consumer approve should succeed, got %v", err)
	}
}

func respond(t *testing.T, conn net.Conn, requestID, decision string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{"type": "approval_response", "request_id": requestID, "decision": decision, "decided_by": "user"})
	if _, err := conn.Write(append(msg, '\n')); err != nil {
		t.Fatalf("respond: %v", err)
	}
}

func TestDesktopConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Desktop.Enabled {
		t.Error("desktop should be disabled by default")
	}
	if cfg.Desktop.TimeoutMS != 25000 {
		t.Errorf("default timeout = %d, want 25000", cfg.Desktop.TimeoutMS)
	}
	if cfg.Desktop.SocketPath == "" {
		t.Error("default socket path should be set")
	}
}

func TestSelectApprovalGateMisconfigDenies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SSH.Approval.Method = "shed-desktop"
	// desktop.enabled false → desktop server nil → fail-closed deny gate.
	g := selectApprovalGate(cfg, nil)
	if err := g.Approve("stbot", "x"); err == nil {
		t.Fatal("misconfigured shed-desktop method should deny")
	}
}
