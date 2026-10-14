package main

import (
	"encoding/json"
	"fmt"
	"github.com/efarrer/git-sd/internal/metadata"
	"os"
	"path/filepath"
	"time"
)

// A running `git sd watch` is the only thing that holds <git dir>/sd/watch.pid
// (so the link is per worktree, like the metadata). The file has two jobs:
//
//   - it is the lock that keeps a second watch out: it holds the watch's pid
//     and start time, and a new watch refuses to start while that process is
//     alive;
//   - its modification time is the doorbell: any command that changes something,
//     or shows the user the PRs' state, touches it, and the watch (which looks
//     once a second) runs its normal poll at once instead of waiting out its
//     interval.
//
// With no watch running the commands write nothing.
const (
	watchPIDFileName = "watch.pid"
	// maxWatchAge bounds how long a watch.pid is believed even if its pid is
	// alive, in case the pid was reused after a crash.
	maxWatchAge = 7 * 24 * time.Hour
)

// linkDir is where the file lives; tests replace it.
var linkDir = metadata.Dir

type watchPIDFile struct {
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

// readWatchPID reads the record in dir. A missing, empty or unparseable file
// counts as no record at all.
func readWatchPID(dir string) (watchPIDFile, bool) {
	var f watchPIDFile
	data, err := os.ReadFile(filepath.Join(dir, watchPIDFileName))
	if err != nil || json.Unmarshal(data, &f) != nil || f.PID <= 0 {
		return f, false
	}
	return f, true
}

// liveWatch reports whether a watch is running in the repository whose sd
// directory is dir: a record exists, its process is alive, and it is not
// implausibly old (pid reuse).
func liveWatch(dir string) bool {
	f, ok := readWatchPID(dir)
	return ok && pidAlive(f.PID) && time.Since(f.Started) < maxWatchAge
}

// watchRunningError is returned when another watch already holds watch.pid.
type watchRunningError struct {
	path    string
	pid     int
	started time.Time
}

func (e *watchRunningError) Error() string {
	return fmt.Sprintf("another git sd watch is already running in this repository (pid %d, started %s).\n"+
		"If it is not, remove %s and run 'git sd watch' again.", e.pid, e.started.Format("2006-01-02 15:04:05"), e.path)
}

// acquireWatch takes watch.pid for this process, or fails if another live
// watch holds it. The file appears with its content already in place and is
// never replaced, so two watches starting together cannot both win. A record
// that is missing, empty, unparseable, left by a dead process, or too old is
// as good as none and is taken over. The returned function releases it
// (removing the file only if it is still ours). If the directory cannot be
// written the watch runs without the link (nothing can ring it) rather than
// not at all.
func acquireWatch() (release func(), err error) {
	dir, derr := linkDir()
	if derr != nil || os.MkdirAll(dir, 0o755) != nil {
		return func() {}, nil
	}
	path := filepath.Join(dir, watchPIDFileName)
	data, _ := json.Marshal(watchPIDFile{PID: os.Getpid(), Started: time.Now()})
	for range 5 {
		cerr := createExclusive(path, data)
		if cerr == nil {
			return func() { releaseWatch(dir) }, nil
		}
		if !os.IsExist(cerr) {
			return func() {}, nil
		}
		if rec, ok := readWatchPID(dir); ok && pidAlive(rec.PID) && time.Since(rec.Started) < maxWatchAge {
			return nil, &watchRunningError{path: path, pid: rec.PID, started: rec.Started}
		}
		os.Remove(path) // no usable record: take it over
	}
	return nil, fmt.Errorf("could not take %s; remove it if no watch is running", path)
}

// createExclusive creates path with data in it, failing with an "exists" error
// if it is already there, and never leaving a reader a half-written file: the
// content is written to a temporary file that is then hard-linked into place
// (atomic, and refuses to replace). Where links are not supported it falls
// back to an exclusive create.
func createExclusive(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.%d.tmp", path, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	err := os.Link(tmp, path)
	if err == nil || os.IsExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	f.Close()
	if werr != nil {
		os.Remove(path)
	}
	return werr
}

// releaseWatch removes this process's record, unless a newer watch has
// replaced it.
func releaseWatch(dir string) {
	if rec, ok := readWatchPID(dir); ok && rec.PID == os.Getpid() {
		os.Remove(filepath.Join(dir, watchPIDFileName))
	}
}

// touchWatch rings the doorbell of a running watch. It writes nothing and
// creates nothing: with no watch (or one that has just stopped and removed its
// file) it does nothing at all.
func touchWatch() {
	dir, err := linkDir()
	if err != nil || !liveWatch(dir) {
		return
	}
	now := time.Now()
	os.Chtimes(filepath.Join(dir, watchPIDFileName), now, now)
}

// ringBy is set for the commands that change what `status` shows, so that a
// running watch is rung when they finish.
var ringBy string

// ringWatch rings a running watch if this command changed something. Safe to
// call more than once.
func ringWatch() {
	if ringBy == "" {
		return
	}
	ringBy = ""
	touchWatch()
}

// ringFeed is watch's end of the doorbell: it notices watch.pid being touched
// (one stat per call).
type ringFeed struct {
	path string
	seen fileStamp
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) fileStamp {
	if fi, err := os.Stat(path); err == nil {
		return fileStamp{fi.ModTime(), fi.Size()}
	}
	return fileStamp{}
}

// newRingFeed starts listening now; call it after acquireWatch so the record's
// own creation is not taken for a ring.
func newRingFeed() *ringFeed {
	dir, err := linkDir()
	if err != nil {
		return &ringFeed{}
	}
	path := filepath.Join(dir, watchPIDFileName)
	return &ringFeed{path: path, seen: stampOf(path)}
}

// changed reports whether watch.pid was touched since the last take. Two
// touches inside one coarse modification-time tick count as one.
func (f *ringFeed) changed() bool {
	if f.path == "" {
		return false
	}
	st := stampOf(f.path)
	return st != (fileStamp{}) && st != f.seen
}

// take reports whether it was rung, and re-arms.
func (f *ringFeed) take() bool {
	if !f.changed() {
		return false
	}
	f.seen = stampOf(f.path)
	return true
}
