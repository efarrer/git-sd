package integration

import (
	"os/exec"
	"strings"
	"testing"
)

// ghAPI runs `gh api` in dir as the author and returns trimmed stdout.
func ghAPI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("gh", append([]string{"api"}, args...)...)
	cmd.Dir = dir
	cmd.Env = ghEnv(authorToken)
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("gh api %v: %v\n%s", args, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// TestSDSyncKeepsTheRegisteredStackWhenNothingForcesAChange runs against real
// GitHub: a no-change re-sync and a title edit must leave the Stack registered
// (same Stack number — it would change if it were unstacked and recreated), a
// longer chain must re-register it, and the kept-then-rebuilt Stack must still
// merge.
func TestSDSyncKeepsTheRegisteredStackWhenNothingForcesAChange(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	makeCommits(t, repoDir, "keepstack", "Keep Commit", 3)
	runGitSD(t, repoDir, "assign", "keepstack:0-2")
	runGitSD(t, repoDir, "sync", "keepstack")
	prs := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "keepstack")
	if len(prs) != 3 {
		t.Fatalf("expected 3 PRs, got %v", prs)
	}
	stackOf := func() string {
		return ghAPI(t, repoDir, "repos/{owner}/{repo}/stacks?pull_request="+prs[1], "--jq", ".[0].number")
	}
	first := stackOf()
	if first == "" {
		t.Fatal("the set should be registered as a Stack after the first sync")
	}

	runGitSD(t, repoDir, "sync", "keepstack")
	if got := stackOf(); got != first {
		t.Fatalf("a no-change re-sync must keep the Stack: %s -> %s", first, got)
	}

	// A title change on the top commit edits its PR without touching the base.
	msg := runGit(t, repoDir, "log", "-1", "--format=%B")
	runGit(t, repoDir, "commit", "--amend", "-m", strings.Replace(strings.TrimSpace(msg), "Keep Commit 3", "Keep Commit 3 renamed", 1))
	out := runGitSD(t, repoDir, "sync", "keepstack")
	if strings.Contains(out, "Error") {
		t.Fatalf("sync after a title change reported an error:\n%s", out)
	}
	if got := stackOf(); got != first {
		t.Fatalf("a title change must keep the Stack: %s -> %s", first, got)
	}
	if title := runGit(t, repoDir, "pr", "view", prs[2], "--json", "title", "--jq", ".title"); !strings.Contains(title, "renamed") {
		t.Fatalf("the top PR's title should have been updated, got %q", title)
	}

	// A fourth PR changes the chain: the Stack is re-registered with all four.
	makeCommits(t, repoDir, "keepstack2", "Keep Extra", 1)
	runGitSD(t, repoDir, "assign", "keepstack:3")
	out = runGitSD(t, repoDir, "sync", "keepstack")
	if strings.Contains(out, "Error") {
		t.Fatalf("sync after adding a PR reported an error:\n%s", out)
	}
	count := ghAPI(t, repoDir, "repos/{owner}/{repo}/stacks?pull_request="+prs[1], "--jq", ".[0].pull_requests | length")
	if count != "4" {
		t.Fatalf("the re-registered Stack should hold 4 PRs, got %s", count)
	}

	// And it still merges.
	all := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "keepstack")
	if out, err := runGitSDErr(repoDir, "merge", "keepstack"); err != nil {
		t.Fatalf("merge failed: %v\n%s", err, out)
	}
	for _, n := range all {
		if state := prState(t, repoDir, n); state != "MERGED" {
			t.Fatalf("PR #%s should be MERGED, got %s", n, state)
		}
	}
}
