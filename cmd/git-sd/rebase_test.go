package main

import (
	"git-sd/internal/git"
	"git-sd/internal/github"
	"git-sd/internal/metadata"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func gitT(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, file, content, msg string) git.Commit {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	gitT(t, "add", file)
	gitT(t, "commit", "-q", "-m", msg)
	hash := gitT(t, "rev-parse", "HEAD")
	return git.Commit{Hash: hash, Message: gitT(t, "log", "-1", "--format=%B", hash) + "\n"}
}

func initRepo(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	gitT(t, "init", "-q", "-b", "main")
	gitT(t, "config", "user.name", "git-sd-test")
	gitT(t, "config", "user.email", "git-sd-test@example.com")
	commitFile(t, "a.txt", "line1\nline2\nline3\n", "base")
	return gitT(t, "rev-parse", "HEAD")
}

func mergedInfo(base, mergeCommit string) *github.PRInfo {
	info := &github.PRInfo{State: "MERGED", BaseRefName: base}
	info.MergeCommit = &struct {
		OID string `json:"oid"`
	}{OID: mergeCommit}
	return info
}

func trackedCommit(t *testing.T, file, content, msg, id string) git.Commit {
	t.Helper()
	return commitFile(t, file, content, msg+"\n\ngit-sd-id: "+id)
}

// TestRebaseOntoRetiresAndRollsBack drops a landed commit, checks it was
// retired (metadata gone, branch queued for sync), then rolls back and
// checks HEAD and metadata both came back.
func TestRebaseOntoRetiresAndRollsBack(t *testing.T) {
	base := initRepo(t)
	gitT(t, "checkout", "-q", "-b", "upstream", base)
	commitFile(t, "landed.txt", "landed\n", "landed upstream")
	target := gitT(t, "rev-parse", "HEAD")

	gitT(t, "checkout", "-q", "-b", "work", base)
	landed := trackedCommit(t, "landed.txt", "landed\n", "landed", "saaaa")
	open := trackedCommit(t, "open.txt", "open\n", "open", "sbbbb")
	origHead := open.Hash

	m := &metadata.Metadata{
		Commits: map[string]metadata.CommitMetadata{
			"saaaa": {StackSet: "set", PRNumber: 1, BranchName: "landed-saaaa"},
			"sbbbb": {StackSet: "set", PRNumber: 2, BranchName: "open-sbbbb"},
		},
		StackSets: map[string]metadata.StackSetMetadata{"set": {StackNumber: 7}},
	}
	snap, err := takeSnapshot(m)
	if err != nil {
		t.Fatal(err)
	}
	retired, err := rebaseOnto(m, snap, target, []git.Commit{landed, open}, map[string]bool{landed.Hash: true}, nil, nil)
	if err != nil {
		t.Fatalf("rebaseOnto: %v", err)
	}
	if retired != 1 {
		t.Fatalf("retired = %d, want 1", retired)
	}
	if _, ok := m.Commits["saaaa"]; ok {
		t.Fatal("landed commit's metadata was not retired")
	}
	if _, ok := m.Commits["sbbbb"]; !ok {
		t.Fatal("still-open commit's metadata was retired (B-018)")
	}
	if len(m.PendingBranchDeletions) != 1 || m.PendingBranchDeletions[0].Branch != "landed-saaaa" {
		t.Fatalf("PendingBranchDeletions = %+v, want landed-saaaa queued", m.PendingBranchDeletions)
	}
	if got := gitT(t, "rev-parse", "HEAD~1"); got != target {
		t.Fatalf("HEAD~1 = %s, want target %s", got, target)
	}
	if got := gitT(t, "rev-parse", snap.Ref); got != origHead {
		t.Fatalf("backup ref = %s, want %s", got, origHead)
	}

	if err := restoreBackup(m, len(m.RebaseBackups)-1); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}
	if got := gitT(t, "rev-parse", "HEAD"); got != origHead {
		t.Fatalf("HEAD after rollback = %s, want %s", got, origHead)
	}
	if _, ok := m.Commits["saaaa"]; !ok || len(m.PendingBranchDeletions) != 0 || m.StackSets["set"].StackNumber != 7 {
		t.Fatalf("metadata not restored: %+v", m)
	}
	if len(m.RebaseBackups) != 0 {
		t.Fatalf("consumed backup still listed: %+v", m.RebaseBackups)
	}
	if _, err := git.RevParse(snap.Ref); err == nil {
		t.Fatal("consumed backup's ref still exists")
	}

	// The on-disk copy must match too — rollback is only safe if it's saved.
	onDisk, err := metadata.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk.Commits["saaaa"]; !ok || len(onDisk.RebaseBackups) != 0 {
		t.Fatalf("saved metadata not restored: %+v", onDisk)
	}
}

func TestBackupsArePruned(t *testing.T) {
	initRepo(t)
	m := &metadata.Metadata{Commits: map[string]metadata.CommitMetadata{}, StackSets: map[string]metadata.StackSetMetadata{}}
	head := gitT(t, "rev-parse", "HEAD")
	var first metadata.RebaseBackup
	for i := range maxRebaseBackups + 2 {
		snap, err := takeSnapshot(m)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = snap
		}
		if _, err := rebaseOnto(m, snap, head, nil, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.RebaseBackups) != maxRebaseBackups {
		t.Fatalf("len(RebaseBackups) = %d, want %d", len(m.RebaseBackups), maxRebaseBackups)
	}
	if _, err := git.RevParse(first.Ref); err == nil {
		t.Fatal("pruned backup's ref still exists")
	}
}
