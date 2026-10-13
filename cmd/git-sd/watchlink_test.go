package main

import (
	"context"
	"encoding/json"
	"errors"
	"git-sd/internal/ui"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// linkSandbox points the watch link at a temp dir and makes pids alive or not.
func linkSandbox(t *testing.T, alive bool) string {
	t.Helper()
	dir := t.TempDir()
	oldDir, oldAlive := linkDir, pidAlive
	linkDir = func() (string, error) { return dir, nil }
	pidAlive = func(int) bool { return alive }
	t.Cleanup(func() { linkDir, pidAlive = oldDir, oldAlive })
	return dir
}

func writePID(t *testing.T, dir string, pid int, started time.Time) {
	t.Helper()
	data, _ := json.Marshal(watchPIDFile{PID: pid, Started: started})
	if err := os.WriteFile(filepath.Join(dir, watchPIDFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestLiveWatchRules(t *testing.T) {
	dir := linkSandbox(t, true)
	if liveWatch(dir) {
		t.Errorf("no watch.pid means no watch")
	}
	writePID(t, dir, 4242, time.Now())
	if !liveWatch(dir) {
		t.Errorf("a record with a live process is a live watch")
	}
	pidAlive = func(int) bool { return false }
	if liveWatch(dir) {
		t.Errorf("a dead process is not a watch (a crashed watch leaves its file behind)")
	}
	pidAlive = func(int) bool { return true }
	writePID(t, dir, 4242, time.Now().Add(-30*24*time.Hour))
	if liveWatch(dir) {
		t.Errorf("an implausibly old record is ignored (pid reuse)")
	}
	for name, content := range map[string]string{"damaged": "{", "empty": "", "no pid": "{}"} {
		os.WriteFile(filepath.Join(dir, watchPIDFileName), []byte(content), 0o644)
		if liveWatch(dir) {
			t.Errorf("a %s record is as good as none", name)
		}
	}
}

func TestSecondWatchIsRefusedAndNamesTheFile(t *testing.T) {
	dir := linkSandbox(t, true)
	release, err := acquireWatch()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, watchPIDFileName)
	if !liveWatch(dir) {
		t.Fatalf("the first watch records itself")
	}

	// Another process holds it: refused, with the pid, the start time and the file to remove.
	writePID(t, dir, 4242, time.Date(2026, 10, 13, 9, 18, 0, 0, time.Local))
	_, err = acquireWatch()
	var running *watchRunningError
	if err == nil || !errors.As(err, &running) {
		t.Fatalf("a second watch is refused, got %v", err)
	}
	for _, want := range []string{"another git sd watch is already running", "pid 4242", "2026-10-13 09:18:00", "remove " + path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message lacks %q:\n%s", want, err)
		}
	}
	if !exists(path) {
		t.Errorf("a refused watch must leave the running watch's file alone")
	}

	// The first one exiting late must not delete the newer record.
	release()
	if !exists(path) {
		t.Errorf("release removes only its own record")
	}
}

func TestUnusableRecordsAreTakenOverAtOnce(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(dir string)
	}{
		{"a crashed watch (dead pid)", func(dir string) { pidAlive = func(int) bool { return false }; writePID(t, dir, 4242, time.Now()) }},
		{"a damaged file", func(dir string) { os.WriteFile(filepath.Join(dir, watchPIDFileName), []byte("{"), 0o644) }},
		{"an empty file", func(dir string) { os.WriteFile(filepath.Join(dir, watchPIDFileName), nil, 0o644) }},
		{"a too old record", func(dir string) { writePID(t, dir, 4242, time.Now().Add(-30*24*time.Hour)) }},
	} {
		dir := linkSandbox(t, true)
		c.setup(dir)
		start := time.Now()
		release, err := acquireWatch()
		if err != nil {
			t.Errorf("%s: should be taken over, got %v", c.name, err)
			continue
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s: taking over needs no waiting", c.name)
		}
		if rec, ok := readWatchPID(dir); !ok || rec.PID != os.Getpid() {
			t.Errorf("%s: the record is now ours: %+v %v", c.name, rec, ok)
		}
		release()
		if exists(filepath.Join(dir, watchPIDFileName)) {
			t.Errorf("%s: released on exit", c.name)
		}
	}
}

func TestOnlyOneOfSeveralSimultaneousWatchesWins(t *testing.T) {
	dir := linkSandbox(t, true)
	// Every contender looks "alive" to the others, so only the atomic create decides.
	var wins, refusals int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := acquireWatch()
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else {
				refusals++
			}
		}()
	}
	wg.Wait()
	if wins != 1 || refusals != 7 {
		t.Errorf("exactly one watch may start: %d won, %d refused", wins, refusals)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("no temporary files are left behind: %v", left)
	}
}

func TestTouchRingsOnlyALiveWatchAndCreatesNothing(t *testing.T) {
	dir := linkSandbox(t, true)
	path := filepath.Join(dir, watchPIDFileName)
	touchWatch() // no watch: nothing to ring, nothing created
	if exists(path) {
		t.Fatalf("ringing with no watch creates nothing")
	}
	release, err := acquireWatch()
	if err != nil {
		t.Fatal(err)
	}
	f := newRingFeed()
	if f.changed() {
		t.Errorf("the watch's own start is not a ring")
	}

	// The doorbell is the modification time; the content is not touched.
	past := time.Now().Add(-time.Hour)
	os.Chtimes(path, past, past)
	f = newRingFeed()
	before, _ := os.ReadFile(path)
	touchWatch()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("touching only changes the modification time")
	}
	if !f.changed() || !f.take() {
		t.Fatalf("a touch is noticed")
	}
	if f.changed() || f.take() {
		t.Errorf("a ring is reported once")
	}

	// A watch that has just stopped and removed its file: the touch is a no-op, not a re-creation.
	release()
	touchWatch()
	if exists(path) {
		t.Errorf("a touch after the watch stopped must not bring the file back")
	}
	if (&ringFeed{}).changed() {
		t.Errorf("a feed with no directory never fires")
	}
	if f.changed() {
		t.Errorf("a removed file is not a ring")
	}
}

func TestRingWatchRingsOncePerCommand(t *testing.T) {
	dir := linkSandbox(t, true)
	if _, err := acquireWatch(); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(dir, watchPIDFileName), past, past)
	f := newRingFeed()
	ringBy = ""
	ringWatch()
	if f.changed() {
		t.Errorf("a command that changed nothing rings nothing")
	}
	ringBy = "sync"
	ringWatch()
	if !f.changed() || ringBy != "" {
		t.Errorf("ringing touches the watch and clears the request: changed=%v ringBy=%q", f.changed(), ringBy)
	}
}

func TestSleepWallWakesEarlyWhenRung(t *testing.T) {
	start := time.Now()
	if !sleepWall(context.Background(), time.Hour, func() bool { return true }, nil) {
		t.Errorf("being woken is not being cancelled: the loop must keep going")
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("a ring wakes it at once, took %v", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepWall(ctx, time.Hour, func() bool { return true }, nil) {
		t.Errorf("a cancelled context still ends the loop")
	}
}

// ------------------------------------------------ the loop when rung

// ringOnce makes the watcher hear one ring before its second poll.
func ringOnce(w *watcher) {
	given := false
	w.rung = func() bool {
		if given {
			return false
		}
		given = true
		return true
	}
}

func TestRingPollsAtOnceAndAnnouncesNormally(t *testing.T) {
	w, out, _, sleeps, rec := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.CIRunning))}, // a real change, perhaps one status has just shown
	}, false)
	calls := 0
	w.rung = func() bool { calls++; return calls == 2 } // rung before the third poll
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Review → CI running") || len(rec.runs) != 1 {
		t.Errorf("a ring is only a poll: changes are announced as usual, even one status just showed:\n%s", out.String())
	}
	if len(*sleeps) != 3 || (*sleeps)[2] != w.minInterval {
		t.Errorf("a ring-triggered poll that finds a change restarts the wait at the minimum, got %v", *sleeps)
	}
}

// Running status must not disturb the schedule: a watch backed off to its
// longest wait stays there, and the extra poll does not double the wait either.
func TestRingThatFindsNothingKeepsTheSchedule(t *testing.T) {
	same := func() watchStep { return watchStep{snap: snapOf(pr(1, ui.Review))} }
	w, _, _, sleeps, _ := harness([]watchStep{same(), same(), same(), same(), same()}, false)
	calls := 0
	w.rung = func() bool { calls++; return calls == 3 } // rung before the fourth poll
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 1s after the baseline, then 2s, 4s (backing off); the ring-triggered poll
	// keeps the 4s instead of resetting to 1s or doubling to 8s; the next
	// ordinary poll doubles it as before.
	want := []time.Duration{1, 2, 4, 4, 8}
	for i := range want {
		want[i] *= time.Second
	}
	if len(*sleeps) != len(want) {
		t.Fatalf("got waits %v, want %v", *sleeps, want)
	}
	for i := range want {
		if (*sleeps)[i] != want[i] {
			t.Errorf("waits %v, want %v", *sleeps, want)
			break
		}
	}
}

func TestHoldWatchIntervalRespectsTheLimits(t *testing.T) {
	min, max := time.Second, 60*time.Second
	for _, c := range []struct {
		current time.Duration
		ci      bool
		want    time.Duration
	}{
		{60 * time.Second, false, 60 * time.Second},
		{30 * time.Second, false, 30 * time.Second},
		{time.Second, false, time.Second},
		{0, false, time.Second},
		{60 * time.Second, true, watchCIMaxInterval}, // CI running caps the wait
	} {
		if got := holdWatchInterval(c.current, c.ci, min, max); got != c.want {
			t.Errorf("holdWatchInterval(%v, ci=%v) = %v, want %v", c.current, c.ci, got, c.want)
		}
	}
}

func TestNoRingMeansTheNormalBackoff(t *testing.T) {
	w, _, _, sleeps, _ := harness([]watchStep{
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.Review))},
		{snap: snapOf(pr(1, ui.Review))},
	}, false)
	w.rung = func() bool { return false }
	if err := w.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 3 || (*sleeps)[2] <= (*sleeps)[1] {
		t.Errorf("without rings the wait grows as before, got %v", *sleeps)
	}
}
