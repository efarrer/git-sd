package main

import (
	"encoding/json"
	"errors"
	"git-sd/internal/metadata"
	"git-sd/internal/ui"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// operation is a `sync` or `merge` that is running in this repository, written
// to <git dir>/sd/operation.json for `status` to read from another terminal.
type operation struct {
	Command string    `json:"command"`       // "sync" or "merge"
	PID     int       `json:"pid"`           // the running process
	Set     string    `json:"set,omitempty"` // the Stack Set being processed; empty means every set
	Started time.Time `json:"started"`
}

const (
	operationFileName = "operation.json"
	// maxOperationAge bounds how long a lock is believed even if its PID is
	// alive, in case the PID was reused after a crash. A merge waits at most
	// 5 minutes for GitHub, so two hours is far beyond any real run.
	maxOperationAge = 2 * time.Hour
)

// pidAlive reports whether a process exists (signal 0 delivers nothing). A
// process owned by someone else counts as alive. Tests replace it.
var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// heldOperation is the lock file this process wrote, if any.
var heldOperation string

func operationPath() (string, error) {
	dir, err := metadata.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, operationFileName), nil
}

// beginOperation records that this process is running command for set ("" =
// every set). It is best effort: failing to write the file only means `status`
// won't show Syncing, never that the command fails.
func beginOperation(command, set string) {
	path, err := operationPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(operation{Command: command, PID: os.Getpid(), Set: set, Started: time.Now()})
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if os.WriteFile(path, data, 0o644) == nil {
		heldOperation = path
	}
}

// endOperation removes the lock this process wrote, unless another process has
// replaced it since.
func endOperation() {
	if heldOperation == "" {
		return
	}
	path := heldOperation
	heldOperation = ""
	if op, err := readOperation(path); err == nil && op.PID == os.Getpid() {
		os.Remove(path)
	}
}

// exitCleanly is os.Exit that first releases the lock (os.Exit skips defers).
func exitCleanly(code int) {
	endOperation()
	os.Exit(code)
}

func readOperation(path string) (*operation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var op operation
	if err := json.Unmarshal(data, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// activeOperation returns the sync or merge running in this repository right
// now, or nil. A lock that can't be read, whose process is gone (killed or
// crashed), or that is too old is stale: it is ignored and removed.
func activeOperation() *operation {
	path, err := operationPath()
	if err != nil {
		return nil
	}
	op, err := readOperation(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			os.Remove(path) // unreadable or corrupt
		}
		return nil
	}
	if !pidAlive(op.PID) || time.Since(op.Started) > maxOperationAge {
		os.Remove(path)
		return nil
	}
	return op
}

// affects reports whether the operation is processing stackSet.
func (o *operation) affects(stackSet string) bool {
	return o != nil && stackSet != "" && (o.Set == "" || o.Set == stackSet)
}

// withOperation is the marker a commit of an assigned Stack Set shows while an
// operation is processing its set: Syncing, except that a PR already known to
// be merged or closed, or whose state couldn't be read, keeps that marker.
func withOperation(id ui.MarkerID, syncing bool) ui.MarkerID {
	if !syncing {
		return id
	}
	switch id {
	case ui.Merged, ui.Closed, ui.Unavailable:
		return id
	}
	return ui.Syncing
}
