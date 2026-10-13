package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSDWatchIsALockAndADoorbell runs real `git sd watch` processes against the
// fake gh with an interval so long (30 s) that a poll can only come early if
// another command rings the watch by touching .git/sd/watch.pid: `status` and
// `sync` must make it check at once, a second watch must be refused (naming the
// file), a stale record must be taken over, and nothing is left behind.
func TestSDWatchIsALockAndADoorbell(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	e.commitFile("Commit_A.txt", "Commit A")
	e.commitFile("Commit_B.txt", "Commit B")
	if o, err := e.sd("assign", "lnk:0-1"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	st, err := e.sd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, st)
	}
	prs := extractStackPRNumbers(st, "lnk")
	if len(prs) != 2 {
		t.Fatalf("expected 2 PRs, got %v", prs)
	}
	second := prs[1]
	e.git(e.repo, "config", "sd.watch.min-interval", "30s")
	e.git(e.repo, "config", "sd.watch.max-interval", "60s")
	pidPath := filepath.Join(e.repo, ".git", "sd", "watch.pid")

	// With no watch running, status leaves no watch.pid behind.
	if o, err := e.sd("status"); err != nil {
		t.Fatalf("status: %v\n%s", err, o)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("with no watch running nothing is written, got %v", err)
	}

	setState := func(rollup, decision string) {
		t.Helper()
		body := `{"number":` + second + `,"url":"https://github.com/o/r/pull/` + second + `","state":"OPEN","isDraft":false,"reviewDecision":"` + decision + `","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","statusCheckRollup":` + rollup + `}`
		if err := os.WriteFile(filepath.Join(e.root, "pr-counter-pr-"+second+".json"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// startWatch runs `git sd watch`, returning a way to read its output and one to stop it.
	startWatch := func(name string) (read func() string, stop func() error) {
		t.Helper()
		outPath := filepath.Join(e.root, name+".out")
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
		read = func() string { data, _ := os.ReadFile(outPath); return string(data) }
		stop = func() error {
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				return err
			}
			select {
			case err := <-finished:
				return err
			case <-time.After(10 * time.Second):
				return fmt.Errorf("the watch did not stop on SIGINT:\n%s", read())
			}
		}
		return read, stop
	}
	waitFor := func(read func() string, what string, ok func(out string) bool) {
		t.Helper()
		for range 200 {
			if ok(read()) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s:\n%s", what, read())
	}

	read, stop := startWatch("first")
	waitFor(read, "the baseline table", func(o string) bool { return strings.Contains(o, "/pull/"+second) })
	waitFor(read, "watch.pid", func(string) bool { _, err := os.Stat(pidPath); return err == nil })

	// A second watch in the same repository is refused, and names the file.
	out, err := e.sd("watch")
	if err == nil || !strings.Contains(out, "another git sd watch is already running in this repository") ||
		!strings.Contains(out, "remove ") || !strings.Contains(out, filepath.Join(".git", "sd", "watch.pid")) {
		t.Fatalf("a second watch must be refused, naming the file: %v\n%s", err, out)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("a refused watch must not remove the running watch's file: %v", err)
	}

	// status rings the doorbell: the watch polls at once (its own wait is 30 s)
	// and announces the change in the usual way.
	setState(`[{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""}]`, "")
	if o, err := e.sd("status"); err != nil || !strings.Contains(o, "CI running") {
		t.Fatalf("status should show CI running: %v\n%s", err, o)
	}
	waitFor(read, "the change announced after status rang", func(o string) bool {
		return strings.Contains(o, "lnk 2/2  PR #"+second+"  Review → CI running")
	})

	// sync (which changes nothing here) rings it too.
	setState(`[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]`, "APPROVED")
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	waitFor(read, "the change announced after sync rang", func(o string) bool {
		return strings.Contains(o, "lnk 2/2  PR #"+second+"  CI running → Approved")
	})

	// Stopping removes watch.pid; afterwards commands write nothing again.
	if err := stop(); err != nil {
		t.Fatalf("a watch stopped with Ctrl-C exits 0, got %v\n%s", err, read())
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("watch.pid is removed when the watch stops: %v", err)
	}
	if o, err := e.sd("status"); err != nil {
		t.Fatalf("status: %v\n%s", err, o)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("with the watch gone, status must not bring watch.pid back: %v", err)
	}

	// A record left by a watch that crashed (its process is gone) is taken over.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	stale := fmt.Sprintf(`{"pid":%d,"started":"%s"}`, dead.Process.Pid, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(pidPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	read, stop = startWatch("second")
	waitFor(read, "a watch that took over the stale record", func(o string) bool { return strings.Contains(o, "/pull/"+second) })
	if data, _ := os.ReadFile(pidPath); strings.Contains(string(data), fmt.Sprintf(`"pid":%d,`, dead.Process.Pid)) {
		t.Errorf("the record now names the new watch, not the dead one: %s", data)
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v\n%s", err, read())
	}
}
