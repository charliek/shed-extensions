// shed-host-agent is the host-side daemon that handles credential operations
// for shed microVMs. It subscribes to shed-server's plugin message bus and
// performs SSH signing and AWS credential vending using the developer's local
// credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charliek/shed-extensions/internal/version"
)

func main() {
	configPath := flag.String("config", "~/.config/shed/extensions.yaml", "Path to config file")
	flag.Parse()

	// Handle version subcommand
	if flag.NArg() > 0 && flag.Arg(0) == "version" {
		fmt.Println(version.FullInfo())
		os.Exit(0)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
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
	var desktop *DesktopServer
	if cfg.Desktop.Enabled {
		desktop = NewDesktopServer(cfg.Desktop, audit, version.FullInfo(), logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			desktop.Listen(ctx)
		}()
		logger.Info("shed-desktop approval channel enabled", "socket", cfg.Desktop.SocketPath)
	}

	// Select the approval gate now that the desktop channel (if any) exists.
	approval := selectApprovalGate(cfg, desktop)
	logger.Info("approval gate", "method", approval.Method(), "enabled", approval.Enabled())

	// Build the server-agnostic backends once; per-server handlers share them.
	// AWS and Docker are optional — a missing/unconfigured backend stays nil and
	// its handler simply isn't started for any server.
	var awsBackend AWSBackend
	if b, err := NewSTSBackend(ctx, cfg.AWS, logger); err != nil {
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
		SSHBackend:    sshBackend,
		AWSBackend:    awsBackend,
		DockerBackend: dockerBackend,
		Approval:      approval,
		Audit:         audit,
		Logger:        logger,
	}
	sup := NewSupervisor(ctx, deps)

	if cfg.Discovery != nil && cfg.Server != "" && cfg.Server != "http://localhost:8080" {
		logger.Warn("`server:` is ignored when `discovery:` is configured", "server", cfg.Server)
	}

	// computeDesired resolves the set of servers to broker for. The bool is
	// false only on a discovery read error, in which case the caller skips the
	// reconcile and keeps the current servers (rather than tearing them all
	// down on a transient/partial-write read).
	computeDesired := func() ([]ServerTarget, bool) {
		var discovered []ServerTarget
		if cfg.Discovery != nil {
			d, err := LoadDiscoveredServers(cfg.Discovery.Source)
			if err != nil {
				logger.Warn("discovery read failed; keeping current servers",
					"source", cfg.Discovery.Source, "error", err)
				return nil, false
			}
			discovered = d
		}
		return cfg.ResolveTargets(discovered), true
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
