// docker-credential-shed is the guest-side Docker credential helper that runs
// inside shed microVMs. Docker execs this binary on demand to resolve registry
// credentials. It translates credential helper protocol operations into message
// bus requests to the host agent.
//
// This is a one-shot CLI, not a daemon. Docker execs it per operation:
//
//	echo "us-docker.pkg.dev" | docker-credential-shed get
//	docker-credential-shed list
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charliek/shed-extensions/internal/dockercred"
	"github.com/charliek/shed-extensions/internal/version"
)

const defaultPublishURL = "http://127.0.0.1:498/v1/publish"
const requestTimeout = 5 * time.Second

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "docker-credential-shed %s\n", version.Info())
		fmt.Fprintf(os.Stderr, "usage: docker-credential-shed <get|list|store|erase>\n")
		os.Exit(1)
	}

	publishURL := defaultPublishURL
	if v := os.Getenv("SHED_PUBLISH_URL"); v != "" {
		publishURL = v
	}

	command := os.Args[1]

	switch command {
	case "get":
		doGet(publishURL)
	case "list":
		doList(publishURL)
	case "store", "erase":
		fmt.Fprintf(os.Stderr, "docker-credential-shed: %s not supported (read-only credential broker)\n", command)
		os.Exit(1)
	case "version":
		fmt.Fprintf(os.Stderr, "docker-credential-shed %s\n", version.FullInfo())
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "docker-credential-shed: unknown command %q\n", command)
		os.Exit(1)
	}
}

func doGet(publishURL string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: reading stdin: %s\n", err)
		os.Exit(1)
	}

	serverURL := strings.TrimSpace(string(input))
	if serverURL == "" {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: empty server URL\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	helper := dockercred.New(dockercred.WithPublishURL(publishURL))
	resp, err := helper.Get(ctx, serverURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: %s\n", err)
		os.Exit(1)
	}

	// Docker expects PascalCase JSON fields from credential helpers
	out := struct {
		ServerURL string `json:"ServerURL"`
		Username  string `json:"Username"`
		Secret    string `json:"Secret"`
	}{
		ServerURL: resp.ServerURL,
		Username:  resp.Username,
		Secret:    resp.Secret,
	}

	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: encoding response: %s\n", err)
		os.Exit(1)
	}
}

func doList(publishURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	helper := dockercred.New(dockercred.WithPublishURL(publishURL))
	registries, err := helper.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: %s\n", err)
		os.Exit(1)
	}

	if err := json.NewEncoder(os.Stdout).Encode(registries); err != nil {
		fmt.Fprintf(os.Stderr, "docker-credential-shed: encoding response: %s\n", err)
		os.Exit(1)
	}
}
