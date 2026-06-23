package rc

import (
	"bytes"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Result is the outcome of one tmux invocation.
type Result struct {
	Stdout string
	Stderr string
	Code   int
}

// Runner runs `tmux <args>` and returns the result. Injected so the operations are
// testable against a fake tmux.
type Runner interface {
	Run(args ...string) Result
}

type execRunner struct{}

func (execRunner) Run(args ...string) Result {
	cmd := exec.Command("tmux", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			// tmux not found / could not start.
			code = -1
			if errb.Len() == 0 {
				errb.WriteString(err.Error())
			}
		}
	}
	return Result{Stdout: out.String(), Stderr: errb.String(), Code: code}
}

// DefaultRunner runs the real tmux binary.
func DefaultRunner() Runner { return execRunner{} }

var (
	dupSessionRe = regexp.MustCompile(`(?i)duplicate session|already exists`)
	// tmux reports a gone target differently per command: kill-session says "can't
	// find session", capture-pane/send-keys say "can't find pane"; killing the last
	// session stops the server ("no server running"). All mean "already gone".
	missingSessionRe = regexp.MustCompile(`(?i)can't find session|can't find pane|no session|no server running`)
)

func isDuplicateSession(stderr string) bool { return dupSessionRe.MatchString(stderr) }
func isMissingSession(stderr string) bool   { return missingSessionRe.MatchString(stderr) }

// createSession runs `tmux new-session -d -s <name> -c <workdir> <envArgs…> <inner>`.
func createSession(r Runner, name, workdir string, envArgs []string, inner string) Result {
	args := append([]string{"new-session", "-d", "-s", name, "-c", workdir}, envArgs...)
	args = append(args, inner)
	return r.Run(args...)
}

// capturePane returns the last 200 lines of a session's pane.
func capturePane(r Runner, name string) Result {
	return r.Run("capture-pane", "-t", name, "-p", "-S", "-200")
}

// listSessionNames returns the rc-* tmux session names (empty if no server/sessions).
func listSessionNames(r Runner) []string {
	res := r.Run("ls", "-F", "#{session_name}")
	if res.Code != 0 {
		return nil // no server running / no sessions
	}
	var names []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, TmuxPrefix) {
			names = append(names, line)
		}
	}
	return names
}

// showEnvironment returns a session's SHED_RC_*-filtered show-environment dump.
func showEnvironment(r Runner, name string) string {
	res := r.Run("show-environment", "-t", name)
	if res.Code != 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.HasPrefix(line, envPrefix) {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// killSession kills a session; a missing session is reported via Result for the
// caller to treat as idempotent success.
func killSession(r Runner, name string) Result {
	return r.Run("kill-session", "-t", name)
}

// sendLineSettle is the pause between typing a line and submitting it. A freshly
// ready remote-control REPL can still be ingesting the literal paste, and an Enter
// that arrives mid-ingest is dropped — leaving the line typed but unsubmitted. A var
// so tests can zero it.
var sendLineSettle = 750 * time.Millisecond

// sendLine types text literally into a session, then submits with Enter. The `--`
// stops tmux option parsing so a line beginning with `-` is sent as text, not a flag.
// A short settle between the paste and Enter avoids the Enter being dropped.
func sendLine(r Runner, name, text string) Result {
	res := r.Run("send-keys", "-t", name, "-l", "--", text)
	if res.Code != 0 {
		return res
	}
	time.Sleep(sendLineSettle)
	return r.Run("send-keys", "-t", name, "Enter")
}

// sendEnter presses Enter (used to accept the pre-selected "Yes, I trust this folder").
func sendEnter(r Runner, name string) Result {
	return r.Run("send-keys", "-t", name, "Enter")
}

// acceptBypassPrompt accepts claude's "Bypass Permissions mode" dialog by selecting
// "2. Yes, I accept": option "1. No, exit" is pre-selected, so move down once, then
// Enter. A failed Down short-circuits (don't Enter on "No, exit").
func acceptBypassPrompt(r Runner, name string) Result {
	if res := r.Run("send-keys", "-t", name, "Down"); res.Code != 0 {
		return res
	}
	return r.Run("send-keys", "-t", name, "Enter")
}
