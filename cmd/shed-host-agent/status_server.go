package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LiveStatus is the snapshot served over the read-only status socket and shown
// by `shed-host-agent status --live`: the running daemon's own view of each
// watched server's per-namespace subscription state (the authoritative answer
// the env-probe `status` can't give — am I connected, or retrying, and why).
type LiveStatus struct {
	Version   string         `json:"version"`
	Pid       int            `json:"pid"`
	WrittenAt string         `json:"written_at"`
	Servers   []ServerHealth `json:"servers"`
}

// ServerHealth is one watched server's connection state.
type ServerHealth struct {
	Name       string            `json:"name"`
	URL        string            `json:"url"`
	Namespaces []NamespaceHealth `json:"namespaces"`
}

// NamespaceHealth is one namespace subscription's state on a server.
type NamespaceHealth struct {
	Namespace string `json:"namespace"`
	State     string `json:"state"` // connected | reconnecting | stopped
	LastError string `json:"last_error,omitempty"`
	Since     string `json:"since"` // RFC3339 — when the state began
}

// statusSocketPath is the read-only status UDS, alongside the desktop socket.
// Both the daemon (serving) and `status --live` (dialing) derive it identically.
// Falls back to the default socket dir when desktop.socket_path is empty, so it
// never resolves to a surprising CWD-relative path.
func statusSocketPath(cfg Config) string {
	base := cfg.Desktop.SocketPath
	if base == "" {
		base = DefaultConfig().Desktop.SocketPath
	}
	return filepath.Join(filepath.Dir(base), "host-agent-status.sock")
}

// buildLiveStatus snapshots the daemon's live health for the status socket.
func buildLiveStatus(sup *Supervisor, version string) LiveStatus {
	return LiveStatus{
		Version:   version,
		Pid:       os.Getpid(),
		WrittenAt: time.Now().UTC().Format(time.RFC3339),
		Servers:   sup.Health(),
	}
}

// serveStatusSocket serves a read-only UDS that, on each connection, writes the
// current LiveStatus JSON and closes. It's separate from the desktop approval
// socket (so a query never promotes itself as the approval consumer) and always
// available, so `status --live` can query the running daemon. `health` is called
// per connection to snapshot live state. Serves until ctx is cancelled.
func serveStatusSocket(ctx context.Context, path string, health func() LiveStatus, logger *slog.Logger) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		logger.Warn("status socket: could not create dir", "dir", filepath.Dir(path), "error", err)
	}
	// Owner-only dir (defense-in-depth even if it already existed), matching the
	// desktop socket's protection.
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		logger.Warn("status socket: could not restrict dir perms", "dir", filepath.Dir(path), "error", err)
	}
	if err := prepareSocketPath(path); err != nil {
		logger.Error("status socket: refusing to bind", "path", path, "error", err)
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		logger.Error("status socket: failed to listen", "path", path, "error", err)
		return
	}
	if err := os.Chmod(path, 0600); err != nil {
		logger.Warn("status socket: could not set perms to 0600", "path", path, "error", err)
	}
	logger.Info("status socket listening", "path", path)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed (shutdown) or fatal accept error
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_ = json.NewEncoder(c).Encode(health())
		}(conn)
	}
}
