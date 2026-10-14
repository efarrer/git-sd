package main

import (
	"fmt"
	"github.com/efarrer/git-sd/internal/git"
	"github.com/efarrer/git-sd/internal/github"
	"github.com/efarrer/git-sd/internal/metadata"
	"maps"
	"slices"
	"strings"
	"sync"
)

func runSync(args []string) {
	setName := ""
	if len(args) > 0 && args[0] != "--all" {
		setName = args[0]
	}
	beginOperation("sync", setName) // lets `status` in another terminal show Syncing
	defer endOperation()

	m, err := metadata.Load()
	if err != nil {
		fmt.Printf("Error: could not load the metadata: %v\n", err)
		exitCleanly(1)
	}

	baseBranch, remote, since, err := git.ResolveBaseBranch()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		exitCleanly(1)
	}

	commits, err := git.GetCommitsInRange(since)
	if err != nil {
		fmt.Printf("Error: could not read the commits: %v\n", err)
		exitCleanly(1)
	}

	// Group commits into PRs: each top-level commit plus its sub-commits is one
	// PR, identified by the top-level commit's stable git-sd-id (not a physical
	// index). A bug-fix sub-commit therefore folds into its parent's PR rather
	// than getting its own.
	groups := GroupCommits(commits, m)

	// Pass 0: dissolve sets that no longer have any commits. A set that does
	// is left registered for now: its Stack is read below and only unstacked
	// when something GitHub would reject is about to happen (a base edit, or a
	// registered PR list that no longer matches) — see "Pass 0" in DESIGN.md.
	//
	// This iterates m.StackSets rather than m.StackSetNames(): the latter is
	// derived from commits, so it can't see a set whose commits have all been
	// retired (by rebase or merge) — exactly the set that needs dissolving.
	referenced := map[string]bool{}
	var dissolved []string // sets removed because all their commits are gone; reported after the PR summary
	for _, sn := range m.StackSetNames() {
		referenced[sn] = true
	}
	for _, sn := range slices.Sorted(maps.Keys(m.StackSets)) {
		if setName != "" && sn != setName {
			continue
		}
		if referenced[sn] {
			continue
		}
		sm := m.StackSets[sn]
		if sm.StackNumber != 0 {
			if err := github.Unstack(sm.StackNumber); err != nil {
				failOnEnvError(m, err)
				reportSyncError("could not unregister stack '%s': %v", sn, err)
				continue
			}
		}
		delete(m.StackSets, sn)
		dissolved = append(dissolved, sn)
	}

	// Pass 1: Push and Create/Update PRs
	// Each Stack Set chains its own branches independently — tracked per
	// StackSet name rather than in one shared variable, since two unrelated,
	// unmerged Stack Sets can be interleaved on the same branch (SPEC.md's
	// "Stack Sets" feature). Without this, syncing more than one set in the
	// same call would incorrectly base one set's first PR on the other's
	// branch instead of on sd.base-branch.
	stackBase := map[string]string{} // PR --base argument: branch name (or bare base branch for the first group)
	pushTip := map[string]string{}   // cherry-pick base for the next group's trailer-stripped chain
	warnedMerged := false
	skipped := map[string]bool{} // TopLevelIDs whose PR was already MERGED/CLOSED — excluded from Stack registration
	var results []syncResult     // what happened to each PR, for the summary

	// Look up every in-scope PR's state at once (about one round trip for the
	// whole stack) instead of one at a time inside the loop below.
	var prNumbers []int
	for _, g := range groups {
		if setName != "" && g.StackSet != setName {
			continue
		}
		if n := m.Commits[g.TopLevelID].PRNumber; n != 0 {
			prNumbers = append(prNumbers, n)
		}
	}
	// The registered Stacks of the sets in scope are read in the same round
	// trip as the PR states.
	var stackNumbers []int
	for _, sn := range m.StackSetNames() {
		if setName != "" && sn != setName {
			continue
		}
		if n := m.StackSets[sn].StackNumber; n != 0 {
			stackNumbers = append(stackNumbers, n)
		}
	}
	var prStates map[int]prStateLookup
	var stackStates map[int]stackLookup
	var fetched sync.WaitGroup
	fetched.Add(2)
	go func() { defer fetched.Done(); prStates = fetchPRStates(prNumbers) }()
	go func() { defer fetched.Done(); stackStates = fetchStacks(stackNumbers) }()
	fetched.Wait()

	// ensureUnstacked tears a set's registered Stack down, once, at the last
	// moment: before a PR's base changes, or before the set is re-registered.
	unstacked := map[string]bool{}
	ensureUnstacked := func(sn string) {
		sm, ok := m.StackSets[sn]
		if !ok || sm.StackNumber == 0 || unstacked[sn] {
			return
		}
		unstacked[sn] = true
		if err := github.Unstack(sm.StackNumber); err != nil {
			failOnEnvError(m, err)
			reportSyncError("could not unregister stack '%s': %v", sn, err)
			return
		}
		sm.StackNumber = 0
		m.StackSets[sn] = sm
	}
	// A registered Stack that can't be read can't be trusted: tear it down now,
	// as sync always used to, and re-register it in Pass 2.
	registered := map[string][]int{} // sets whose Stack was read and is kept: its PR numbers, bottom to top
	for _, sn := range m.StackSetNames() {
		if setName != "" && sn != setName {
			continue
		}
		sm := m.StackSets[sn]
		if sm.StackNumber == 0 {
			continue
		}
		lk := stackStates[sm.StackNumber]
		if lk.err != nil {
			failOnEnvError(m, lk.err)
			ensureUnstacked(sn)
			continue
		}
		registered[sn] = lk.prs
	}

	// What each commit looks like now, to record what this sync pushes (see
	// recordSynced). One pipeline for the whole range; if it fails the sync still
	// works, it just can't say afterwards which sub-commits are in their PRs.
	patchIDs, patchErr := git.PatchIDs(since)
	if patchErr != nil {
		fmt.Printf("Warning: could not record what was synced (status may show sub-commits as unsynced): %v\n", patchErr)
	}

	// Groups still to process, in stack order. Each wave plans them locally,
	// pushes every branch in ONE `git push`, then creates/edits PRs in order.
	// A group that fails drops out; the later groups of the same Stack Set were
	// built on its branch, so they go round again and are re-planned from the
	// last good tip, exactly as if every group had been handled one at a time.
	var remaining []PRGroup
	for _, g := range groups {
		if setName != "" && g.StackSet != setName {
			continue
		}
		remaining = append(remaining, g)
	}

	type plannedGroup struct {
		g          PRGroup
		skip       bool   // PR already merged/closed: nothing to push
		branchName string // remote branch
		tip        string // trailer-stripped tip to push
	}
	for len(remaining) > 0 {
		// Wave, step 1: plan (local only, no network).
		tmpBase, tmpTip := maps.Clone(stackBase), maps.Clone(pushTip)
		retargets := false // does any existing PR's base change in this wave?
		var plan []plannedGroup
		var pushes []github.BranchPush
		for _, g := range remaining {
			topMeta := m.Commits[g.TopLevelID]

			// Partial Merge Handling: once this group's PR has been merged or
			// closed (e.g. by a previous `git sd merge`), its branch is no
			// longer active — don't re-push or re-update it, and retarget
			// whatever comes after it back to the stack's own base branch
			// instead of chaining onto a now-defunct branch.
			if topMeta.PRNumber != 0 {
				lk := prStates[topMeta.PRNumber]
				info, err := lk.info, lk.err
				failOnEnvError(m, err)
				if err == nil && (info.State == "MERGED" || info.State == "CLOSED") {
					if info.State == "MERGED" && !warnedMerged {
						// Survivors are built on the pre-rebase base until the
						// merged commits are dropped locally.
						fmt.Println("Warning: some PRs are already merged but still on your local branch; run 'git sd rebase' first so the remaining PRs are built on the updated base.")
						warnedMerged = true
					}
					tmpBase[g.StackSet] = baseBranch
					tmpTip[g.StackSet] = since
					plan = append(plan, plannedGroup{g: g, skip: true})
					continue
				}
			}

			branchName := topMeta.BranchName
			if branchName == "" {
				subject, _, _ := strings.Cut(g.Commits[0].Message, "\n")
				branchName = git.BranchNameSlug(subject, g.TopLevelID)
			}

			tipBase, ok := tmpTip[g.StackSet]
			if !ok {
				tipBase = since
			}
			// Cherry-pick and re-message just this group's own commits onto the
			// chain's current tip, dropping each one's git-sd-id trailer — it's
			// git-sd's own internal identity marker (metadata.json remains its
			// source of truth) and never belongs in a PR a reviewer sees.
			// Building a fresh chain here (rather than pushing the local
			// commits' own hashes) also means the pushed branch contains only
			// this group's changes, not any unrelated Stack Set's commits that
			// happen to sit physically between the base and this group on the
			// working branch.
			strippedTip, err := git.BuildTrailerStrippedChain(tipBase, g.Commits)
			if err != nil {
				reportSyncError("could not prepare branch %s for push: %v", branchName, err)
				continue
			}
			// Will this existing PR's base change? If so (or if we can't tell),
			// this wave must push and retarget one group at a time.
			if topMeta.PRNumber != 0 {
				newBase, ok := tmpBase[g.StackSet]
				if !ok {
					newBase = baseBranch
				}
				newBase, _ = strings.CutPrefix(newBase, remote+"/")
				if lk := prStates[topMeta.PRNumber]; lk.err != nil || lk.info == nil || lk.info.BaseRefName != newBase {
					retargets = true
				}
			}
			tmpBase[g.StackSet] = branchName // chain within this Stack Set only
			tmpTip[g.StackSet] = strippedTip
			plan = append(plan, plannedGroup{g: g, branchName: branchName, tip: strippedTip})
			pushes = append(pushes, github.BranchPush{Local: strippedTip, Branch: branchName})
		}

		// Wave, step 2: one push for every branch — unless a PR is being
		// retargeted, in which case each group is pushed in step 3, just before
		// its own PR is updated (see "Batched pushes" in DESIGN.md).
		pushedBranch := map[string]github.PushResult{}
		if !retargets {
			pushResults, err := github.PushBranches(remote, pushes)
			if err != nil {
				failOnEnvError(m, err)
				reportSyncError("could not push branches: %v", err)
				break
			}
			for i, p := range pushes {
				pushedBranch[p.Branch] = pushResults[i]
			}
		}

		// Wave, step 3: in stack order, advance the chain and update PRs that
		// already exist; PRs that don't exist yet are created together below.
		failedSets := map[string]bool{}
		var again []PRGroup
		type preparedGroup struct {
			g          PRGroup
			topMeta    metadata.CommitMetadata
			branchName string
			title      string
			body       string
			prBase     string
			action     syncAction
			updateFail bool // gh pr edit failed: still recorded, but not reported
			create     bool // PR doesn't exist yet
			created    *github.PRInfo
			createErr  error
		}
		var prepared []*preparedGroup
		for _, p := range plan {
			g := p.g
			if failedSets[g.StackSet] {
				again = append(again, g) // built on a failed group's branch: re-plan
				continue
			}
			if p.skip {
				stackBase[g.StackSet] = baseBranch
				pushTip[g.StackSet] = since
				skipped[g.TopLevelID] = true
				continue
			}
			topMeta := m.Commits[g.TopLevelID]
			topMeta.BranchName = p.branchName
			branchName := p.branchName

			res, batched := pushedBranch[branchName]
			if !batched {
				rs, err := github.PushBranches(remote, []github.BranchPush{{Local: p.tip, Branch: branchName}})
				if err != nil {
					failOnEnvError(m, err)
					res = github.PushResult{Err: err}
				} else {
					res = rs[0]
				}
			}
			if res.Err != nil {
				failOnEnvError(m, res.Err)
				reportSyncError("could not push branch %s: %v", branchName, res.Err)
				failedSets[g.StackSet] = true
				continue
			}

			currentBase, ok := stackBase[g.StackSet]
			if !ok {
				currentBase = baseBranch
			}
			strippedMessage := git.StripTrailer(g.Commits[0].Message, "git-sd-id")
			title, body, _ := strings.Cut(strippedMessage, "\n")
			body = strings.TrimSpace(body)
			prBase, _ := strings.CutPrefix(currentBase, remote+"/")

			// The branch is pushed, so the chain advances whatever happens to
			// its PR: the next group's base is this branch.
			stackBase[g.StackSet] = branchName // Chain within this Stack Set only
			pushTip[g.StackSet] = p.tip

			pg := &preparedGroup{g: g, topMeta: topMeta, branchName: branchName, title: title, body: body, prBase: prBase, action: syncUnchanged}
			prepared = append(prepared, pg)
			if topMeta.PRNumber == 0 {
				pg.create = true
				continue
			}
			// gh pr edit (~0.4 s) runs only when the title, body or base differ
			// from what was last applied, not on every sync.
			fingerprint := prFingerprint(title, body, prBase)
			if needsPREdit(topMeta.LastSynced, title, body, prBase) {
				// A PR from before fingerprints were recorded gets one edit to
				// establish the baseline; that edit alone isn't reported as an
				// update, since nothing is known to have changed.
				baseline := topMeta.LastSynced == ""
				// GitHub rejects any --base on a PR in a registered Stack, even
				// the value it already has, but accepts title/body edits: send
				// the base only when it really changes (or can't be checked),
				// after taking the Stack down.
				editBase := ""
				if lk := prStates[topMeta.PRNumber]; lk.err != nil || lk.info == nil || lk.info.BaseRefName != prBase {
					editBase = prBase
					ensureUnstacked(g.StackSet)
				}
				if err := github.UpdatePR(topMeta.PRNumber, title, body, editBase); err != nil {
					failOnEnvError(m, err)
					reportSyncError("could not update PR #%d: %v", topMeta.PRNumber, err)
					pg.updateFail = true
				} else {
					pg.topMeta.LastSynced = fingerprint
					if !baseline {
						pg.action = syncUpdated
					}
				}
			}
			if !pg.updateFail && pg.action == syncUnchanged && res.Pushed {
				pg.action = syncUpdated
			}
		}

		// Create the new PRs concurrently (about 2.2 s each, and GitHub handles
		// them in parallel). Every base branch is already pushed, so order
		// doesn't matter; results are applied below in stack order.
		var creating []*preparedGroup
		for _, pg := range prepared {
			if pg.create {
				creating = append(creating, pg)
			}
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxConcurrentPRCreates)
		for _, pg := range creating {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				pg.created, pg.createErr = github.CreatePR(pg.title, pg.body, pg.prBase, pg.branchName)
			}()
		}
		wg.Wait()

		// Record every success before acting on any error, so a fatal
		// environment error can't lose the PR numbers of PRs that were created.
		for _, pg := range creating {
			if pg.createErr == nil {
				pg.topMeta.PRNumber = pg.created.Number
				pg.topMeta.PRURL = pg.created.URL
				pg.topMeta.LastSynced = prFingerprint(pg.title, pg.body, pg.prBase)
				pg.action = syncCreated
				m.Commits[pg.g.TopLevelID] = pg.topMeta
			}
		}
		for _, pg := range creating {
			if pg.createErr != nil {
				failOnEnvError(m, pg.createErr)
			}
		}
		var notCreated []string
		for _, pg := range prepared {
			if pg.createErr != nil {
				reportSyncError("could not create a PR for branch %s: %v", pg.branchName, pg.createErr)
				notCreated = append(notCreated, pg.branchName)
				continue
			}
			if !pg.create {
				m.Commits[pg.g.TopLevelID] = pg.topMeta
			}
			recordSynced(m, pg.g, pg.topMeta.PRNumber, patchIDs)
			if !pg.updateFail {
				results = append(results, syncResult{Action: pg.action, Number: pg.topMeta.PRNumber, URL: pg.topMeta.PRURL, Title: pg.title, StackSet: pg.g.StackSet})
			}
		}
		if len(notCreated) > 0 {
			// The branches are pushed and later PRs already sit on them, so
			// the next sync only has to create what is missing.
			fmt.Printf("No PR was created for: %s. Fix the error above and re-run 'git sd sync' to create %s (and register the Stack); the other PRs need no changes.\n",
				strings.Join(notCreated, ", "), map[bool]string{true: "it", false: "them"}[len(notCreated) == 1])
		}
		remaining = again
	}

	// Retired commits' remote branches are only deleted now, after Pass 1 has
	// retargeted every surviving PR: deleting a branch an open PR is still
	// based on makes GitHub close that PR. Pass 1 can fail to retarget (a
	// push or base-edit error), and PRs outside git-sd may be based on the
	// branch too, so GitHub is asked directly before each deletion; if it
	// can't answer, the branch stays queued (fail safe).
	var stillPending []metadata.PendingDeletion
	deleted := 0
	for _, pd := range m.PendingBranchDeletions {
		if setName != "" && pd.StackSet != setName {
			stillPending = append(stillPending, pd)
			continue
		}
		dependents, err := github.OpenPRsBasedOn(pd.Branch)
		if err != nil {
			failOnEnvError(m, err)
			fmt.Printf("Warning: could not check for PRs based on %s, keeping it (will retry next sync): %v\n", pd.Branch, err)
			stillPending = append(stillPending, pd)
			continue
		}
		if len(dependents) > 0 {
			fmt.Printf("Warning: keeping finished branch %s — open PR(s) %s are still based on it (will retry next sync)\n", pd.Branch, formatPRList(dependents))
			stillPending = append(stillPending, pd)
			continue
		}
		if err := github.DeleteRemoteBranch(remote, pd.Branch); err != nil {
			fmt.Printf("Warning: could not delete branch %s (will retry next sync): %v\n", pd.Branch, err)
			stillPending = append(stillPending, pd)
		} else {
			deleted++
		}
	}
	m.PendingBranchDeletions = stillPending

	// Pass 2: register each Stack Set's current PR chain as a GitHub Stack.
	// Local metadata only tracks commit-trailer identity and PR numbers — the
	// ordered PR chain itself must be separately registered with GitHub so
	// its native Stacks UI and async merge understand the chain. move/assign
	// only touch local metadata, so this — combined with Pass 0's teardown —
	// is also how a restructure actually takes effect on GitHub. GitHub's
	// Stacks API rejects registering a "stack" of fewer than 2 PRs (there's
	// nothing to chain), so a lone PR is simply left unregistered; git-sd's
	// own PR is sufficient on its own.
	setPRNumbers := map[string][]int{}
	expected := map[string]int{} // live groups per set: all of them need a PR before the Stack can be registered
	for _, g := range groups {
		if skipped[g.TopLevelID] {
			continue
		}
		expected[g.StackSet]++
		topMeta := m.Commits[g.TopLevelID]
		if topMeta.PRNumber == 0 {
			continue
		}
		setPRNumbers[g.StackSet] = append(setPRNumbers[g.StackSet], topMeta.PRNumber)
	}

	for _, sn := range m.StackSetNames() {
		if setName != "" && sn != setName {
			continue
		}
		current := setPRNumbers[sn]
		if len(current) < expected[sn] {
			// A group has no PR (its create failed, which was reported). GitHub
			// refuses a chain with a gap, so registering now could only add a
			// second, redundant error; the next sync registers it.
			continue
		}
		if len(current) < 2 {
			ensureUnstacked(sn) // a lone PR isn't a Stack
			continue
		}
		if have, ok := registered[sn]; ok && !unstacked[sn] && slices.Equal(have, current) {
			continue // the registered Stack already is this chain: nothing to do
		}
		ensureUnstacked(sn)
		info, err := github.CreateStack(current)
		if err != nil {
			failOnEnvError(m, err)
			reportSyncError("could not register stack '%s': %v", sn, err)
			continue
		}
		sm := m.StackSets[sn]
		sm.StackNumber = info.Number
		m.StackSets[sn] = sm
	}

	if err := m.Save(); err != nil {
		reportSyncError("could not save the metadata: %v", err)
	}

	for _, line := range formatSyncSummary(results) {
		fmt.Println(line)
	}
	if deleted > 0 {
		fmt.Printf("Deleted %d finished %s.\n", deleted, plural(deleted, "branch", "branches"))
	}
	for _, sn := range dissolved {
		fmt.Printf("Stack Set '%s' fully finished; dissolved.\n", sn)
	}
	if syncErrors > 0 {
		fmt.Printf("Sync completed with %d %s.\n", syncErrors, plural(syncErrors, "error", "errors"))
		exitCleanly(1)
	}
	fmt.Println("Sync completed.")
}

// syncErrors counts the errors this sync has reported. A failed group or step
// doesn't stop the sync (one bad PR must not block the others, and the next
// sync retries it), but the closing line and the exit status must say so.
var syncErrors int

// reportSyncError prints "Error: could not <action>: <reason>" and counts it.
func reportSyncError(format string, args ...any) {
	syncErrors++
	fmt.Printf("Error: "+format+"\n", args...)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func formatPRList(numbers []int) string {
	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, ", ")
}

// failOnEnvError ends the run if err means gh is missing or unauthenticated:
// every later gh call would fail the same way, so repeating the message for
// each branch and PR only buries it. Metadata gathered so far (PR numbers
// already created in this run) is saved first.
func failOnEnvError(m *metadata.Metadata, err error) {
	if !github.IsEnvError(err) {
		return
	}
	_ = m.Save()
	fmt.Printf("Error: %v\n", err)
	exitCleanly(1)
}
