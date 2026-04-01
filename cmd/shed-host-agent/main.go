// shed-host-agent is the host-side daemon that handles credential operations
// for shed microVMs. It subscribes to shed-server's plugin message bus and
// performs SSH signing (and in Phase 2, AWS credential vending) using the
// developer's local credentials.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/charliek/shed-extensions/internal/hostclient"
	"github.com/charliek/shed-extensions/internal/version"
)

func main() {
	configPath := flag.String("config", "~/.config/shed/extensions.yaml", "Path to config file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	logger.Info("starting shed-host-agent", "version", version.FullInfo())

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	// Initialize SSH backend
	backend, err := ResolveSSHBackend(cfg.SSH, logger)
	if err != nil {
		logger.Error("failed to initialize SSH backend", "error", err)
		os.Exit(1)
	}

	// Initialize host client
	client := hostclient.New(
		hostclient.WithServerURL(cfg.Server),
		hostclient.WithLogger(logger),
	)

	// Initialize approval gate (Touch ID on macOS, no-op elsewhere)
	approval := newApprovalGate(cfg.SSH.Approval)
	if approval.Enabled() {
		logger.Info("Touch ID approval enabled", "policy", cfg.SSH.Approval.Policy)
	}

	// Initialize audit logger
	audit := NewAuditLogger(cfg.Logging, logger)
	defer audit.Close()

	// Create SSH handler
	sshHandler := NewSSHHandler(backend, client, approval, audit, logger)

	// Run with graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sigCh
		logger.Info("shutting down")
		cancel()
	}()

	logger.Info("subscribing to namespaces", "server", cfg.Server)
	sshHandler.Run(ctx)

	logger.Info("stopped")
}
