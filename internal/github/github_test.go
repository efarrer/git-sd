package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeGH points PATH at a directory containing a fake "gh" whose
// behavior is entirely determined by script (a POSIX shell script body) —
// the sacrificial repo can't exercise a real branch-protection rejection
// (private repos need a paid plan for that), so these stub gh's async-merge
// responses instead, to verify MergeStack's own parsing/error-surfacing
// logic directly and deterministically, without any network access.
func withFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatalf("failed to write fake gh: %v", err)
	}
	t.Setenv("PATH", dir)
	resetForTests() // gh's availability is cached per process; start each test clean
	t.Cleanup(resetForTests)
}

func TestOpenPRsBasedOn(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  "pr list --base busy-branch --state open --json number") echo '[{"number":12},{"number":34}]' ;;
  "pr list --base idle-branch --state open --json number") echo '[]' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`)
	got, err := OpenPRsBasedOn("busy-branch")
	if err != nil || len(got) != 2 || got[0] != 12 || got[1] != 34 {
		t.Fatalf("OpenPRsBasedOn(busy-branch) = %v, %v; want [12 34], nil", got, err)
	}
	got, err = OpenPRsBasedOn("idle-branch")
	if err != nil || len(got) != 0 {
		t.Fatalf("OpenPRsBasedOn(idle-branch) = %v, %v; want [], nil", got, err)
	}
	if _, err := OpenPRsBasedOn("other"); err == nil {
		t.Fatal("OpenPRsBasedOn should surface a gh failure as an error")
	}
}

func TestMergeStackMergedImmediately(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  *merge-async*)
    echo '{"status":"merged","details":{"sha":"deadbeef"}}'
    exit 0
    ;;
esac
`)
	if err := MergeStack(42); err != nil {
		t.Fatalf("expected MergeStack to succeed on an immediate 'merged' status, got: %v", err)
	}
}

func TestMergeStackPendingThenMerged(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  *merge-async/*)
    echo '{"status":"merged","details":{"sha":"deadbeef"}}'
    exit 0
    ;;
  *merge-async*)
    echo '{"status":"pending","details":{"uuid":"abc-123"}}'
    exit 0
    ;;
esac
`)
	if err := MergeStack(42); err != nil {
		t.Fatalf("expected MergeStack to succeed after polling to 'merged', got: %v", err)
	}
}

func TestMergeStackFailedSurfacesBranchProtectionMessage(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  *merge-async/*)
    echo '{"status":"failed","details":{"message":"At least 1 approving review is required by reviewers with write access."}}'
    exit 0
    ;;
  *merge-async*)
    echo '{"status":"pending","details":{"uuid":"abc-123"}}'
    exit 0
    ;;
esac
`)
	err := MergeStack(42)
	if err == nil {
		t.Fatal("expected MergeStack to fail when GitHub reports status=failed, but it succeeded")
	}
	if !strings.Contains(err.Error(), "approving review is required") {
		t.Fatalf("expected the branch-protection rejection message to be surfaced, got: %v", err)
	}
}

func TestMergeStackEnqueuedIsTreatedAsError(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  *merge-async*)
    echo '{"status":"enqueued"}'
    exit 0
    ;;
esac
`)
	err := MergeStack(42)
	if err == nil {
		t.Fatal("expected MergeStack to treat 'enqueued' as an error (it hasn't actually landed yet), but it succeeded")
	}
	if !strings.Contains(err.Error(), "merge queue") {
		t.Fatalf("expected the error to mention the merge queue, got: %v", err)
	}
}

func TestMergeStackSubmitRejectedImmediately(t *testing.T) {
	withFakeGH(t, `
case "$*" in
  *merge-async*)
    echo '{"status":"failed","details":{"message":"Pull request is a draft."}}' >&2
    exit 1
    ;;
esac
`)
	err := MergeStack(42)
	if err == nil {
		t.Fatal("expected MergeStack to fail on an immediate 'failed' submit response, but it succeeded")
	}
	if !strings.Contains(err.Error(), "draft") {
		t.Fatalf("expected the draft-PR rejection message to be surfaced, got: %v", err)
	}
}

func TestMergeStackAsyncMergeUnavailableOn404(t *testing.T) {
	withFakeGH(t, `
echo 'gh: Not Found (HTTP 404)' >&2
exit 1
`)
	err := MergeStack(42)
	if !errors.Is(err, ErrAsyncMergeUnavailable) {
		t.Fatalf("expected ErrAsyncMergeUnavailable on a 404, got: %v", err)
	}
}

func TestCreateStackAsyncMergeUnavailableOn404(t *testing.T) {
	withFakeGH(t, `
echo 'gh: Not Found (HTTP 404)' >&2
exit 1
`)
	_, err := CreateStack([]int{1, 2})
	if !errors.Is(err, ErrAsyncMergeUnavailable) {
		t.Fatalf("expected ErrAsyncMergeUnavailable on a 404, got: %v", err)
	}
}

func TestCreateStackGenericFailureIsNotAsyncMergeUnavailable(t *testing.T) {
	withFakeGH(t, `
echo '{"message":"Invalid request.\n\nInvalid property /pull_requests: 2 items required; only 1 was supplied.","status":"422"}' >&2
exit 1
`)
	_, err := CreateStack([]int{1})
	if errors.Is(err, ErrAsyncMergeUnavailable) {
		t.Fatalf("a 422 should not be misreported as ErrAsyncMergeUnavailable, got: %v", err)
	}
	if !strings.Contains(err.Error(), "422") {
		t.Fatalf("expected the real GitHub error to be surfaced, got: %v", err)
	}
}

func TestUnstackAsyncMergeUnavailableOn404(t *testing.T) {
	withFakeGH(t, `
echo 'gh: Not Found (HTTP 404)' >&2
exit 1
`)
	if err := Unstack(7); !errors.Is(err, ErrAsyncMergeUnavailable) {
		t.Fatalf("expected ErrAsyncMergeUnavailable on a 404, got: %v", err)
	}
}
