package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDSyncRecoversFromAFailedPRCreateOnGitHub runs against real GitHub with a
// thin gh wrapper that fails ONLY the creation of the middle PR, to see what a
// partial failure really leaves behind (including whether GitHub accepts
// registering a Stack with a gap) and that the next sync repairs it.
func TestSDSyncRecoversFromAFailedPRCreateOnGitHub(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: failed PR create, then recovery ---")
	realGH, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	wrapDir := t.TempDir()
	failFile := filepath.Join(wrapDir, "fail")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"pr create"*"--head gap-commit-2-"*)
    if [ -e %q ]; then echo "GraphQL: simulated failure creating this PR" >&2; exit 1; fi ;;
esac
exec %q "$@"
`, failFile, realGH)
	if err := os.WriteFile(filepath.Join(wrapDir, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failFile, nil, 0644); err != nil {
		t.Fatal(err)
	}
	sd := func(args ...string) (string, error) {
		cmd := exec.Command(gitSDPath, args...)
		cmd.Dir = repoDir
		cmd.Env = append(ghEnv(authorToken), "PATH="+wrapDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	makeCommits(t, repoDir, "gapset", "Gap Commit", 3)
	runGitSD(t, repoDir, "assign", "gapset:0-2")

	// First sync: the middle PR's create fails.
	out, err := sd("sync", "gapset")
	fmt.Printf("FIRST SYNC (middle create fails): %v\n%s\n", err, out)
	if err == nil {
		t.Fatalf("a sync with a failed create must exit non-zero:\n%s", out)
	}
	if got := strings.Count(out, "\nError:") + strings.Count(out[:min(len(out), 6)], "Error:"); got != 1 {
		t.Errorf("exactly one error should be reported (the failed create), got %d:\n%s", got, out)
	}
	if !strings.Contains(out, "Error: could not create a PR for branch gap-commit-2-") || !strings.Contains(out, "Sync completed with 1 error.") {
		t.Errorf("the failed create and the error count should be reported:\n%s", out)
	}
	if strings.Contains(out, "could not register stack") {
		t.Errorf("an incomplete chain must not be registered:\n%s", out)
	}
	status := runGitSD(t, repoDir, "status")
	prs := extractStackPRNumbers(status, "gapset")
	if len(prs) != 2 {
		t.Fatalf("PRs for the first and third commits only, got %v:\n%s", prs, status)
	}
	if got := statusCell(t, status, rowWith(t, status, "Gap Commit 2"), "Status"); got != "Assigned" {
		t.Errorf("the commit whose PR failed is still Assigned, got %q", got)
	}
	if stacks := ghAPI(t, repoDir, "repos/{owner}/{repo}/stacks?pull_request="+prs[0], "--jq", "length"); stacks != "0" {
		t.Errorf("no Stack should exist for an incomplete chain, GitHub has %s", stacks)
	}

	// Recovery: let the create succeed and sync again.
	if err := os.Remove(failFile); err != nil {
		t.Fatal(err)
	}
	out, err = sd("sync", "gapset")
	fmt.Printf("SECOND SYNC (create allowed): %v\n%s\n", err, out)
	if err != nil || strings.Contains(out, "Error") {
		t.Fatalf("the recovery sync must succeed cleanly: %v\n%s", err, out)
	}
	if strings.Count(out, "Created PR") != 1 || !strings.Contains(out, "2 PRs unchanged.") {
		t.Errorf("only the missing PR is created, the other two are left alone:\n%s", out)
	}
	status = runGitSD(t, repoDir, "status")
	all := extractStackPRNumbers(status, "gapset") // in stack order: lower, middle, upper
	if len(all) != 3 {
		t.Fatalf("all three PRs should exist now, got %v:\n%s", all, status)
	}
	stack := ghAPI(t, repoDir, "repos/{owner}/{repo}/stacks?pull_request="+all[0], "--jq", `.[0].pull_requests | map(.number|tostring) | join(",")`)
	if stack != strings.Join(all, ",") {
		t.Errorf("the Stack should list the three PRs in stack order %v, GitHub has %s", all, stack)
	}
	fmt.Println("Failed-create recovery verified on GitHub.")
}
