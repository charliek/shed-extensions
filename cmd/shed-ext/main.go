// shed-ext provides in-VM health checking for shed-extensions credential
// brokering. It queries each namespace through the message bus and reports
// connectivity status.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/charliek/shed-extensions/internal/busclient"
	"github.com/charliek/shed-extensions/internal/protocol"
)

const statusTimeout = 2 * time.Second

func main() {
	publishURL := flag.String("publish-url", busclient.DefaultPublishURL, "shed-agent publish endpoint")
	flag.Parse()

	if len(flag.Args()) == 0 || flag.Arg(0) != "status" {
		fmt.Fprintf(os.Stderr, "Usage: shed-ext status [--publish-url URL]\n")
		os.Exit(2)
	}

	bus := busclient.New(*publishURL, statusTimeout)
	allOK := true

	// Query SSH agent status
	sshOK := checkSSH(bus)
	if !sshOK {
		allOK = false
	}

	// Query AWS credentials status
	awsOK := checkAWS(bus)
	if !awsOK {
		allOK = false
	}

	if !allOK {
		fmt.Println()
		fmt.Println("Hint: start shed-host-agent on your Mac:")
		fmt.Println("  shed-host-agent --config ~/.config/shed/extensions.yaml")
		os.Exit(1)
	}
}

func checkSSH(bus *busclient.Client) bool {
	req := protocol.SSHStatusRequest{Operation: protocol.SSHOpStatus}
	payload, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()

	respPayload, err := bus.Publish(ctx, protocol.NamespaceSSHAgent, payload)
	if err != nil {
		fmt.Printf("ssh-agent:       \u2717 not connected (shed-host-agent not responding)\n")
		return false
	}

	var resp protocol.SSHStatusResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		fmt.Printf("ssh-agent:       \u2717 not connected (invalid response)\n")
		return false
	}

	keyWord := "keys"
	if resp.KeyCount == 1 {
		keyWord = "key"
	}
	fmt.Printf("ssh-agent:       \u2713 connected (%s mode, %d %s available)\n", resp.Mode, resp.KeyCount, keyWord)
	return true
}

func checkAWS(bus *busclient.Client) bool {
	req := protocol.AWSStatusRequest{Operation: protocol.AWSOpStatus}
	payload, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()

	respPayload, err := bus.Publish(ctx, protocol.NamespaceAWSCredentials, payload)
	if err != nil {
		fmt.Printf("aws-credentials: \u2717 not connected (shed-host-agent not responding)\n")
		return false
	}

	var resp protocol.AWSStatusResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		fmt.Printf("aws-credentials: \u2717 not connected (invalid response)\n")
		return false
	}

	detail := fmt.Sprintf("role: %s", resp.Role)
	if resp.CachedUntil != "" {
		if t, err := time.Parse("2006-01-02T15:04:05Z", resp.CachedUntil); err == nil {
			detail += fmt.Sprintf(", cached until %s UTC", t.Format("15:04"))
		}
	}
	fmt.Printf("aws-credentials: \u2713 connected (%s)\n", detail)
	return true
}
