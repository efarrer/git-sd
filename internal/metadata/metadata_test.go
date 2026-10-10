package metadata

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsBlank(t *testing.T) {
	cases := []struct {
		name string
		cm   CommitMetadata
		want bool
	}{
		{"nothing at all", CommitMetadata{}, true},
		{"only a stale index", CommitMetadata{Index: 5}, true},
		{"assigned to a Stack Set", CommitMetadata{StackSet: "s"}, false},
		{"a sub-commit", CommitMetadata{ParentID: "p", SubRank: "1"}, false},
		{"has a PR number", CommitMetadata{PRNumber: 7}, false},
		{"has a PR URL", CommitMetadata{PRURL: "https://x/pull/7"}, false},
		{"has a branch", CommitMetadata{BranchName: "b"}, false},
		{"has a last-synced fingerprint", CommitMetadata{LastSynced: "abc"}, false},
		{"has a synced fingerprint", CommitMetadata{SyncedFingerprint: "abc"}, false},
		{"was synced into a PR", CommitMetadata{SyncedPR: 3}, false},
	}
	for _, c := range cases {
		if got := c.cm.IsBlank(); got != c.want {
			t.Errorf("%s: IsBlank = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPruneBlank(t *testing.T) {
	m := &Metadata{Commits: map[string]CommitMetadata{
		"ready1":   {Index: 3},                                         // blank: dropped
		"ready2":   {},                                                 // blank: dropped
		"assigned": {StackSet: "s", Index: 0},                          // kept
		"withpr":   {StackSet: "s", PRNumber: 9, PRURL: "u"},           // kept
		"child":    {StackSet: "s", ParentID: "dormant", SubRank: "1"}, // kept
		"dormant":  {Index: 1},                                         // blank but a sub-commit's parent: kept
		"synced":   {SyncedFingerprint: "f", SyncedPR: 2},              // kept
	}}
	if got := m.PruneBlank(); got != 2 {
		t.Errorf("PruneBlank removed %d entries, want 2", got)
	}
	for _, id := range []string{"ready1", "ready2"} {
		if _, ok := m.Commits[id]; ok {
			t.Errorf("%s should have been pruned", id)
		}
	}
	for _, id := range []string{"assigned", "withpr", "child", "dormant", "synced"} {
		if _, ok := m.Commits[id]; !ok {
			t.Errorf("%s must be kept", id)
		}
	}
	if got := m.PruneBlank(); got != 0 {
		t.Errorf("pruning twice removes nothing more, got %d", got)
	}
}

func TestSnapshotSkipsBlankEntries(t *testing.T) {
	m := &Metadata{Commits: map[string]CommitMetadata{
		"ready":    {Index: 2},
		"assigned": {StackSet: "s"},
	}}
	snap := m.Snapshot()
	if _, ok := snap.Commits["ready"]; ok {
		t.Error("a snapshot should not copy blank entries")
	}
	if _, ok := snap.Commits["assigned"]; !ok {
		t.Error("a snapshot must copy entries that carry information")
	}
	if _, ok := m.Commits["ready"]; !ok {
		t.Error("taking a snapshot must not change the live metadata")
	}
}

func TestSaveHealsAFileWithBlankEntries(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })

	// A file as an older version left it: blank entries for commits long gone.
	path := filepath.Join(dir, ".git", "sd", "metadata.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"commits":{"gone1":{"stack_set":"","index":3,"pr_url":"","pr_number":0},"gone2":{"stack_set":"","index":1,"pr_url":"","pr_number":0},"live":{"stack_set":"dep","index":0,"pr_url":"https://x/pull/1","pr_number":1}},"stack_sets":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Commits) != 3 {
		t.Fatalf("test premise: 3 entries loaded, got %d", len(m.Commits))
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	m2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Commits) != 1 || m2.Commits["live"].PRNumber != 1 {
		t.Errorf("after a save only the live entry should remain, got %v", m2.Commits)
	}
}
