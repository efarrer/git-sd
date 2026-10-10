package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSDStatusShowsCIStateFromCommitStatuses runs against real GitHub: a commit
// status posted on a PR's head commit (the Commit Status API: no workflow, no
// runner, instant) must show up in `status` as CI running, then CI failed, and
// go away when it succeeds.
func TestSDStatusShowsCIStateFromCommitStatuses(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: CI states from commit statuses ---")
	makeCommits(t, repoDir, "cistate", "CI Commit", 1)
	runGitSD(t, repoDir, "assign", "cistate:0")
	runGitSD(t, repoDir, "sync", "cistate")
	prs := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "cistate")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR, got %v", prs)
	}
	sha := strings.TrimSpace(runGit(t, repoDir, "pr", "view", prs[0], "--json", "headRefOid", "--jq", ".headRefOid"))
	if len(sha) < 40 {
		t.Fatalf("could not read the PR's head commit: %q", sha)
	}

	// GitHub can take a moment to reflect a new status in the PR's rollup.
	expect := func(want, what string) {
		t.Helper()
		var out, got string
		for range 10 {
			out = runGitSD(t, repoDir, "status")
			got = statusCell(t, out, rowWith(t, out, "CI Commit 1"), "Status")
			if got == want {
				return
			}
			time.Sleep(3 * time.Second)
		}
		t.Fatalf("%s: expected Status %q, got %q:\n%s", what, want, got, out)
	}
	post := func(state string) {
		t.Helper()
		ghAPI(t, repoDir, "--method", "POST", "repos/{owner}/{repo}/statuses/"+sha,
			"-f", "state="+state, "-f", "context=git-sd/ci-test", "-f", "description=git-sd integration test")
	}

	expect("Review", "a PR with no checks")
	post("pending")
	expect("CI running", "a pending commit status")
	post("failure")
	expect("CI failed", "a failing commit status")
	out := runGitSD(t, repoDir, "status")
	if icon := statusCell(t, out, rowWith(t, out, "CI Commit 1"), "Icon"); !strings.Contains(icon, "🚨") {
		t.Errorf("a failing CI shows the 🚨 icon, got %q", icon)
	}
	post("success")
	expect("Review", "a commit status that then succeeded")
	fmt.Println("CI states verified: Review -> CI running -> CI failed -> Review.")
}
