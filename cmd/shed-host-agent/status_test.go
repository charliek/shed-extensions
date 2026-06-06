package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
)

func TestProbeListeners(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/plugins/listeners" {
			http.NotFound(w, r)
			return
		}
		// Returned out of order to confirm probeListeners sorts.
		_, _ = w.Write([]byte(`{"listeners":[{"namespace":"ssh-agent","created_at":"x"},{"namespace":"docker-credentials","created_at":"y"}]}`))
	}))
	defer srv.Close()

	ns, err := probeListeners(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("probeListeners: %v", err)
	}
	if got := strings.Join(ns, ","); got != "docker-credentials,ssh-agent" {
		t.Fatalf("namespaces = %q, want sorted docker-credentials,ssh-agent", got)
	}
}

func TestProbeListenersErrors(t *testing.T) {
	// Non-200 is an error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if _, err := probeListeners(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Fatal("expected an error for HTTP 503")
	}

	// Malformed JSON is an error.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer junk.Close()
	if _, err := probeListeners(context.Background(), junk.Client(), junk.URL); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestDesktopSocketState(t *testing.T) {
	dir := t.TempDir()

	// Disabled → reported as disabled regardless of the path.
	if st := desktopSocketState(DesktopConfig{Enabled: false}); st.State != "disabled" {
		t.Fatalf("disabled state = %q", st.State)
	}

	// Enabled but the socket file is absent → missing (the socket-vanish case).
	miss := desktopSocketState(DesktopConfig{Enabled: true, SocketPath: filepath.Join(dir, "nope.sock")})
	if miss.State != "missing" {
		t.Fatalf("absent socket state = %q, want missing", miss.State)
	}

	// A regular file at the path → not-a-socket (never treated as listening).
	reg := filepath.Join(dir, "regular")
	if err := os.WriteFile(reg, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := desktopSocketState(DesktopConfig{Enabled: true, SocketPath: reg}); st.State != "not-a-socket" {
		t.Fatalf("regular-file state = %q, want not-a-socket", st.State)
	}

	// A live Unix listener → listening.
	sock := filepath.Join(dir, "live.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if st := desktopSocketState(DesktopConfig{Enabled: true, SocketPath: sock}); st.State != "listening" {
		t.Fatalf("live socket state = %q, want listening", st.State)
	}
}

func TestGatherStatusAssemblesPoliciesAndServers(t *testing.T) {
	cfg := Config{
		SSH:    SSHConfig{Approval: ApprovalConfig{Policy: PolicyShedDesktop}},
		AWS:    AWSConfig{Approval: ApprovalConfig{Policy: PolicyDenyAll}},
		Docker: DockerConfig{Approval: ApprovalConfig{Policy: PolicyApproveAll}},
	}
	targets := []ServerTarget{
		{Name: "up", URL: "http://up"},
		{Name: "down", URL: "http://down"},
	}
	desktop := desktopStatus{Enabled: true, SocketPath: "/x.sock", State: "listening"}
	probe := func(_ context.Context, baseURL string) ([]string, error) {
		if baseURL == "http://down" {
			return nil, errors.New("connection refused")
		}
		return []string{"ssh-agent"}, nil
	}

	r := gatherStatus(context.Background(), cfg, targets, desktop, probe)

	if r.Policies[protocol.NamespaceSSHAgent] != PolicyShedDesktop ||
		r.Policies[protocol.NamespaceAWSCredentials] != PolicyDenyAll ||
		r.Policies[protocol.NamespaceDockerCredentials] != PolicyApproveAll {
		t.Fatalf("policies = %+v", r.Policies)
	}
	// SSH (and only SSH here) is delegated to shed-desktop.
	if len(r.GateNamespaces) != 1 || r.GateNamespaces[0] != protocol.NamespaceSSHAgent {
		t.Fatalf("gate namespaces = %v", r.GateNamespaces)
	}
	if r.Desktop.State != "listening" {
		t.Fatalf("desktop state = %q", r.Desktop.State)
	}
	// Servers preserve order; reachability + listeners/error reflect the probe.
	if len(r.Servers) != 2 {
		t.Fatalf("servers = %+v", r.Servers)
	}
	if !r.Servers[0].Reachable || strings.Join(r.Servers[0].Listeners, ",") != "ssh-agent" {
		t.Fatalf("server[0] = %+v", r.Servers[0])
	}
	if r.Servers[1].Reachable || r.Servers[1].Error == "" {
		t.Fatalf("server[1] = %+v", r.Servers[1])
	}
}

func TestRenderStatusHumanOutput(t *testing.T) {
	r := statusReport{
		Policies: map[string]string{
			protocol.NamespaceSSHAgent:          PolicyShedDesktop,
			protocol.NamespaceAWSCredentials:    PolicyDenyAll,
			protocol.NamespaceDockerCredentials: PolicyShedDesktop,
		},
		GateNamespaces: []string{protocol.NamespaceSSHAgent, protocol.NamespaceDockerCredentials},
		Desktop:        desktopStatus{Enabled: true, SocketPath: "/x.sock", State: "missing"},
		Servers: []serverStatus{
			{Name: "mac-mini", URL: "http://localhost:8080", Reachable: true, Listeners: []string{"docker-credentials", "ssh-agent"}},
			{Name: "dev", URL: "http://dev:18080", Error: "connection refused"},
		},
	}
	var sb strings.Builder
	renderStatus(&sb, r)
	out := sb.String()

	for _, want := range []string{
		"ssh-agent", "shed-desktop", "(decided in shed-desktop)",
		"state    missing", // surfaces the socket-vanish failure mode
		"mac-mini", "listeners: docker-credentials, ssh-agent",
		"unreachable: connection refused",
		"Servers (2)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusTargetsSingleServer(t *testing.T) {
	// Single-server mode (no discovery) → one unnamed target from Server.
	targets := statusTargets(Config{Server: "http://localhost:8080"})
	if len(targets) != 1 || targets[0].URL != "http://localhost:8080" || targets[0].Name != "" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestRenderLiveStatus(t *testing.T) {
	ls := LiveStatus{
		Version: "0.3.6", Pid: 1234, WrittenAt: "2026-06-06T12:00:00Z",
		Servers: []ServerHealth{
			{Name: "mac-mini", URL: "http://localhost:8080", Namespaces: []NamespaceHealth{
				{Namespace: "ssh-agent", State: "connected"},
				{Namespace: "docker-credentials", State: "reconnecting", LastError: "connection refused"},
			}},
			{Name: "", URL: "http://x:8080"}, // default name, no subscriptions yet
		},
	}
	var sb strings.Builder
	renderLiveStatus(&sb, ls)
	out := sb.String()
	for _, want := range []string{
		"pid 1234", "snapshot: 2026-06-06T12:00:00Z",
		"mac-mini", "ssh-agent", "connected",
		"reconnecting: connection refused",
		"(default)", "(no subscriptions yet)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("live render missing %q:\n%s", want, out)
		}
	}
}

func TestServeStatusSocketRoundTrip(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "status.sock")
	want := LiveStatus{
		Version: "v1", Pid: 99, WrittenAt: "t0",
		Servers: []ServerHealth{{Name: "s1", URL: "u1",
			Namespaces: []NamespaceHealth{{Namespace: "ssh-agent", State: "connected"}}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go serveStatusSocket(ctx, sock, func() LiveStatus { return want }, testLogger())

	// Wait for the listener to bind.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("status socket never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	var got LiveStatus
	if err := json.NewDecoder(conn).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Pid != 99 || len(got.Servers) != 1 || got.Servers[0].Name != "s1" ||
		len(got.Servers[0].Namespaces) != 1 || got.Servers[0].Namespaces[0].State != "connected" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
