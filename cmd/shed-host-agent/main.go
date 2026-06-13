// shed-host-agent is the host-side daemon that handles credential operations
// for shed microVMs. It subscribes to shed-server's plugin message bus and
// performs SSH signing and AWS credential vending using the developer's local
// credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charliek/shed-extensions/internal/protocol"
	"github.com/charliek/shed-extensions/internal/version"
)

// desktopGateNamespaces lists the credential namespaces whose approval policy is
// shed-desktop — advertised to the app so it shows the matching approval UI.
func desktopGateNamespaces(cfg Config) []string {
	var ns []string
	if cfg.SSH.Approval.EffectivePolicy() == PolicyShedDesktop {
		ns = append(ns, protocol.NamespaceSSHAgent)
	}
	if cfg.AWS.Approval.EffectivePolicy() == PolicyShedDesktop {
		ns = append(ns, protocol.NamespaceAWSCredentials)
	}
	if cfg.Docker.Approval.EffectivePolicy() == PolicyShedDesktop {
		ns = append(ns, protocol.NamespaceDockerCredentials)
	}
	return ns
}

func main() {
	configPath := flag.String("config", "~/.config/shed/extensions.yaml", "Path to config file")
	logFile := flag.String("log-file", "", "Write the operational log to this file (rotated); empty = stderr")
	flag.Parse()

	// Handle version subcommand
	if flag.NArg() > 0 && flag.Arg(0) == "version" {
		fmt.Println(version.FullInfo())
		os.Exit(0)
	}

	// Handle status subcommand: a one-shot environment probe (config + desktop
	// socket + per-server reachability), separate from running the daemon.
	if flag.NArg() > 0 && flag.Arg(0) == "status" {
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "status: load config:", err)
			os.Exit(1)
		}
		jsonOut, live := false, false
		for _, a := range flag.Args()[1:] {
			switch a {
			case "--json", "-json":
				jsonOut = true
			case "--live", "-live":
				live = true
			}
		}
		if live {
			os.Exit(runStatusLive(cfg, jsonOut, os.Stdout))
		}
		os.Exit(runStatus(cfg, jsonOut, os.Stdout))
	}

	logWriter := newLogWriter(*logFile)
	if c, ok := logWriter.(io.Closer); ok {
		defer c.Close()
	}
	logger := slog.New(slog.NewTextHandler(logWriter, nil))
	slog.SetDefault(logger)

	logger.Info("starting shed-host-agent", "version", version.FullInfo())

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	// Initialize audit logger (also fans entries out to the desktop channel)
	audit := NewAuditLogger(cfg.Logging, logger)
	defer audit.Close()

	// Graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("shutting down")
		cancel()
	}()

	// Initialize SSH backend
	sshBackend, err := ResolveSSHBackend(cfg.SSH, logger)
	if err != nil {
		logger.Error("failed to initialize SSH backend", "error", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup

	// Optional shed-desktop approval delegation channel (feature-flagged off
	// by default). When enabled, the agent serves a local UDS the app uses
	// for the all-namespace audit/event stream and SSH approval decisions.
	gateNamespaces := desktopGateNamespaces(cfg)
	var desktop *DesktopServer
	if cfg.Desktop.Enabled {
		desktop = NewDesktopServer(cfg.Desktop, audit, version.FullInfo(), gateNamespaces, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			desktop.Listen(ctx)
		}()
		logger.Info("shed-desktop approval channel enabled", "socket", cfg.Desktop.SocketPath)
	}

	// Build the per-extension approval gates now that the desktop channel (if
	// any) exists. An empty/omitted policy resolves to deny-all (fail closed).
	sshApproval := gateFor(protocol.NamespaceSSHAgent, protocol.SSHOpSign, cfg.SSH.Approval, desktop)
	awsApproval := gateFor(protocol.NamespaceAWSCredentials, protocol.AWSOpGetCredentials, cfg.AWS.Approval, desktop)
	dockerApproval := gateFor(protocol.NamespaceDockerCredentials, protocol.DockerOpGet, cfg.Docker.Approval, desktop)
	logger.Info("approval policies",
		"ssh", cfg.SSH.Approval.EffectivePolicy(),
		"aws", cfg.AWS.Approval.EffectivePolicy(),
		"docker", cfg.Docker.Approval.EffectivePolicy())

	// Build the server-agnostic backends once; per-server handlers share them.
	// AWS and Docker are optional — a missing/unconfigured backend stays nil and
	// its handler simply isn't started for any server.
	var awsBackend AWSBackend
	if b, err := NewSTSBackend(cfg.AWS, logger); err != nil {
		logger.Warn("AWS handler disabled", "error", err)
	} else {
		awsBackend = b
	}

	var dockerBackend DockerBackend
	if b, err := NewDockerBackend(cfg.Docker, logger); err != nil {
		logger.Warn("Docker handler disabled", "error", err)
	} else {
		dockerBackend = b
	}

	deps := SharedDeps{
		SSHBackend:     sshBackend,
		AWSBackend:     awsBackend,
		DockerBackend:  dockerBackend,
		SSHApproval:    sshApproval,
		AWSApproval:    awsApproval,
		DockerApproval: dockerApproval,
		Audit:          audit,
		Logger:         logger,
	}
	sup := NewSupervisor(ctx, deps)

	// Serve the read-only status socket so `shed-host-agent status --live` can
	// query this daemon's live per-server connection state.
	statusSock := statusSocketPath(cfg)
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveStatusSocket(ctx, statusSock, func() LiveStatus { return buildLiveStatus(sup, version.FullInfo()) }, logger)
	}()

	if cfg.Discovery != nil && cfg.Server != "" && cfg.Server != "http://localhost:8080" {
		logger.Warn("`server:` is ignored when `discovery:` is configured", "server", cfg.Server)
	}

	// computeDesired resolves the set of servers to broker for. The bool is
	// false only on a discovery read error, in which case the caller skips the
	// reconcile and keeps the current servers (rather than tearing them all
	// down on a transient/partial-write read). Shares resolveTargets with
	// `status` so the two never disagree about which servers are watched.
	computeDesired := func() ([]ServerTarget, bool) {
		targets, err := resolveTargets(cfg)
		if err != nil {
			logger.Warn("discovery read failed; keeping current servers",
				"source", cfg.Discovery.Source, "error", err)
			return nil, false
		}
		return targets, true
	}
	reconcile := func() {
		if desired, ok := computeDesired(); ok {
			sup.Reconcile(desired)
		}
	}

	// One path for both modes. Legacy single-server is just a discovery config
	// that reconciles once and never reloads (watch: off); computeDesired
	// already returns the single target when cfg.Discovery is nil.
	watchCfg := DiscoveryConfig{Watch: "off"}
	if cfg.Discovery != nil {
		watchCfg = *cfg.Discovery
		logger.Info("multi-server discovery enabled", "source", watchCfg.Source, "watch", watchCfg.Watch)
	} else {
		logger.Info("brokering for single server", "server", cfg.Server)
	}
	runWatchLoop(ctx, watchCfg, reconcile, logger)

	logger.Info("stopping server watchers")
	sup.Shutdown()

	wg.Wait()
	logger.Info("stopped")
}
