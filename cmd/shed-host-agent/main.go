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

	sdk "github.com/charliek/shed/sdk"

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

	// Initialize host client (shared by all handlers)
	client := sdk.NewHostClient(
		sdk.WithServerURL(cfg.Server),
		sdk.WithLogger(logger),
	)

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

	// Start SSH handler
	sshHandler := NewSSHHandler(sshBackend, client, approval, audit, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		sshHandler.Run(ctx)
	}()

	// Start AWS handler (optional — don't fail if AWS isn't configured)
	awsBackend, err := NewSTSBackend(ctx, cfg.AWS, logger)
	if err != nil {
		logger.Warn("AWS handler disabled", "error", err)
	} else {
		awsHandler := NewAWSHandler(awsBackend, client, audit, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			awsHandler.Run(ctx)
		}()
	}

	// Start Docker handler (optional — don't fail if Docker isn't configured)
	dockerBackend, err := NewDockerBackend(cfg.Docker, logger)
	if err != nil {
		logger.Warn("Docker handler disabled", "error", err)
	} else {
		dockerHandler := NewDockerHandler(dockerBackend, client, audit, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			dockerHandler.Run(ctx)
		}()
	}

	logger.Info("subscribing to namespaces", "server", cfg.Server)
	wg.Wait()

	logger.Info("stopped")
}
