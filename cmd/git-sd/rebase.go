package main

import (
	"errors"
	"fmt"
	"git-sd/internal/git"
	"git-sd/internal/metadata"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxRebaseBackups caps metadata.RebaseBackups; the oldest entries (and
// their refs) are pruned beyond this.
const maxRebaseBackups = 10

func runRebase(args []string) {
	rollback := false
	list := false
	n := 1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rollback":
			rollback = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				v, err := strconv.Atoi(args[i])
				if err != nil || v < 1 {
					fmt.Printf("Error: invalid --rollback backup number '%s' (must be a positive integer; see 'git sd rebase --list')\n", args[i])
					exitCleanly(1)
				}
				n = v
			}
		case "--list":
			list = true
		default:
			fmt.Printf("Error: unknown argument '%s' (usage: git sd rebase [--rollback [<n>] | --list])\n", args[i])
			exitCleanly(1)
		}
	}
	if rollback && list {
		fmt.Println("Error: --rollback cannot be combined with --list")
		exitCleanly(1)
	}

	m, err := metadata.Load()
	if err != nil {
		fmt.Printf("Error loading metadata: %v\n", err)
		exitCleanly(1)
	}

	switch {
	case list:
		listBackups(m)
	case rollback:
		if err := requireClean(); err != nil {
			fmt.Printf("Error: %v\n", err)
			exitCleanly(1)
		}
		if n > len(m.RebaseBackups) {
			fmt.Printf("Error: no backup #%d (%d available; see 'git sd rebase --list')\n", n, len(m.RebaseBackups))
			exitCleanly(1)
		}
		idx := len(m.RebaseBackups) - n
		b := m.RebaseBackups[idx]
		if err := restoreBackup(m, idx); err != nil {
			fmt.Printf("Error: %v\n", err)
			exitCleanly(1)
		}
		fmt.Printf("Rolled back to backup #%d (%s, %s).\n", n, b.Timestamp, shortHash(b.SHA))
	default:
		if err := rebase(m); err != nil {
			fmt.Printf("Error: %v\n", err)
			exitCleanly(1)
		}
	}
}

func rebase(m *metadata.Metadata) error {
	if err := requireClean(); err != nil {
		return err
	}
	baseBranch, remote, since, err := git.ResolveBaseBranch()
	if err != nil {
		return err
	}
	if err := git.FetchRemote(remote); err != nil {
		return err
	}
	baseTip, err := git.RevParse(baseBranch)
	if err != nil {
		return err
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		return fmt.Errorf("getting commits: %w", err)
	}
	groups := GroupCommits(commits, m)

	// Decide everything before mutating anything: a single mismatched PR
	// aborts the whole command with the repo untouched.
	var prNumbers []int
	for _, g := range groups {
		if n := m.Commits[g.TopLevelID].PRNumber; n != 0 {
			prNumbers = append(prNumbers, n)
		}
	}
	prStates := fetchPRStates(prNumbers) // all lookups at once; read below in stack order
	drop := map[string]bool{}
	var closedIDs []string
	droppedPRs := 0
	var followUps []followUp
	for _, g := range groups {
		topCM := m.Commits[g.TopLevelID]
		if topCM.PRNumber == 0 {
			continue // never synced — can't have landed via a PR
		}
		lk := prStates[topCM.PRNumber]
		info, err := lk.info, lk.err
		if err != nil {
			return fmt.Errorf("could not check PR #%d's state, nothing was changed: %w", topCM.PRNumber, err)
		}
		switch info.State {
		case "CLOSED":
			closedIDs = append(closedIDs, groupIDs(g)...)
		case "MERGED":
			res, err := classifyLanded(g, info, baseBranch, baseTip)
			if err != nil {
				return fmt.Errorf("PR #%d is merged, but %v.\n"+
					"Nothing was changed. If the local commits for this PR are obsolete, drop them yourself "+
					"(e.g. 'git rebase -i') and re-run 'git sd rebase'", topCM.PRNumber, err)
			}
			for _, c := range g.Commits {
				drop[c.Hash] = true
			}
			droppedPRs++
			if res.Kind != landedFully {
				fu, err := buildFollowUp(m, g, res)
				if err != nil {
					return fmt.Errorf("PR #%d is merged with local changes that did not land, but building a follow-up commit failed, nothing was changed: %w", topCM.PRNumber, err)
				}
				followUps = append(followUps, fu)
			}
		}
	}

	if len(drop) == 0 && len(closedIDs) == 0 && since == baseTip && len(vanishedIDs(m, commits)) == 0 {
		fmt.Println("Already up to date.")
		return nil
	}

	snap, err := takeSnapshot(m)
	if err != nil {
		return err
	}
	retired, err := rebaseOnto(m, snap, baseBranch, commits, drop, closedIDs, followUps)
	if err != nil {
		return err
	}
	fmt.Printf("Rebased onto %s: dropped %d landed PR(s), retired %d commit(s).\n", baseBranch, droppedPRs, retired)
	return nil
}

// takeSnapshot captures HEAD plus the metadata consistent with it, before
// anything is mutated. It's persisted by rebaseOnto.
func takeSnapshot(m *metadata.Metadata) (metadata.RebaseBackup, error) {
	head, err := git.RevParse("HEAD")
	if err != nil {
		return metadata.RebaseBackup{}, err
	}
	now := time.Now()
	b := m.Snapshot()
	b.SHA = head
	b.Timestamp = now.Format(time.RFC3339)
	b.Ref = fmt.Sprintf("refs/sd/backups/%d", now.UnixNano())
	return b, nil
}

// rebaseOnto drops the given commits and replays the rest onto target with a
// real git rebase, then retires finished commits — retireIDs explicitly, plus
// any tracked commit no longer in local history afterward. It persists snap
// as a backup first and records a PendingRebase. If a remaining commit
// conflicts, the git rebase is left in progress for the user to continue,
// skip or abort with plain git, and the bookkeeping is settled by the next
// git-sd command (see reconcilePending). The caller must already have
// fetched target. Returns the number of commits retired.
func rebaseOnto(m *metadata.Metadata, snap metadata.RebaseBackup, target string, commits []git.Commit, drop map[string]bool, retireIDs []string, followUps []followUp) (int, error) {
	if err := requireClean(); err != nil {
		return 0, err
	}

	if err := git.UpdateRef(snap.Ref, snap.SHA); err != nil {
		return 0, fmt.Errorf("could not record backup: %w", err)
	}
	m.RebaseBackups = append(m.RebaseBackups, snap)
	for len(m.RebaseBackups) > maxRebaseBackups {
		git.DeleteRef(m.RebaseBackups[0].Ref)
		m.RebaseBackups = m.RebaseBackups[1:]
	}
	if err := m.Save(); err != nil {
		return 0, fmt.Errorf("could not save backup: %w", err)
	}

	targetTip, err := git.RevParse(target)
	if err != nil {
		return 0, err
	}
	mergeBase, err := git.GetMergeBase(target)
	if err != nil {
		return 0, err
	}
	var dropIDs []string
	for _, c := range commits {
		if drop[c.Hash] {
			if id := git.ExtractTrailer(c.Message, "git-sd-id"); id != "" {
				dropIDs = append(dropIDs, id)
			}
		}
	}
	followUpAt := map[string]followUp{}
	var synthetics []metadata.PendingSynthetic
	for _, fu := range followUps {
		followUpAt[fu.FirstHash] = fu
		synthetics = append(synthetics, fu.PendingSynthetic)
	}
	if len(drop) > 0 || mergeBase != targetTip {
		// A dropped group with local changes that did not land is replaced,
		// at the position of its first commit, by its follow-up commit.
		var remaining []git.Commit
		for _, c := range commits {
			if fu, ok := followUpAt[c.Hash]; ok {
				remaining = append(remaining, fu.Commit)
			}
			if !drop[c.Hash] {
				remaining = append(remaining, c)
			}
		}
		m.PendingRebase = &metadata.PendingRebase{
			OrigHead:   snap.SHA,
			TargetTip:  targetTip,
			RetireIDs:  slices.Clone(retireIDs),
			DropIDs:    dropIDs,
			BackupRef:  snap.Ref,
			Synthetics: synthetics,
		}
		if err := m.Save(); err != nil {
			discardBackup(m, snap.Ref)
			m.PendingRebase = nil
			_ = m.Save()
			return 0, fmt.Errorf("could not save pending rebase: %w", err)
		}
		if err := git.RebasePicks(targetTip, mergeBase, "drop", remaining); err != nil {
			if git.RebaseInProgress() {
				return 0, errors.New(rebaseConflictMessage(m, target, remaining))
			}
			// The rebase never started (e.g. a hook refused): undo the bookkeeping.
			discardBackup(m, snap.Ref)
			m.PendingRebase = nil
			_ = m.Save()
			return 0, err
		}
		m.PendingRebase = nil
	}
	return finishRebase(m, targetTip, retireIDs, dropIDs, synthetics)
}

// finishRebase retires finished commits once the branch has been rebuilt onto
// targetTip: retireIDs and dropIDs (known landed/closed), plus any tracked
// commit that has vanished from local history — except one whose PR is still
// open, which is left alone with a warning (retiring it would queue its
// branch for deletion and close the PR). Saves the metadata and returns the
// number of commits retired.
func finishRebase(m *metadata.Metadata, targetTip string, retireIDs, dropIDs []string, synthetics []metadata.PendingSynthetic) (int, error) {
	after, err := git.GetCommitsInRange(targetTip)
	if err != nil {
		return 0, err
	}
	known := map[string]bool{}
	for _, id := range append(slices.Clone(retireIDs), dropIDs...) {
		known[id] = true
	}
	ids := slices.Clone(retireIDs)
	vanished := vanishedIDs(m, after)
	var need []int // PRs whose state decides whether a vanished commit is safe to retire
	for _, id := range vanished {
		if cm := m.Commits[id]; !known[id] && cm.ParentID == "" && cm.PRNumber != 0 {
			need = append(need, cm.PRNumber)
		}
	}
	states := fetchPRStates(need)
	for _, id := range vanished {
		if known[id] || safeToRetire(m, id, states) {
			ids = append(ids, id)
		}
	}
	retired := retireCommitsLocal(m, ids)

	// Register each follow-up commit that survived (one the user skipped or
	// resolved to nothing is simply gone) under the Stack Set of the group it
	// came from — assigned, but with no PR until the next sync.
	for _, syn := range synthetics {
		for _, c := range after {
			if git.ExtractTrailer(c.Message, "git-sd-id") != syn.ID {
				continue
			}
			m.Commits[syn.ID] = metadata.CommitMetadata{StackSet: syn.StackSet}
			stat := git.DiffShortstat(c.Hash+"^", c.Hash)
			if stat == "" {
				stat = "no net change"
			}
			fmt.Printf("Kept local changes from PR #%d as a new commit (assigned to '%s'): %s; run 'git sd sync' to open a PR.\n", syn.FromPR, syn.StackSet, stat)
			break
		}
	}
	if err := m.Save(); err != nil {
		return 0, fmt.Errorf("error saving metadata: %w", err)
	}
	return retired, nil
}

// safeToRetire reports whether retiring commit id can't close an open PR: a
// sub-commit or never-synced commit has no PR branch of its own, and a PR
// that's merged or closed is already finished. A lookup failure counts as
// not safe.
func safeToRetire(m *metadata.Metadata, id string, states map[int]prStateLookup) bool {
	cm := m.Commits[id]
	if cm.ParentID != "" || cm.PRNumber == 0 {
		return true
	}
	lk := states[cm.PRNumber]
	info, err := lk.info, lk.err
	if err != nil {
		fmt.Printf("Warning: could not check PR #%d, so its removed commit %s was not retired: %v\n", cm.PRNumber, id, err)
		return false
	}
	if info.State == "OPEN" {
		fmt.Printf("Warning: PR #%d is still open but its commit %s is no longer on your branch; leaving its tracking and remote branch alone.\n", cm.PRNumber, id)
		return false
	}
	return true
}

// discardBackup removes the backup entry (and its pinning ref) with the given
// ref, if present.
func discardBackup(m *metadata.Metadata, ref string) {
	for i, b := range m.RebaseBackups {
		if b.Ref == ref {
			m.RebaseBackups = slices.Delete(m.RebaseBackups, i, i+1)
			break
		}
	}
	git.DeleteRef(ref)
}

// rebaseConflictMessage describes a rebase stopped on a conflict: which
// commit (and PR), which files, and how to get out with plain git.
func rebaseConflictMessage(m *metadata.Metadata, target string, remaining []git.Commit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Conflict while rebasing onto %s.\n", target)
	if stopped := git.RebaseStoppedSHA(); stopped != "" {
		for _, c := range remaining {
			if c.Hash != stopped {
				continue
			}
			subject, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
			fmt.Fprintf(&b, "Stopped at: %s \"%s\"", shortHash(c.Hash), subject)
			id := git.ExtractTrailer(c.Message, "git-sd-id")
			top := id
			if cm := m.Commits[id]; cm.ParentID != "" {
				top = cm.ParentID
			}
			if tcm := m.Commits[top]; tcm.PRNumber != 0 {
				fmt.Fprintf(&b, " (PR #%d, set '%s')", tcm.PRNumber, tcm.StackSet)
			}
			b.WriteString("\n")
			if strings.HasPrefix(subject, "Follow-up to PR #") {
				b.WriteString("This commit holds your local changes from that PR that did not land; they collide with what did.\n")
			}
			break
		}
	}
	if files := git.ConflictedFiles(); len(files) > 0 {
		b.WriteString("Conflicting files:\n")
		for _, f := range files {
			fmt.Fprintf(&b, "  %s\n", f)
		}
	}
	b.WriteString("Resolve it with plain git, then:\n" +
		"  git add <files> && git rebase --continue   finish the rebase\n" +
		"  git rebase --skip                          drop this commit (treat it as landed)\n" +
		"  git rebase --abort                         restore your branch exactly as it was\n" +
		"During a rebase, 'ours' is the updated base and 'theirs' is your local commit.\n" +
		"(git checkout --ours/--theirs <file> takes that side's WHOLE file, including its\n" +
		"other changes; edit the conflict markers to keep just one side of a hunk.)\n" +
		"git sd retires landed PRs' tracking the next time it runs.")
	return b.String()
}

// vanishedIDs returns the tracked, Stack-Set-assigned commits whose
// git-sd-id no longer appears anywhere in commits — they've landed (or were
// dropped) and are finished. Presence is judged over every commit, not just
// grouped ones, so a dormant sub-commit whose parent isn't assigned isn't
// mistaken for gone (B-018).
func vanishedIDs(m *metadata.Metadata, commits []git.Commit) []string {
	present := map[string]bool{}
	for _, c := range commits {
		if id := git.ExtractTrailer(c.Message, "git-sd-id"); id != "" {
			present[id] = true
		}
	}
	var ids []string
	for id, cm := range m.Commits {
		if cm.StackSet != "" && !present[id] {
			ids = append(ids, id)
		}
	}
	return ids
}

// retireCommitsLocal removes the metadata entry for each given git-sd-id and
// queues each top-level commit's remote branch for the next sync to delete
// (after it has retargeted any PR still based on it). The caller must have
// already confirmed each id is finished. Returns how many were retired.
func retireCommitsLocal(m *metadata.Metadata, ids []string) int {
	retired := 0
	for _, id := range ids {
		cm, ok := m.Commits[id]
		if !ok {
			continue
		}
		if cm.ParentID == "" && cm.BranchName != "" {
			m.PendingBranchDeletions = append(m.PendingBranchDeletions, metadata.PendingDeletion{Branch: cm.BranchName, StackSet: cm.StackSet})
		}
		delete(m.Commits, id)
		retired++
	}
	return retired
}

// restoreBackup resets HEAD and restores metadata from backup idx together,
// then drops that backup (older ones survive) and saves.
func restoreBackup(m *metadata.Metadata, idx int) error {
	b := m.RebaseBackups[idx]
	if err := git.ResetHard(b.SHA); err != nil {
		return err
	}
	m.Restore(b)
	m.RebaseBackups = slices.Delete(m.RebaseBackups, idx, idx+1)
	git.DeleteRef(b.Ref)
	return m.Save()
}

func listBackups(m *metadata.Metadata) {
	if len(m.RebaseBackups) == 0 {
		fmt.Println("No rebase backups.")
		return
	}
	for n := 1; n <= len(m.RebaseBackups); n++ {
		b := m.RebaseBackups[len(m.RebaseBackups)-n]
		fmt.Printf("%d  %s  %s\n", n, b.Timestamp, shortHash(b.SHA))
	}
}

func groupIDs(g PRGroup) []string {
	var ids []string
	for _, c := range g.Commits {
		if id := git.ExtractTrailer(c.Message, "git-sd-id"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func requireClean() error {
	clean, err := git.IsClean()
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("you have uncommitted changes; commit or stash them first")
	}
	return nil
}

func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}
