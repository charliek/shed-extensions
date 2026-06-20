// shed-ext-rc is the guest-side RC Session Convention v2 helper. It runs inside a
// shed and is invoked over SSH by orchestrators (shed-remote-agent, shed-desktop,
// the shed CLI) to create, list, probe, prompt, and tear down remote-control tmux
// sessions named rc-<slug>. It prints the neutral JSON DTO on stdout and writes
// diagnostics to stderr; exit codes carry domain outcomes (see exitCode).
//
// This is a one-shot CLI, not a daemon. All tmux work happens locally in the shed.
//
//	shed-ext-rc create --kind claude-rc --name my-shed/foo [--wait] [--prompt-stdin]
//	shed-ext-rc list
//	shed-ext-rc probe --slug abc123
//	shed-ext-rc accept-trust --slug abc123
//	shed-ext-rc prompt --slug abc123 [--session-id <uuid>]   # text on stdin
//	shed-ext-rc kill --slug abc123
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charliek/shed-extensions/internal/rc"
	"github.com/charliek/shed-extensions/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	r := rc.DefaultRunner()
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "create":
		return doCreate(r, rest)
	case "list":
		return doList(r, rest)
	case "probe":
		return doProbe(r, rest)
	case "accept-trust":
		return doAcceptTrust(r, rest)
	case "prompt":
		return doPrompt(r, rest)
	case "kill":
		return doKill(r, rest)
	case "version", "--version", "-v":
		fmt.Printf("shed-ext-rc %s\n", version.FullInfo())
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "shed-ext-rc: unknown command %q\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: shed-ext-rc <command> [flags]

commands:
  create   --kind <k> --name <display> [--slug s] [--workdir d] [--created-by t/v]
           [--target label] [--wait] [--interactive-shell] [--prompt-stdin]
  list
  probe    --slug <s>
  accept-trust --slug <s>
  prompt   --slug <s> [--session-id <uuid>]   (text read from stdin)
  kill     --slug <s>
  version
`)
}

// exitCode maps a domain error to a process exit code. SSH-transport classification
// (auth/unreachable) is the orchestrator's job; these are the binary-local outcomes.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, rc.ErrBadArgs):
		return 2
	case errors.Is(err, rc.ErrDuplicateSlug):
		return 3
	case errors.Is(err, rc.ErrSessionNotFound):
		return 4
	default:
		return 1
	}
}

// fail prints err to stderr and returns its exit code.
func fail(err error) int {
	fmt.Fprintf(os.Stderr, "shed-ext-rc: %s\n", err)
	return exitCode(err)
}

// readStdinLine reads all of stdin and strips a single trailing newline (and CR), so
// a kickoff line can be piped in without it being mistaken for a CLI flag.
func readStdinLine() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}
	s := strings.TrimSuffix(string(data), "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, nil
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "shed-ext-rc: encoding output: %s\n", err)
		return 1
	}
	return 0
}

func doCreate(r rc.Runner, args []string) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	var (
		kind        = fs.String("kind", string(rc.DefaultKind), "session kind: claude-rc|claude-broker|shell")
		name        = fs.String("name", "", "display name (--name to claude); defaults to the slug")
		slug        = fs.String("slug", "", "caller-supplied slug (generated when empty)")
		workdir     = fs.String("workdir", "", "working directory (defaults to $SHED_WORKSPACE)")
		createdBy   = fs.String("created-by", "", "provenance <tool>/<version>")
		target      = fs.String("target", "", "advisory target label")
		wait        = fs.Bool("wait", false, "block until ready, accept trust, deliver prompt")
		interactive = fs.Bool("interactive-shell", false, "wrap claude kinds in `bash -ic`")
		promptStdin = fs.Bool("prompt-stdin", false, "read a kickoff prompt line from stdin")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	prompt := ""
	if *promptStdin {
		p, err := readStdinLine()
		if err != nil {
			return fail(err)
		}
		if p == "" {
			return fail(fmt.Errorf("%w: --prompt-stdin given but stdin is empty", rc.ErrBadArgs))
		}
		prompt = p
	}

	session, err := rc.Create(r, os.Getenv, rc.CreateOptions{
		Kind:             rc.Kind(*kind),
		DisplayName:      *name,
		Slug:             *slug,
		Workdir:          *workdir,
		CreatedBy:        *createdBy,
		Target:           *target,
		Prompt:           prompt,
		Wait:             *wait,
		InteractiveShell: *interactive,
	}, nil)
	if err != nil {
		return fail(err)
	}
	return printJSON(session)
}

func doList(r rc.Runner, args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return printJSON(rc.List(r, nil))
}

// runSlugCmd parses a slug-only subcommand (probe/accept-trust/kill) and runs fn.
func runSlugCmd(name string, args []string, fn func(slug string) int) int {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	slug := fs.String("slug", "", "session slug")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		return fail(fmt.Errorf("%w: --slug is required", rc.ErrBadArgs))
	}
	return fn(*slug)
}

func doProbe(r rc.Runner, args []string) int {
	return runSlugCmd("probe", args, func(slug string) int {
		session, err := rc.Probe(r, slug, nil)
		if err != nil {
			return fail(err)
		}
		return printJSON(session)
	})
}

func doAcceptTrust(r rc.Runner, args []string) int {
	return runSlugCmd("accept-trust", args, func(slug string) int {
		if err := rc.AcceptTrust(r, slug); err != nil {
			return fail(err)
		}
		return 0
	})
}

func doPrompt(r rc.Runner, args []string) int {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	slug := fs.String("slug", "", "session slug")
	sessionID := fs.String("session-id", "", "expected SHED_RC_ID (guards a recreated slug)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *slug == "" {
		return fail(fmt.Errorf("%w: --slug is required", rc.ErrBadArgs))
	}
	text, err := readStdinLine()
	if err != nil {
		return fail(err)
	}
	if text == "" {
		return fail(fmt.Errorf("%w: prompt text (stdin) is empty", rc.ErrBadArgs))
	}
	if err := rc.Prompt(r, rc.PromptOptions{Slug: *slug, Text: text, SessionID: *sessionID}); err != nil {
		return fail(err)
	}
	return 0
}

func doKill(r rc.Runner, args []string) int {
	return runSlugCmd("kill", args, func(slug string) int {
		if err := rc.Kill(r, slug); err != nil {
			return fail(err)
		}
		return 0
	})
}
