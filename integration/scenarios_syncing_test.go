package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startSlowSync starts `git sd sync <args>` in the background with the fake gh
// taking delay seconds to create each PR, and waits until the lock file says
// the sync is running. The caller must Wait or Kill the returned command.
func (e *batchPushEnv) startSlowSync(t *testing.T, delay string, args ...string) (*exec.Cmd, string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.root, "pr-counter.delay"), []byte(delay), 0644); err != nil {
		t.Fatal(err)
	}
	started := strings.Count(strings.Join(e.lines(e.ghLog), "\n"), "CREATE_START") // creates from earlier syncs
	cmd := exec.Command(gitSDPath, append([]string{"sync"}, args...)...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(), "PATH="+e.pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(e.repo, ".git", "sd", "operation.json")
	for range 200 {
		// Wait for the slow phase (PR creation): before it sync is still building
		// each PR's branch, which briefly checks out the base commit.
		if _, err := os.Stat(lock); err == nil && strings.Count(strings.Join(e.lines(e.ghLog), "\n"), "CREATE_START") > started {
			return cmd, lock
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Kill()
	cmd.Wait()
	t.Fatal("the sync never wrote its lock file")
	return nil, ""
}

// TestSDStatusShowsSyncingWhileASyncRuns checks the Syncing marker from a second
// process: shown for the affected set while sync is in flight, gone (with the
// lock) when it finishes, and not left behind by a sync that is killed.
func TestSDStatusShowsSyncingWhileASyncRuns(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	// A and B in set "tmp", C in set "other": syncing "tmp" must not touch C.
	if o, err := e.sd("assign", "tmp:0-1", "other:2"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	statusOf := func(msg string) string {
		t.Helper()
		out, err := e.sd("status")
		if err != nil {
			t.Fatalf("status: %v\n%s", err, out)
		}
		return statusCell(t, out, rowWith(t, out, msg), "Status")
	}
	if got := statusOf("Commit A"); got != "Assigned" {
		t.Fatalf("before any sync, A is Assigned, got %q", got)
	}

	// status normally rewrites the metadata (it refreshes each Index) ...
	metaPath := filepath.Join(e.repo, ".git", "sd", "metadata.json")
	mtime := func() time.Time {
		info, err := os.Stat(metaPath)
		if err != nil {
			t.Fatal(err)
		}
		return info.ModTime()
	}
	idleBefore := mtime()
	time.Sleep(1100 * time.Millisecond) // coarse file timestamps
	statusOf("Commit A")
	if !mtime().After(idleBefore) {
		t.Fatalf("test premise: an idle status should rewrite the metadata")
	}

	// A sync of one set, in flight.
	cmd, lock := e.startSlowSync(t, "3", "tmp")
	inFlight := mtime()
	time.Sleep(1100 * time.Millisecond)
	if got := statusOf("Commit A"); got != "Syncing" {
		t.Errorf("A is being synced: want Syncing, got %q", got)
	}
	if got := statusOf("Commit B"); got != "Syncing" {
		t.Errorf("B is being synced: want Syncing, got %q", got)
	}
	if got := statusOf("Commit C"); got != "Assigned" {
		t.Errorf("C is in another set and must not show Syncing, got %q", got)
	}
	// ... but not while a sync runs: its own save must not be overwritten.
	if mtime() != inFlight {
		t.Errorf("status must not write the metadata while a sync is running")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("a finished sync removes its lock file")
	}
	if got := statusOf("Commit A"); got != "Review" {
		t.Errorf("after the sync A shows its PR's state, got %q", got)
	}

	// A sync of every set that is killed mid-way leaves a stale lock: ignored and cleaned.
	cmd, lock = e.startSlowSync(t, "5")
	if got := statusOf("Commit C"); got != "Syncing" {
		t.Errorf("a sync of every set covers C too, got %q", got)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("a killed sync cannot clean up after itself, so the lock should still be there: %v", err)
	}
	if got := statusOf("Commit C"); got == "Syncing" {
		t.Errorf("a killed sync must not leave Syncing behind, got %q", got)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("status should have removed the stale lock")
	}
}
