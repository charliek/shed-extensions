package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/charliek/shed-extensions/internal/protocol"
	sdk "github.com/charliek/shed/sdk"
)

// statusReport is the agent's self-reported configuration + live reachability,
// produced by `shed-host-agent status`. It is an environment probe: it reads the
// config, checks the desktop socket, and asks each configured server which
// namespaces are registered. It does NOT (yet) report whether THIS agent is the
// registrant — that needs the SDK to surface per-subscription connection state
// (charliek/shed#182); a `--live` mode will add it.
type statusReport struct {
	Policies       map[string]string `json:"policies"`        // namespace -> effective policy
	GateNamespaces []string          `json:"gate_namespaces"` // delegated to shed-desktop
	Desktop        desktopStatus     `json:"desktop"`
	Servers        []serverStatus    `json:"servers"`
}

type desktopStatus struct {
	Enabled    bool   `json:"enabled"`
	SocketPath string `json:"socket_path"`
	// State is one of: disabled, listening, missing, stale, not-a-socket, error.
	State string `json:"state"`
	// Detail carries the underlying error for the stale/error states.
	Detail string `json:"detail,omitempty"`
}

type serverStatus struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Reachable bool     `json:"reachable"`
	Listeners []string `json:"listeners,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// statusNamespaces is the provider order shown in the status report.
var statusNamespaces = []string{
	protocol.NamespaceSSHAgent,
	protocol.NamespaceAWSCredentials,
	protocol.NamespaceDockerCredentials,
}

// listenerProbe asks one server which plugin-listener namespaces are registered.
type listenerProbe func(ctx context.Context, baseURL string) ([]string, error)

// runStatus gathers and prints the status report. Returns a process exit code.
func runStatus(cfg Config, jsonOut bool, out io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 3 * time.Second}
	report := gatherStatus(ctx, cfg, statusTargets(cfg), desktopSocketState(cfg.Desktop),
		func(ctx context.Context, baseURL string) ([]string, error) {
			return probeListeners(ctx, client, baseURL)
		})

	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "status: encode:", err)
			return 1
		}
		return 0
	}
	renderStatus(out, report)
	return 0
}

// statusTargets resolves the servers this agent would watch, via the same
// resolveTargets the daemon uses. A discovery read error is best-effort here:
// report whatever resolves (single-server, or none) rather than failing.
func statusTargets(cfg Config) []ServerTarget {
	targets, _ := resolveTargets(cfg)
	return targets
}

// gatherStatus assembles the report. The desktop socket state is computed by the
// caller (so it's injectable); each server is probed concurrently.
func gatherStatus(ctx context.Context, cfg Config, targets []ServerTarget, desktop desktopStatus, probe listenerProbe) statusReport {
	report := statusReport{
		Policies: map[string]string{
			protocol.NamespaceSSHAgent:          cfg.SSH.Approval.EffectivePolicy(),
			protocol.NamespaceAWSCredentials:    cfg.AWS.Approval.EffectivePolicy(),
			protocol.NamespaceDockerCredentials: cfg.Docker.Approval.EffectivePolicy(),
		},
		GateNamespaces: desktopGateNamespaces(cfg),
		Desktop:        desktop,
		Servers:        make([]serverStatus, len(targets)),
	}

	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t ServerTarget) {
			defer wg.Done()
			s := serverStatus{Name: t.Name, URL: t.URL}
			if ns, err := probe(ctx, t.URL); err != nil {
				s.Error = err.Error()
			} else {
				s.Reachable = true
				s.Listeners = ns
			}
			report.Servers[i] = s
		}(i, t)
	}
	wg.Wait()
	return report
}

// desktopSocketState reports whether the desktop approval socket is being served.
// It distinguishes "missing" (the socket-vanish failure mode) from "stale" (a
// leftover socket file with nothing accepting) so the cause is obvious.
func desktopSocketState(d DesktopConfig) desktopStatus {
	st := desktopStatus{Enabled: d.Enabled, SocketPath: d.SocketPath}
	if !d.Enabled {
		st.State = "disabled"
		return st
	}
	fi, err := os.Lstat(d.SocketPath)
	if err != nil {
		if os.IsNotExist(err) {
			st.State = "missing"
		} else {
			st.State = "error"
			st.Detail = err.Error()
		}
		return st
	}
	if fi.Mode()&os.ModeSocket == 0 {
		st.State = "not-a-socket"
		return st
	}
	if socketIsLive(d.SocketPath) {
		st.State = "listening"
	} else {
		st.State = "stale" // a leftover socket file with nothing accepting
	}
	return st
}

// probeListeners GETs a server's registered plugin-listener namespaces.
func probeListeners(ctx context.Context, client *http.Client, baseURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/plugins/listeners", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Listeners []struct {
			Namespace string `json:"namespace"`
		} `json:"listeners"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ns := make([]string, 0, len(body.Listeners))
	for _, l := range body.Listeners {
		ns = append(ns, l.Namespace)
	}
	sort.Strings(ns)
	return ns, nil
}

// renderStatus writes the human-readable report.
func renderStatus(out io.Writer, r statusReport) {
	fmt.Fprintln(out, "shed-host-agent status")
	fmt.Fprintln(out)

	gated := make(map[string]bool, len(r.GateNamespaces))
	for _, ns := range r.GateNamespaces {
		gated[ns] = true
	}
	fmt.Fprintln(out, "Approval policies:")
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, ns := range statusNamespaces {
		note := ""
		if gated[ns] {
			note = "(decided in shed-desktop)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", ns, r.Policies[ns], note)
	}
	tw.Flush()
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Desktop approval channel:")
	fmt.Fprintf(out, "  enabled  %t\n", r.Desktop.Enabled)
	if r.Desktop.Enabled {
		fmt.Fprintf(out, "  socket   %s\n", r.Desktop.SocketPath)
		state := r.Desktop.State
		if r.Desktop.Detail != "" {
			state += " (" + r.Desktop.Detail + ")"
		}
		fmt.Fprintf(out, "  state    %s\n", state)
	}
	fmt.Fprintln(out)

	fmt.Fprintf(out, "Servers (%d):\n", len(r.Servers))
	if len(r.Servers) == 0 {
		fmt.Fprintln(out, "  (none configured)")
		return
	}
	stw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	for _, s := range r.Servers {
		mark, detail := "x", "unreachable: "+s.Error
		if s.Reachable {
			mark = "ok"
			detail = "listeners: " + listOrNone(s.Listeners)
		}
		fmt.Fprintf(stw, "  %s\t%s\t%s\t%s\n", mark, serverLabel(s.Name), s.URL, detail)
	}
	stw.Flush()
}

func listOrNone(ns []string) string {
	if len(ns) == 0 {
		return "none"
	}
	return strings.Join(ns, ", ")
}

func serverLabel(name string) string {
	if name == "" {
		return "(default)"
	}
	return name
}

// runStatusLive queries the running daemon's read-only status socket for its
// authoritative per-(server, namespace) connection state and prints it. Unlike
// the env-probe, this reflects whether THIS agent is connected (vs. retrying).
func runStatusLive(cfg Config, jsonOut bool, out io.Writer) int {
	conn, err := net.DialTimeout("unix", statusSocketPath(cfg), 2*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status --live: the agent isn't serving live status "+
			"(not running, or an older build): %v\n", err)
		return 1
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	var ls LiveStatus
	if err := json.NewDecoder(conn).Decode(&ls); err != nil {
		fmt.Fprintf(os.Stderr, "status --live: reading from the agent: %v\n", err)
		return 1
	}
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(ls); err != nil {
			fmt.Fprintln(os.Stderr, "status --live: encode:", err)
			return 1
		}
		return 0
	}
	renderLiveStatus(out, ls)
	return 0
}

// renderLiveStatus writes the human-readable live report.
func renderLiveStatus(out io.Writer, ls LiveStatus) {
	fmt.Fprintf(out, "shed-host-agent live status (pid %d, %s)\n", ls.Pid, ls.Version)
	fmt.Fprintf(out, "snapshot: %s\n\n", ls.WrittenAt)
	if len(ls.Servers) == 0 {
		fmt.Fprintln(out, "No servers being watched.")
		return
	}
	for _, sv := range ls.Servers {
		fmt.Fprintf(out, "%s  (%s)\n", serverLabel(sv.Name), sv.URL)
		if len(sv.Namespaces) == 0 {
			fmt.Fprintln(out, "  (no subscriptions yet)")
			continue
		}
		tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
		for _, ns := range sv.Namespaces {
			detail := ns.State
			if ns.LastError != "" {
				detail += ": " + ns.LastError
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", connMark(ns.State), ns.Namespace, detail)
		}
		tw.Flush()
	}
}

func connMark(state string) string {
	switch state {
	case sdk.ConnConnected:
		return "ok"
	case sdk.ConnStopped:
		return "-"
	default:
		return "x"
	}
}
