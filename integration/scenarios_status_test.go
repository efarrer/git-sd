package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestSDStatusShowsPRStateMarkers checks the status marker for each stage of a
// real PR's life: no PR yet (Assigned), freshly synced and unreviewed (Review
// Required, not Assigned), approved, and — where the repository's plan
// supports drafts — a draft, which wins over approved.
func TestSDStatusShowsPRStateMarkers(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: PR State Markers ---")
	makeCommits(t, repoDir, "markers", "Marker Commit", 1)
	runGitSD(t, repoDir, "assign", "markers:0")

	const (
		assigned = "\U0001F4E6" // box
		review   = "\U0001F440" // eyes
		approved = "\u2705"
		draft    = "\U0001F6A7"
	)
	// GitHub can take a moment to reflect a review or draft change, so poll.
	expect := func(want, doNotWant, what string) {
		t.Helper()
		var line string
		for range 6 {
			for _, l := range strings.Split(runGitSD(t, repoDir, "status"), "\n") {
				if strings.Contains(l, "Marker Commit 1") {
					line = l
				}
			}
			if strings.Contains(line, want) && (doNotWant == "" || !strings.Contains(line, doNotWant)) {
				return
			}
			time.Sleep(2 * time.Second)
		}
		t.Fatalf("%s: expected marker %q (and not %q), status line:\n%s", what, want, doNotWant, line)
	}

	expect(assigned, "", "a commit with no PR yet")

	runGitSD(t, repoDir, "sync", "markers")
	prs := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "markers")
	if len(prs) != 1 {
		t.Fatalf("expected 1 PR after sync, got %v", prs)
	}
	expect(review, assigned, "a freshly synced, unreviewed PR")

	if out, err := approveAsReviewer(repoDir, prs[0]); err != nil {
		t.Logf("no reviewer account available, stopping after Review Required: %v\n%s", err, out)
		return
	}
	expect(approved, review, "an approved PR")

	if out, err := runGitErr(repoDir, "pr", "ready", prs[0], "--undo"); err != nil {
		t.Logf("drafts are not available on this repository's plan, skipping the draft check: %v\n%s", err, out)
		return
	}
	expect(draft, approved, "an approved PR converted to a draft")
	fmt.Println("PR state markers verified: assigned, review required, approved, draft.")
}
