package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitSDPath is the git-sd binary built once by TestMain, shared (read-only) by
// every test. testRepo is the sacrificial repo spec (from GIT_SD_TEST_REPO)
// that each test clones its own isolated copy of via newTestRepo, so scenarios
// can run in parallel without sharing local state. repoMirror is a local
// --mirror clone of testRepo, built once in TestMain, that newTestRepo clones
// use as a --reference to avoid re-fetching objects from GitHub on every
// parallel test's clone.
//
// authorToken/reviewerToken (from GIT_SD_TEST_AUTHOR_TOKEN/GIT_SD_TEST_REVIEWER_TOKEN)
// are optional: read once here, then only ever passed on a specific
// exec.Cmd's Env (see ghEnv in helpers_test.go) — never written back into
// this process's own environment, so they stay safe under t.Parallel(). If
// authorToken is empty, every author-side call falls back to the ambient `gh
// auth login` session, exactly as before these existed. reviewerToken is
// required only by the two-account scenario(s), which skip cleanly if it's
// unset — GitHub forbids self-approval, so a second real identity is the
// only way to exercise "a PR gets approved by someone else."
var (
	gitSDPath     string
	testRepo      string
	repoMirror    string
	authorToken   string
	reviewerToken string
)

// TestMain builds git-sd, sanitizes the sacrificial repo's shared remote once
// (via a throwaway clone), runs the scenarios, and tears everything down. If
// GIT_SD_TEST_REPO is unset, the whole suite is skipped cleanly (exit 0) rather
// than failing.
func TestMain(m *testing.M) {
	// .env (see .env.example) is an optional convenience for local runs —
	// real environment variables always take precedence over it.
	dotEnv, err := dotEnvValues()
	if err != nil {
		fmt.Printf("Error reading .env: %v\n", err)
		os.Exit(1)
	}

	testRepo = getTestEnv(dotEnv, "GIT_SD_TEST_REPO")
	if testRepo == "" {
		fmt.Println("Skipping git-sd integration tests: GIT_SD_TEST_REPO not set.")
		os.Exit(0)
	}
	authorToken = getTestEnv(dotEnv, "GIT_SD_TEST_AUTHOR_TOKEN")
	reviewerToken = getTestEnv(dotEnv, "GIT_SD_TEST_REVIEWER_TOKEN")

	// Build the git-sd binary once into a temp dir, using the module import
	// path so the current working directory doesn't matter.
	binDir, err := os.MkdirTemp("", "git-sd-bin-*")
	if err != nil {
		fmt.Printf("Error creating temp bin dir: %v\n", err)
		os.Exit(1)
	}
	gitSDPath = filepath.Join(binDir, "git-sd")
	if out, err := exec.Command("go", "build", "-o", gitSDPath, "git-sd/cmd/git-sd").CombinedOutput(); err != nil {
		fmt.Printf("Error building git-sd binary: %v\n%s\n", err, string(out))
		os.RemoveAll(binDir)
		os.Exit(1)
	}

	// One global sanitize of the shared remote, before any test's clone is
	// live: clone into a throwaway dir, sweep crash-leftovers, discard it.
	// Tests don't reliably tear down their own PRs/branches, so this sweeps
	// leftovers from previous (including crashed/interrupted) runs.
	tmpDir, err := os.MkdirTemp("", "git-sd-sanitize-*")
	if err != nil {
		fmt.Printf("Error creating temp dir: %v\n", err)
		os.RemoveAll(binDir)
		os.Exit(1)
	}

	fmt.Printf("Cloning test repository into %s for sanitization...\n", tmpDir)
	cloneCmd := exec.Command("gh", "repo", "clone", testRepo, tmpDir)
	cloneCmd.Env = ghEnv(authorToken)
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		fmt.Printf("Error cloning repo: %v\n%s\n", err, string(out))
		os.RemoveAll(binDir)
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}

	markerFile := filepath.Join(tmpDir, ".git-sd-test-marker")
	if _, err := os.Stat(markerFile); os.IsNotExist(err) {
		fmt.Printf("Error: Safety marker file %s not found in test repository. Aborting to prevent data loss.\n", markerFile)
		os.RemoveAll(binDir)
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}
	fmt.Println("Safety check passed.")

	if err := sanitizeRepo(tmpDir); err != nil {
		fmt.Printf("Error sanitizing test repository: %v\n", err)
		os.RemoveAll(binDir)
		os.RemoveAll(tmpDir)
		os.Exit(1)
	}
	os.RemoveAll(tmpDir)

	// A local --mirror clone of testRepo that newTestRepo's per-test clones
	// borrow objects from (via `git clone --reference`), so parallel test
	// clones don't each re-fetch the same object graph over the network.
	// Each test still gets its own independent .git config, remotes, and
	// refs from its own `gh repo clone` — only the object store is shared,
	// so cross-test isolation is unaffected.
	mirrorDir, err := os.MkdirTemp("", "git-sd-mirror-*")
	if err != nil {
		fmt.Printf("Error creating mirror dir: %v\n", err)
		os.RemoveAll(binDir)
		os.Exit(1)
	}
	fmt.Printf("Creating local mirror of %s at %s...\n", testRepo, mirrorDir)
	mirrorCmd := exec.Command("gh", "repo", "clone", testRepo, mirrorDir, "--", "--mirror")
	mirrorCmd.Env = ghEnv(authorToken)
	if out, err := mirrorCmd.CombinedOutput(); err != nil {
		fmt.Printf("Error mirroring repo: %v\n%s\n", err, string(out))
		os.RemoveAll(binDir)
		os.RemoveAll(mirrorDir)
		os.Exit(1)
	}
	repoMirror = mirrorDir

	fmt.Println("Running integration tests...")
	code := m.Run()

	os.RemoveAll(mirrorDir)
	os.RemoveAll(binDir)
	os.Exit(code)
}
