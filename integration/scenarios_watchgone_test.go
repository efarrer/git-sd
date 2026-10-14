package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSDWatchAnnouncesAMergeItMissedWhenAPRLeavesTheBranch: with a 30 s interval
// the watch cannot poll again on its own, so when a PR is merged on GitHub and its
// commit leaves the Stack Set (here by `assign :<index>`, which rings the watch,
// the same way `rebase` dropping a landed commit does) the PR vanishes from the
// view without the watch ever having seen it Merged. The watch must look it up
// once and announce `Review → Merged` from the state it knew, once, with the
// notification command run once, and must say nothing about the removal.
func TestSDWatchAnnouncesAMergeItMissedWhenAPRLeavesTheBranch(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	e.commitFile("Commit_A.txt", "Commit A")
	e.commitFile("Commit_B.txt", "Commit B")
	if o, err := e.sd("assign", "gone:0-1"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	st, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, st)
	}
	prs := extractStackPRNumbers(st, "gone")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %v", prs)
	}
	second := prs[1]
	idxB := statusIndex(t, st, rowWith(t, st, "Commit B"))
	notified := filepath.Join(e.root, "notified.log")
	e.git(e.repo, "config", "sd.watch.min-interval", "30s")
	e.git(e.repo, "config", "sd.watch.max-interval", "60s")
	e.git(e.repo, "config", "sd.watch.notify-command", `echo "$GIT_SD_EVENT|$GIT_SD_PR_NUMBER|$GIT_SD_FROM -> $GIT_SD_TO" >> `+notified)

	outPath := filepath.Join(e.root, "watch.out")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gitSDPath, "watch")
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(), "PATH="+e.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Stdout, cmd.Stderr = outFile, outFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	read := func(path string) string { data, _ := os.ReadFile(path); return string(data) }
	waitFor := func(path, needle string) {
		t.Helper()
		for range 200 {
			if strings.Contains(read(path), needle) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in %s:\n%s", needle, filepath.Base(path), read(path))
	}
	waitFor(outPath, "/pull/"+second) // the baseline table: the watch knows PR 2 as Review

	// GitHub merges PR 2; before the watch's next poll its commit leaves the Stack Set.
	body := `{"number":` + second + `,"url":"https://github.com/o/r/pull/` + second + `","state":"MERGED","isDraft":false,"reviewDecision":"","mergeable":"UNKNOWN","mergeStateStatus":"UNKNOWN","statusCheckRollup":[]}`
	if err := os.WriteFile(filepath.Join(e.root, "pr-counter-pr-"+second+".json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if o, err := e.sd("assign", ":"+idxB); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	waitFor(outPath, "gone 2/2  PR #"+second+"  Review → Merged")
	waitFor(notified, "merged|"+second+"|Review -> Merged")

	time.Sleep(3 * time.Second) // nothing more may follow
	if got := strings.Count(read(outPath), "→"); got != 1 {
		t.Errorf("exactly one change is announced (the merge, not the removal), got %d:\n%s", got, read(outPath))
	}
	if got := strings.Count(read(notified), "\n"); got != 1 {
		t.Errorf("the notification command runs once, got %d:\n%s", got, read(notified))
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("stopping the watch: %v\n%s", err, read(outPath))
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the watch did not stop on SIGINT:\n%s", read(outPath))
	}
}
