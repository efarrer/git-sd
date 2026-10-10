package github

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGH installs a fake `gh` first on PATH whose behavior is chosen by mode:
// "ok" prints a PR as JSON, "auth4" exits 4 (authentication required), "bad401"
// exits 1 with an HTTP 401, "fail" exits 1 with an unrelated error. Every call
// is appended to the returned log file. State from earlier tests is cleared.
func fakeGH(t *testing.T, mode string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	script := `#!/bin/sh
echo "$@" >> "$GH_FAKE_LOG"
case "$GH_FAKE_MODE" in
  auth4)  echo "To get started with GitHub CLI, please run:  gh auth login" >&2; exit 4;;
  bad401) echo "HTTP 401: Bad credentials (https://api.github.com/graphql)" >&2; exit 1;;
  fail)   echo "GraphQL: Could not resolve to a PullRequest" >&2; exit 1;;
  *)      echo '{"number":1,"url":"https://example.test/1","state":"OPEN"}'; exit 0;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_FAKE_MODE", mode)
	t.Setenv("GH_FAKE_LOG", logPath)
	resetForTests()
	t.Cleanup(resetForTests)
	return logPath
}

func callCount(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

func TestGHNotInstalledIsReportedConsistently(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no gh anywhere
	resetForTests()
	t.Cleanup(resetForTests)

	_, err := GetPRInfo("1")
	if err != ErrGHNotInstalled {
		t.Fatalf("GetPRInfo err = %v, want ErrGHNotInstalled", err)
	}
	if !strings.Contains(err.Error(), "'gh' CLI not found") || !IsEnvError(err) {
		t.Fatalf("unexpected message or not an environment error: %v", err)
	}
	// Every kind of call reports the same thing, unwrapped.
	if _, err := CreatePR("t", "b", "main", "x"); err != ErrGHNotInstalled {
		t.Fatalf("CreatePR err = %v, want ErrGHNotInstalled", err)
	}
	if err := UpdatePR(1, "t", "b", "main"); err != ErrGHNotInstalled {
		t.Fatalf("UpdatePR err = %v, want ErrGHNotInstalled", err)
	}
	if err := MergeStack(1); err != ErrGHNotInstalled {
		t.Fatalf("MergeStack err = %v, want ErrGHNotInstalled", err)
	}
	if _, err := OpenPRsBasedOn("b"); err != ErrGHNotInstalled {
		t.Fatalf("OpenPRsBasedOn err = %v, want ErrGHNotInstalled", err)
	}
	if _, err := CreateStack([]int{1, 2}); err != ErrGHNotInstalled {
		t.Fatalf("CreateStack err = %v, want ErrGHNotInstalled", err)
	}
}

func TestGHExitCode4MeansNotAuthenticatedAndIsCached(t *testing.T) {
	log := fakeGH(t, "auth4")

	for i := range 3 {
		_, err := GetPRInfo("1")
		if err != ErrGHNotAuthenticated {
			t.Fatalf("call %d err = %v, want ErrGHNotAuthenticated", i, err)
		}
	}
	if !strings.Contains(ErrGHNotAuthenticated.Error(), "gh auth login") {
		t.Fatalf("message should say to run 'gh auth login': %v", ErrGHNotAuthenticated)
	}
	// The failure is remembered: gh was only spawned for the first call.
	if n := callCount(t, log); n != 1 {
		t.Fatalf("gh was spawned %d times, want 1 (later calls reuse the cached failure)", n)
	}
}

func TestGHHTTP401MeansNotAuthenticated(t *testing.T) {
	fakeGH(t, "bad401")
	if _, err := GetDetailedPRInfo(1); err != ErrGHNotAuthenticated {
		t.Fatalf("err = %v, want ErrGHNotAuthenticated for an HTTP 401", err)
	}
}

func TestGHOtherFailuresAreNotEnvironmentErrors(t *testing.T) {
	log := fakeGH(t, "fail")

	_, err := GetPRInfo("1")
	if err == nil || IsEnvError(err) {
		t.Fatalf("err = %v, want an ordinary (non-environment) error", err)
	}
	if !strings.Contains(err.Error(), "Could not resolve to a PullRequest") {
		t.Fatalf("the error should carry gh's own message: %v", err)
	}
	// An ordinary failure is not cached: the next call runs gh again.
	if _, err := GetPRInfo("2"); err == nil {
		t.Fatal("second call unexpectedly succeeded")
	}
	if n := callCount(t, log); n != 2 {
		t.Fatalf("gh spawned %d times, want 2 (ordinary failures are not cached)", n)
	}
}

func TestEnsureReadyProbesOnceAndIsSkippedAfterASuccess(t *testing.T) {
	t.Run("probes once across many calls", func(t *testing.T) {
		log := fakeGH(t, "ok")
		for range 5 {
			if err := ensureReady(); err != nil {
				t.Fatal(err)
			}
		}
		if n := callCount(t, log); n != 1 {
			t.Fatalf("probe ran %d times, want 1 (sync.Once)", n)
		}
	})
	t.Run("skipped when a gh call already succeeded", func(t *testing.T) {
		log := fakeGH(t, "ok")
		if _, err := GetPRInfo("1"); err != nil {
			t.Fatal(err)
		}
		if err := ensureReady(); err != nil {
			t.Fatal(err)
		}
		if n := callCount(t, log); n != 1 {
			t.Fatalf("gh spawned %d times, want 1 (the earlier success makes the probe unnecessary)", n)
		}
	})
	t.Run("reports an environment failure every time", func(t *testing.T) {
		fakeGH(t, "auth4")
		for range 3 {
			if err := ensureReady(); err != ErrGHNotAuthenticated {
				t.Fatalf("ensureReady err = %v, want ErrGHNotAuthenticated", err)
			}
		}
	})
}

func TestPushBranchRefusesBeforePushingWhenGHIsUnusable(t *testing.T) {
	fakeGH(t, "auth4")
	dir := t.TempDir()
	remote := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(dir, "init", "-q", "--bare", remote)
	run(dir, "init", "-q", "-b", "main", work)
	run(work, "commit", "-q", "--allow-empty", "-m", "c")
	run(work, "remote", "add", "origin", remote)
	t.Chdir(work)

	if _, err := PushBranch("origin", "HEAD", "feature"); err != ErrGHNotAuthenticated {
		t.Fatalf("PushBranch err = %v, want ErrGHNotAuthenticated", err)
	}
	out, err := exec.Command("git", "--git-dir", remote, "branch", "--list").Output()
	if err != nil || strings.TrimSpace(string(out)) != "" {
		t.Fatalf("a branch was pushed despite the environment failure: %q (err %v)", out, err)
	}
}

func TestGHCallsAreSafeConcurrently(t *testing.T) {
	log := fakeGH(t, "ok")
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := GetDetailedPRInfo(1); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent call failed: %v", err)
	}
	if n := callCount(t, log); n != 20 {
		t.Fatalf("gh spawned %d times, want 20", n)
	}
	if !state.verified.Load() {
		t.Fatal("verified should be set after successful calls")
	}
}

func TestPushBranchReportsWhetherAnythingChanged(t *testing.T) {
	fakeGH(t, "ok") // lets ensureReady's probe succeed
	dir := t.TempDir()
	remote := filepath.Join(dir, "origin.git")
	work := filepath.Join(dir, "work")
	run := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(dir, "init", "-q", "--bare", remote)
	run(dir, "init", "-q", "-b", "main", work)
	run(work, "commit", "-q", "--allow-empty", "-m", "one")
	run(work, "remote", "add", "origin", remote)
	t.Chdir(work)

	steps := []struct {
		name       string
		advance    bool
		wantPushed bool
	}{
		{"first push of a new branch changes the remote", false, true},
		{"pushing the identical commit again is a no-op", false, false},
		{"a new commit changes the remote", true, true},
		{"and again identical is a no-op", false, false},
	}
	for _, st := range steps {
		if st.advance {
			run(work, "commit", "-q", "--allow-empty", "-m", "next")
		}
		pushed, err := PushBranch("origin", "HEAD", "feature")
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if pushed != st.wantPushed {
			t.Fatalf("%s: pushed = %v, want %v", st.name, pushed, st.wantPushed)
		}
	}
}
