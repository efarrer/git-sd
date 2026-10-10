package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSDStatusShowsAConflictGitHubReports runs against real GitHub. Two stacked
// PRs add the same file; then the lower PR's branch is force-pushed with a
// competing version of that file, so the upper PR (whose base is that branch)
// can no longer be merged into it. GitHub reports the conflict and `status`
// shows it; the lower PR is unaffected.
func TestSDStatusShowsAConflictGitHubReports(t *testing.T) {
	t.Parallel()
	repoDir := newTestRepo(t)
	fmt.Println("--- Running Scenario: Conflict reported by GitHub ---")
	write := func(dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "conflict.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(repoDir, "version from the lower commit\n")
	runGit(t, repoDir, "add", "conflict.txt")
	runGit(t, repoDir, "commit", "-m", "Conflict Lower")
	write(repoDir, "version from the upper commit\n")
	runGit(t, repoDir, "add", "conflict.txt")
	runGit(t, repoDir, "commit", "-m", "Conflict Upper")
	runGitSD(t, repoDir, "assign", "conflictset:0-1")
	runGitSD(t, repoDir, "sync", "conflictset")
	prs := extractStackPRNumbers(runGitSD(t, repoDir, "status"), "conflictset")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %v", prs)
	}
	lowerBranch := strings.TrimSpace(prHeadRef(t, repoDir, prs[0]))

	// Replace the lower branch with a competing version of the same file.
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoDir, "fetch", "-q", "origin", "main")
	runGit(t, repoDir, "worktree", "add", "-q", "--detach", wt, "origin/main")
	write(wt, "a competing version\n")
	runGit(t, wt, "add", "conflict.txt")
	runGit(t, wt, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "competing change")
	runGit(t, wt, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+lowerBranch)

	// GitHub works out mergeability in the background; give it time.
	var out, upper, lower string
	for range 15 {
		out = runGitSD(t, repoDir, "status")
		upper = statusCell(t, out, rowWith(t, out, "Conflict Upper"), "Status")
		lower = statusCell(t, out, rowWith(t, out, "Conflict Lower"), "Status")
		if upper == "Conflict" {
			break
		}
		time.Sleep(4 * time.Second)
	}
	if upper != "Conflict" {
		t.Fatalf("the upper PR should show Conflict, got %q:\n%s", upper, out)
	}
	if icon := statusCell(t, out, rowWith(t, out, "Conflict Upper"), "Icon"); !strings.Contains(icon, "\U0001F4A5") {
		t.Errorf("a conflict shows the 💥 icon, got %q", icon)
	}
	if lower == "Conflict" {
		t.Errorf("the lower PR is not in conflict:\n%s", out)
	}
	fmt.Println("Conflict verified: the upper PR shows Conflict, the lower one does not.")
}
