package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

const maxFrameBytes = 1 << 20 // 1 MiB per-line cap on inbound frames

// removeStaleSocket removes the path only if it exists AND is a Unix socket,
// so a misconfigured socket_path can never delete an unrelated file.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("path exists but is not a socket: %s", path)
	}
	return os.Remove(path)
}

var (
	errNoConsumer = errors.New("no shed-desktop connected")
	errTimeout    = errors.New("approval timed out")
)

// consumerConn is the single active shed-desktop connection.
type consumerConn struct {
	conn    net.Conn
	writeMu sync.Mutex
}

// consumerWriteTimeout bounds every frame write so a connected-but-not-reading
// app can't block a send forever (which would hang an approval past its budget
// or strand the ping/forward goroutines). A write past this deadline errors;
// the caller then denies / the connection is dropped — fail closed.
const consumerWriteTimeout = 5 * time.Second

func (c *consumerConn) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(consumerWriteTimeout))
	_, err = c.conn.Write(data)
	return err
}

// DesktopServer exposes the UDS for shed-desktop: the all-namespace
// audit/event stream plus the SSH approval request/response channel. Single
// active consumer, last-writer-wins; fail-closed (deny) when none connected.
type DesktopServer struct {
	cfg          DesktopConfig
	audit        *AuditLogger
	logger       *slog.Logger
	timeout      time.Duration
	agentVersion string

	mu       sync.Mutex
	consumer *consumerConn
	pending  map[string]pendingReq
	ring     []eventMsg
	ringMax  int
}

// pendingReq binds an in-flight approval to the consumer that owns it, so a
// superseded/old connection can't resolve a request it merely observed.
type pendingReq struct {
	ch    chan bool
	owner *consumerConn
}

// NewDesktopServer builds a server. SSH-only gating in v1.
func NewDesktopServer(cfg DesktopConfig, audit *AuditLogger, agentVersion string, logger *slog.Logger) *DesktopServer {
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	return &DesktopServer{
		cfg: cfg, audit: audit, logger: logger, timeout: timeout, agentVersion: agentVersion,
		pending: make(map[string]pendingReq),
		ringMax: 100,
	}
}

// Listen binds the socket and serves until ctx is cancelled.
func (s *DesktopServer) Listen(ctx context.Context) {
	dir := filepath.Dir(s.cfg.SocketPath)
	_ = os.MkdirAll(dir, 0700)
	// Owner-only parent dir is the real protection (it covers the brief window
	// between Listen and the socket Chmod); enforce it even if the dir existed.
	if err := os.Chmod(dir, 0700); err != nil {
		s.logger.Warn("desktop: could not restrict socket dir perms", "dir", dir, "error", err)
	}
	// Clear a stale socket from a prior run — but only if it's actually a
	// socket, so a misconfigured path can never delete an unrelated file.
	if err := removeStaleSocket(s.cfg.SocketPath); err != nil {
		s.logger.Error("desktop: refusing to bind", "path", s.cfg.SocketPath, "error", err)
		return
	}
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		s.logger.Error("desktop: failed to listen", "path", s.cfg.SocketPath, "error", err)
		return
	}
	if err := os.Chmod(s.cfg.SocketPath, 0600); err != nil {
		s.logger.Warn("desktop: could not set socket perms to 0600", "path", s.cfg.SocketPath, "error", err)
	}
	s.logger.Info("desktop: approval socket listening", "path", s.cfg.SocketPath)

	auditCh, unsub := s.audit.Subscribe(256)
	defer unsub()
	go s.forwardAudit(ctx, auditCh)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = removeStaleSocket(s.cfg.SocketPath)
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed (shutdown) or fatal accept error
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *DesktopServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	// Scanner with a hard max token size so a client can't force unbounded
	// memory growth with a giant line that never ends in '\n'.
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)

	// Require a hello within a short grace period.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !sc.Scan() {
		return
	}
	line := sc.Bytes()
	var probe envelopeType
	if json.Unmarshal(line, &probe) != nil || probe.Type != "hello" {
		return
	}
	var hello helloMsg
	_ = json.Unmarshal(line, &hello)
	_ = conn.SetReadDeadline(time.Time{})

	c := &consumerConn{conn: conn}
	// Send hello_ack BEFORE promoting, so RequestApproval can't route an
	// approval_request to this connection before the handshake completes.
	if err := c.send(helloAckMsg{
		V: desktopProtocolVersion, Type: "hello_ack", ID: newID(), Ts: nowRFC3339(),
		Agent:            agentInfo{Version: s.agentVersion, ApprovalMethod: "shed-desktop"},
		Namespaces:       []string{protocol.NamespaceSSHAgent, protocol.NamespaceAWSCredentials, protocol.NamespaceDockerCredentials},
		GateNamespaces:   []string{protocol.NamespaceSSHAgent},
		RequestTimeoutMS: int(s.timeout / time.Millisecond),
		Accepted:         true,
	}); err != nil {
		return
	}
	s.promote(c)
	defer s.demote(c)
	s.replay(c, hello.ReplayEvents)

	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = c.send(pingMsg{V: desktopProtocolVersion, Type: "ping", ID: newID(), Ts: nowRFC3339()})
			}
		}
	}()

	for sc.Scan() {
		line := sc.Bytes()
		var et envelopeType
		if json.Unmarshal(line, &et) != nil {
			continue
		}
		switch et.Type {
		case "approval_response":
			var resp approvalResponseMsg
			if json.Unmarshal(line, &resp) == nil {
				s.resolve(resp.RequestID, resp.Decision == "approve", c)
			}
		case "pong":
			// liveness only
		}
	}
}

// RequestApproval sends an approval request to the connected app and blocks on
// the reply within the timeout. Fail-closed: returns an error (→ deny) when no
// app is connected, on timeout, or on a transport error.
func (s *DesktopServer) RequestApproval(ctx context.Context, namespace, op, shed, detail string) (bool, error) {
	s.mu.Lock()
	consumer := s.consumer
	if consumer == nil {
		s.mu.Unlock()
		return false, errNoConsumer
	}
	id := newID()
	ch := make(chan bool, 1)
	s.pending[id] = pendingReq{ch: ch, owner: consumer}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, id); s.mu.Unlock() }()

	req := approvalRequestMsg{
		V: desktopProtocolVersion, Type: "approval_request", ID: id, Ts: nowRFC3339(),
		Namespace: namespace, Op: op, Shed: shed, Detail: detail,
		ExpiresAt: time.Now().Add(s.timeout).UTC().Format(time.RFC3339),
	}
	if err := consumer.send(req); err != nil {
		return false, err
	}
	select {
	case ok := <-ch:
		return ok, nil
	case <-time.After(s.timeout):
		return false, errTimeout
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (s *DesktopServer) promote(c *consumerConn) {
	s.mu.Lock()
	old := s.consumer
	s.consumer = c
	s.mu.Unlock()
	if old != nil && old != c {
		_ = old.send(helloAckMsg{
			V: desktopProtocolVersion, Type: "hello_ack", ID: newID(), Ts: nowRFC3339(),
			Accepted: false, Reason: "superseded",
		})
		_ = old.conn.Close()
	}
}

func (s *DesktopServer) demote(c *consumerConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumer == c {
		s.consumer = nil
	}
	// Fail any in-flight approvals owned by this (disconnecting) consumer,
	// whether it's still the active one or was already superseded — fail-closed.
	for id, p := range s.pending {
		if p.owner == c {
			select {
			case p.ch <- false:
			default:
			}
			delete(s.pending, id)
		}
	}
}

func (s *DesktopServer) resolve(requestID string, approved bool, from *consumerConn) {
	s.mu.Lock()
	p, ok := s.pending[requestID]
	// Only the consumer the request was sent to may resolve it; a superseded
	// connection that merely observed the id cannot.
	if ok && p.owner == from {
		delete(s.pending, requestID)
	} else {
		ok = false
	}
	s.mu.Unlock()
	if ok {
		select {
		case p.ch <- approved:
		default:
		}
	}
}

func (s *DesktopServer) forwardAudit(ctx context.Context, ch <-chan AuditEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			ev := eventMsg{
				V: desktopProtocolVersion, Type: "event", ID: newID(), Ts: entry.Timestamp,
				Kind: "audit", Shed: entry.Shed, Ns: entry.Namespace, Op: entry.Operation,
				Result: entry.Result, Detail: entry.Detail, Approval: entry.Approval,
			}
			s.mu.Lock()
			s.ring = append(s.ring, ev)
			if len(s.ring) > s.ringMax {
				s.ring = s.ring[len(s.ring)-s.ringMax:]
			}
			consumer := s.consumer
			s.mu.Unlock()
			if consumer != nil {
				_ = consumer.send(ev)
			}
		}
	}
}

func (s *DesktopServer) replay(c *consumerConn, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	start := 0
	if len(s.ring) > n {
		start = len(s.ring) - n
	}
	evs := append([]eventMsg(nil), s.ring[start:]...)
	s.mu.Unlock()
	for _, ev := range evs {
		_ = c.send(ev)
	}
}
