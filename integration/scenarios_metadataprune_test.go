package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (e *batchPushEnv) readMetadata(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(e.repo, ".git", "sd", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	return top
}

func (e *batchPushEnv) metadataCommits(t *testing.T) map[string]map[string]any {
	t.Helper()
	var commits map[string]map[string]any
	if err := json.Unmarshal(e.readMetadata(t)["commits"], &commits); err != nil {
		t.Fatal(err)
	}
	return commits
}

// TestSDMetadataKeepsNoEntriesForReadyCommits covers B-022: `status` must not
// create entries for tracked-but-unassigned commits, and entries left behind by
// older versions (for commits that are gone) must be cleaned up by the next
// command that saves, without touching entries that carry information.
func TestSDMetadataKeepsNoEntriesForReadyCommits(t *testing.T) {
	t.Parallel()
	e := newBatchPushEnv(t)
	for _, c := range []string{"Commit A", "Commit B", "Commit C"} {
		e.commitFile(strings.ReplaceAll(c, " ", "_")+".txt", c)
	}
	if o, err := e.sd("track"); err != nil {
		t.Fatalf("track: %v\n%s", err, o)
	}
	if o, err := e.sd("status"); err != nil {
		t.Fatalf("status: %v\n%s", err, o)
	}
	if commits := e.metadataCommits(t); len(commits) != 0 {
		t.Errorf("status must not create entries for Ready commits, got %v", commits)
	}

	// Assign one commit and give it a PR: that entry is real and must survive.
	if o, err := e.sd("assign", "tmp:0"); err != nil {
		t.Fatalf("assign: %v\n%s", err, o)
	}
	if o, err := e.sd("sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, o)
	}
	commits := e.metadataCommits(t)
	if len(commits) != 1 {
		t.Fatalf("only the assigned commit has an entry, got %v", commits)
	}
	var assignedID string
	for id, cm := range commits {
		assignedID = id
		if cm["stack_set"] != "tmp" || cm["pr_number"] == nil || cm["pr_number"].(float64) == 0 {
			t.Errorf("the assigned commit's entry should be intact: %v", cm)
		}
	}

	// A file as an older version left it: blank entries for commits long gone.
	top := e.readMetadata(t)
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(top["commits"], &legacy); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sgone1", "sgone2", "sgone3"} {
		legacy[id] = json.RawMessage(`{"stack_set":"","index":4,"pr_url":"","pr_number":0}`)
	}
	top["commits"], _ = json.Marshal(legacy)
	data, _ := json.MarshalIndent(top, "", "  ")
	if err := os.WriteFile(filepath.Join(e.repo, ".git", "sd", "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if got := len(e.metadataCommits(t)); got != 4 {
		t.Fatalf("test premise: 4 entries after seeding, got %d", got)
	}

	if o, err := e.sd("status"); err != nil {
		t.Fatalf("status: %v\n%s", err, o)
	}
	commits = e.metadataCommits(t)
	if len(commits) != 1 {
		t.Errorf("the blank leftovers should be gone after one status, got %v", commits)
	}
	if cm, ok := commits[assignedID]; !ok || cm["stack_set"] != "tmp" {
		t.Errorf("the assigned commit's entry must survive the cleanup: %v", commits)
	}

	// And the commits are still usable: assign another one afterwards.
	if o, err := e.sd("assign", "tmp:1"); err != nil {
		t.Fatalf("assign after the cleanup: %v\n%s", err, o)
	}
	if got := len(e.metadataCommits(t)); got != 2 {
		t.Errorf("assigning creates the entry on demand, want 2 entries, got %d", got)
	}
}
