package github

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"git-sd/internal/git"
)

// EnvError is a problem with the user's GitHub CLI setup (not installed, not
// authenticated), as opposed to a failure of one particular call.
type EnvError struct {
	Message string
}

func (e *EnvError) Error() string {
	return e.Message
}

// The two environment problems git-sd reports, worded identically wherever
// they surface so users always see the same fix.
var (
	ErrGHNotInstalled     = &EnvError{"'gh' CLI not found. Please install it to use git-sd."}
	ErrGHNotAuthenticated = &EnvError{"You are not authenticated with 'gh'. Please run 'gh auth login'."}
)

// IsEnvError reports whether err is (or wraps) an environment problem.
func IsEnvError(err error) bool {
	var envErr *EnvError
	return errors.As(err, &envErr)
}

// ghState is what this process has learned about the GitHub CLI. git-sd is a
// short-lived process, so process-wide state is exactly one invocation. It is
// safe for concurrent use (status looks PRs up concurrently).
type ghState struct {
	lookOnce sync.Once
	lookErr  error // non-nil if gh is not on PATH

	verified atomic.Bool // set once any gh call has succeeded

	mu      sync.Mutex
	failure error // a definite environment failure; later calls return it without spawning gh
}

var state ghState

// preflight returns the error a gh call should fail with without running gh at
// all: gh not installed, or an environment failure already seen in this run.
func (s *ghState) preflight() error {
	s.lookOnce.Do(func() {
		if _, err := exec.LookPath("gh"); err != nil {
			s.lookErr = ErrGHNotInstalled
		}
	})
	if s.lookErr != nil {
		return s.lookErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *ghState) recordFailure(err error) {
	s.mu.Lock()
	s.failure = err
	s.mu.Unlock()
}

// classifyGHFailure turns a failed gh call into an environment error if that
// is what it was. gh exits with code 4 when authentication is required; an
// expired or invalid token instead exits 1 with an HTTP 401 message.
func classifyGHFailure(err error, output string) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 4 {
		return ErrGHNotAuthenticated
	}
	for _, marker := range []string{"HTTP 401", "Bad credentials", "gh auth login"} {
		if strings.Contains(output, marker) {
			return ErrGHNotAuthenticated
		}
	}
	return nil
}

// ghRun is the single place git-sd invokes the GitHub CLI. Every gh call goes
// through ghOutput or ghCombined (a test enforces it), so gh being missing or
// unauthenticated is reported with the same message from any command.
func ghRun(stdin []byte, combined bool, args ...string) ([]byte, error) {
	if err := state.preflight(); err != nil {
		return nil, err
	}

	cmd := exec.Command("gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if combined {
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	err := cmd.Run()
	if err == nil {
		state.verified.Store(true)
		return stdout.Bytes(), nil
	}

	if envErr := classifyGHFailure(err, stdout.String()+stderr.String()); envErr != nil {
		state.recordFailure(envErr)
		return nil, envErr
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return stdout.Bytes(), err
}

// ghOutput runs gh and returns its stdout; a failure's stderr is folded into
// the returned error.
func ghOutput(args ...string) ([]byte, error) {
	return ghRun(nil, false, args...)
}

// ghCombined runs gh and returns stdout and stderr together, even on failure
// (some callers parse a JSON error body from it).
func ghCombined(args ...string) ([]byte, error) {
	return ghRun(nil, true, args...)
}

// ghCombinedStdin is ghCombined with a request body on stdin.
func ghCombinedStdin(stdin []byte, args ...string) ([]byte, error) {
	return ghRun(stdin, true, args...)
}

// readyOnce guards ensureReady's single probe.
var (
	readyOnce sync.Once
	readyErr  error
)

// ensureReady confirms, once per process, that gh can authenticate, for the
// places that act before their first gh call — pushing a branch. It makes one
// real, cheap authenticated call, unless some gh call has already succeeded.
// Only an environment problem blocks the caller; a transient failure of the
// probe itself does not, since the push that follows would report it.
func ensureReady() error {
	readyOnce.Do(func() {
		if state.verified.Load() {
			return
		}
		if _, err := ghOutput("api", "user", "--jq", ".login"); IsEnvError(err) {
			readyErr = err
		}
	})
	return readyErr
}

// resetForTests clears everything learned about gh, so tests start clean.
func resetForTests() {
	state.lookOnce = sync.Once{}
	state.lookErr = nil
	state.verified.Store(false)
	state.recordFailure(nil)
	readyOnce = sync.Once{}
	readyErr = nil
}

// pushHint appends git.AuthHint's advice to a failed git network command's
// output, if it looks like an authentication failure.
func pushHint(output string) string {
	if hint := git.AuthHint(output); hint != "" {
		return output + "\n" + hint
	}
	return output
}
