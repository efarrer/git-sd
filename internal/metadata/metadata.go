package metadata

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

type CommitMetadata struct {
	StackSet string `json:"stack_set"`
	Index    int    `json:"index"`
	// ParentID is the git-sd-id of this commit's top-level parent; empty for
	// a top-level commit. A commit is a sub-commit iff ParentID != "". See
	// DESIGN.md's "Metadata Schema" for the full model.
	ParentID string `json:"parent_id,omitempty"`
	// SubRank orders a sub-commit among its siblings. It's a decimal-digit
	// string (e.g. "1", "45", "91") meaning a rank strictly between 0 and 1
	// (0.1, 0.45, 0.91) — not a small integer — so new sub-commits can be
	// inserted between existing siblings without renumbering anything.
	// Compare by right-padding the shorter string with "0" to equal length
	// and comparing as integers. Empty for a top-level commit.
	SubRank  string `json:"sub_rank,omitempty"`
	PRURL    string `json:"pr_url"`
	PRNumber int    `json:"pr_number"`
	// BranchName is the branch slug computed for this group at first sync
	// (top-level commits only). Stored rather than recomputed so a later
	// commit-message reword doesn't rename the branch or churn its PR.
	BranchName string `json:"branch_name,omitempty"`
	// LastSynced fingerprints the PR title, body and base as last successfully
	// applied to GitHub (top-level commits only), so sync can skip `gh pr edit`
	// when nothing changed. Empty means "never recorded": the next sync edits
	// once and records it.
	LastSynced string `json:"last_synced,omitempty"`
	// SyncedFingerprint and SyncedPR record what the last successful sync put
	// into a PR for this commit (top-level or sub-commit): a fingerprint of its
	// content and message, and the PR it went into. status compares them with
	// the commit as it is now to show a sub-commit that isn't in its PR yet.
	SyncedFingerprint string `json:"synced_fingerprint,omitempty"`
	SyncedPR          int    `json:"synced_pr,omitempty"`
}

type StackSetMetadata struct {
	BaseBranch   string `json:"base_branch"`
	TargetBranch string `json:"target_branch"`
	Status       string `json:"status"`
	// StackNumber is the GitHub Stack's repository-scoped number once
	// registered (0 if never registered, or torn down). GitHub rejects any
	// base-branch edit on a PR that's part of a registered Stack, so sync
	// unstacks (using this) just before a base must change or the registered
	// PR list stops matching, then re-registers; otherwise the Stack is kept.
	StackNumber int `json:"stack_number,omitempty"`
}

// PendingDeletion is a retired commit's remote branch, queued for sync to
// delete once it has retargeted any PR still based on it — deleting it
// earlier could make GitHub close that PR.
type PendingDeletion struct {
	Branch   string `json:"branch"`
	StackSet string `json:"stack_set"`
}

// RebaseBackup is a point `git sd rebase --rollback` can restore: HEAD plus
// the metadata it was consistent with. Ref pins SHA (refs/sd/backups/...) so
// the commits stay reachable regardless of reflog/gc settings.
type RebaseBackup struct {
	SHA                    string                      `json:"sha"`
	Ref                    string                      `json:"ref"`
	Timestamp              string                      `json:"timestamp"`
	Commits                map[string]CommitMetadata   `json:"commits"`
	StackSets              map[string]StackSetMetadata `json:"stack_sets"`
	PendingBranchDeletions []PendingDeletion           `json:"pending_branch_deletions,omitempty"`
}

// PendingMove records a 'move'/'fixup' whose rebase stopped on a conflict, so
// the attachment change (ParentID/SubRank/StackSet on MovedID) can be applied
// or discarded once the user finishes or aborts that rebase. OldOrder and
// NewOrder are git-sd-ids in physical order; see DESIGN.md "Conflict Handling".
type PendingMove struct {
	OldOrder []string `json:"old_order"`
	NewOrder []string `json:"new_order"`
	MovedID  string   `json:"moved_id"`
	ParentID string   `json:"parent_id,omitempty"`
	SubRank  string   `json:"sub_rank,omitempty"`
	StackSet string   `json:"stack_set"`
}

// Apply sets the pending attachment on the moved commit's metadata.
func (p *PendingMove) Apply(m *Metadata) {
	cm := m.Commits[p.MovedID]
	cm.ParentID = p.ParentID
	cm.SubRank = p.SubRank
	cm.StackSet = p.StackSet
	m.Commits[p.MovedID] = cm
}

// PendingRebase records a 'rebase' whose git rebase stopped on a conflict, so
// the bookkeeping (retiring landed/closed commits, or discarding the backup
// taken first) can be settled once the user finishes or aborts it. See
// DESIGN.md "Conflict Handling".
type PendingRebase struct {
	OrigHead  string   `json:"orig_head"`
	TargetTip string   `json:"target_tip"`
	RetireIDs []string `json:"retire_ids,omitempty"` // explicit retirements (closed PRs)
	DropIDs   []string `json:"drop_ids,omitempty"`   // ids of commits dropped as landed
	BackupRef string   `json:"backup_ref"`           // ref of the backup taken before the rebase
	// Synthetics are follow-up commits created for merged groups that had
	// local changes which never landed; each is registered under its Stack
	// Set once the rebase finishes, if it is still in history.
	Synthetics []PendingSynthetic `json:"synthetics,omitempty"`
}

// PendingSynthetic is a follow-up commit created by rebase (see PendingRebase).
type PendingSynthetic struct {
	ID       string `json:"id"`
	StackSet string `json:"stack_set"`
	FromPR   int    `json:"from_pr"`
}

type Metadata struct {
	Commits                map[string]CommitMetadata   `json:"commits"`
	StackSets              map[string]StackSetMetadata `json:"stack_sets"`
	PendingBranchDeletions []PendingDeletion           `json:"pending_branch_deletions,omitempty"`
	RebaseBackups          []RebaseBackup              `json:"rebase_backups,omitempty"`
	PendingMove            *PendingMove                `json:"pending_move,omitempty"`
	PendingRebase          *PendingRebase              `json:"pending_rebase,omitempty"`
}

// IsBlank reports whether the entry records nothing: no Stack Set, parent, PR,
// branch or sync record. Index is not information (it is a cosmetic copy of the
// physical position that nothing reads). With no entry at all a commit is simply
// Ready, so a blank entry is redundant.
func (c CommitMetadata) IsBlank() bool {
	return c.StackSet == "" && c.ParentID == "" && c.SubRank == "" &&
		c.PRURL == "" && c.PRNumber == 0 && c.BranchName == "" && c.LastSynced == "" &&
		c.SyncedFingerprint == "" && c.SyncedPR == 0
}

// PruneBlank deletes the blank entries from m.Commits and returns how many it
// removed. An entry that some other entry names as its ParentID is kept, so a
// sub-commit's parent reference never dangles.
func (m *Metadata) PruneBlank() int {
	parents := map[string]bool{}
	for _, cm := range m.Commits {
		if cm.ParentID != "" {
			parents[cm.ParentID] = true
		}
	}
	removed := 0
	for id, cm := range m.Commits {
		if cm.IsBlank() && !parents[id] {
			delete(m.Commits, id)
			removed++
		}
	}
	return removed
}

// nonBlank returns a copy of commits without the blank entries.
func nonBlank(commits map[string]CommitMetadata) map[string]CommitMetadata {
	out := make(map[string]CommitMetadata, len(commits))
	for id, cm := range commits {
		if !cm.IsBlank() {
			out[id] = cm
		}
	}
	return out
}

// Snapshot returns a deep copy of the metadata's restorable state (everything
// but the backups themselves), for a RebaseBackup. Blank entries are left out:
// they record nothing, so restoring without them loses nothing.
func (m *Metadata) Snapshot() RebaseBackup {
	return RebaseBackup{
		Commits:                nonBlank(m.Commits),
		StackSets:              maps.Clone(m.StackSets),
		PendingBranchDeletions: slices.Clone(m.PendingBranchDeletions),
	}
}

// Restore overwrites the metadata's restorable state from b, leaving
// RebaseBackups untouched.
func (m *Metadata) Restore(b RebaseBackup) {
	m.Commits = maps.Clone(b.Commits)
	m.StackSets = maps.Clone(b.StackSets)
	m.PendingBranchDeletions = slices.Clone(b.PendingBranchDeletions)
	if m.Commits == nil {
		m.Commits = make(map[string]CommitMetadata)
	}
	if m.StackSets == nil {
		m.StackSets = make(map[string]StackSetMetadata)
	}
}

// StackSetNames returns the distinct, non-empty stack set names referenced by any commit.
func (m *Metadata) StackSetNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, cm := range m.Commits {
		if cm.StackSet == "" || seen[cm.StackSet] {
			continue
		}
		seen[cm.StackSet] = true
		names = append(names, cm.StackSet)
	}
	return names
}

func Load() (*Metadata, error) {
	path, err := getMetadataPath()
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Metadata{
			Commits:   make(map[string]CommitMetadata),
			StackSets: make(map[string]StackSetMetadata),
		}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading metadata: %w", err)
	}

	var m Metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("error parsing metadata: %w", err)
	}

	return &m, nil
}

func (m *Metadata) Save() error {
	m.PruneBlank() // entries that record nothing are not worth keeping (B-022)
	path, err := getMetadataPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("error creating metadata directory: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling metadata: %w", err)
	}

	// Write-then-rename strategy for atomicity
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("error writing temporary metadata: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("error renaming metadata file: %w", err)
	}

	return nil
}

func getMetadataPath() (string, error) {
	// --absolute-git-dir resolves to the worktree-specific git dir
	// (.git/worktrees/<name> in a linked worktree, or .git in the primary
	// checkout), always as an absolute path — so metadata is isolated
	// per-worktree and git-sd works from any subdirectory, not just the repo
	// root.
	cmd := exec.Command("git", "rev-parse", "--absolute-git-dir")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("could not locate git directory: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("could not locate git directory (is this a git repository?): %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	return filepath.Join(gitDir, "sd", "metadata.json"), nil
}

// Dir returns git-sd's per-worktree state directory (<git dir>/sd), where
// metadata.json and the operation lock live. It is not created.
func Dir() (string, error) {
	path, err := getMetadataPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}
