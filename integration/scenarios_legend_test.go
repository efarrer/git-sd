package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSDStatusLegendShowsWithNoCommitsAndNoBase checks that --legend always
// works: with no commits to list, and with no base branch to resolve. Local
// only; it never touches GitHub.
func TestSDStatusLegendShowsWithNoCommitsAndNoBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitIn := func(d string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
	initRepo := func(name string, withRemote bool) string {
		repo := filepath.Join(dir, name)
		gitIn(dir, "init", "-q", "-b", "main", repo)
		gitIn(repo, "config", "user.name", "t")
		gitIn(repo, "config", "user.email", "t@example.com")
		if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0644); err != nil {
			t.Fatal(err)
		}
		gitIn(repo, "add", "base.txt")
		gitIn(repo, "commit", "-q", "-m", "base")
		if withRemote {
			remote := filepath.Join(dir, name+"-origin.git")
			gitIn(dir, "init", "-q", "--bare", remote)
			gitIn(repo, "remote", "add", "origin", remote)
			gitIn(repo, "push", "-q", "-u", "origin", "main")
		}
		return repo
	}
	const legendMarker = "PR States:"

	withRemote := initRepo("with-remote", true)
	out, err := runGitSDErr(withRemote, "status", "--legend")
	if err != nil {
		t.Fatalf("status --legend with no commits failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, legendMarker) || !strings.Contains(out, "No local commits found") {
		t.Fatalf("expected the legend and the empty-state message, got:\n%s", out)
	}
	if strings.Index(out, legendMarker) > strings.Index(out, "No local commits found") {
		t.Fatalf("the legend should come before the empty-state message:\n%s", out)
	}
	if out, err := runGitSDErr(withRemote, "status"); err != nil || strings.Contains(out, legendMarker) {
		t.Fatalf("status without --legend must not print the legend (err=%v):\n%s", err, out)
	}
	if out, err := runGitSDErr(withRemote, "status", "--no-legend"); err != nil || strings.Contains(out, legendMarker) {
		t.Fatalf("--no-legend must not print the legend (err=%v):\n%s", err, out)
	}

	noRemote := initRepo("no-remote", false)
	out, err = runGitSDErr(noRemote, "status", "--legend")
	if !strings.Contains(out, legendMarker) {
		t.Fatalf("--legend must print the legend even when the base branch can't be resolved, got:\n%s", out)
	}
	if err == nil {
		t.Fatalf("an unresolvable base branch should still be reported as an error, got:\n%s", out)
	}
	if !strings.Contains(out, "sd.base-branch") {
		t.Fatalf("the base-branch error should follow the legend, got:\n%s", out)
	}
}
