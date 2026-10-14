#!/bin/bash

# sd-sim: High-fidelity simulation of the git-sd workflow.
# This script walks through scenarios defined in SPEC.md.

SCENARIO="${1:-walkthrough}"

# --- Helpers ---

# Row Helper: prints one status row (Icon, Status word, Index, Stack Set, ...).
# Usage: row <icon> <word> <index> <stack-set> <sd-id> <hash> <message> <pr>
# The icon cell is 4 columns wide: a 2-wide emoji plus 2 spaces, 3 spaces for
# the 1-wide em dash, or 4 spaces for no icon (a Ready commit).
row() {
    local icon="$1" word="$2" index="$3" set="$4" sdid="$5" hash="$6" msg="$7" pr="$8"
    local pad="  "
    if [ -z "$icon" ]; then
        pad="    "
    elif [[ "$icon" == "—" ]]; then
        pad="   "
    fi
    printf "%s%s%-12s%-7s%-15s%-11s%-8s%-29s%s\n" "$icon" "$pad" "$word" "$index" "$set" "$sdid" "$hash" "$msg" "$pr"
}

# Header line for the status table (the Icon column's header is blank).
status_header() {
    row "" "Status" "Index" "Stack Set" "git-sd-id" "Hash" "Message" "PR"
}

# Tool Prompt
type_out() {
    local text="$1"
    local delay=0.01
    printf "\e[1;32m$\e[0m "
    for (( i=0; i<${#text}; i++ )); do
        echo -n "${text:$i:1}"
        sleep $delay
    done
    echo ""
    sleep 0.2
}

# Scenario Headers
header() {
    # Aligned Scenario numbering
    echo -e "\n\e[1;34m=== Scenario $1 ===\e[0m"
    sleep 0.4
}

# Walkthrough meta-information (NOT tool output)
info() {
    echo -e "\e[0;36m\e[4mSimulation Note: $1\e[24m\e[24m\e[0m"
}

# Standard user pause
wait_for_user() {
    echo -ne "\n\e[1;33m[Press Enter to continue...]\e[0m"
    read -r
}

# --- Mock Status Outputs ---

show_usage() {
    cat <<EOF
usage: git sd <command> [<args>]

Commands:
  status      List commits and their Stack Set/PR status
                Options: --legend, --no-legend
  assign      Assign commits to a Stack Set
  move        Reorder commits, promote/demote/reparent sub-commits
  fixup       Attach HEAD as the next sub-commit of a PR (PR feedback)
  sync        Synchronize GitHub PR state with local branch
  merge       Safely merge approved PRs in a stack
  rebase      Drop landed PRs' commits and rebase onto the updated base
              branch (local only; backed up; a conflict leaves a git rebase in progress)

Options:
  --help      Show this help message
  --agent     Print a detailed, complete command reference for agents

Configuration (via git config):
  sd.status.legend [true|false]      Always show the status emoji legend (Default: false)
  sd.status.columns [string]         Comma-separated list of columns for 'git sd status'
                                     (Default: Status,Index,Stack Set,git-sd-id,Hash,Message,PR)
  sd.status.color [true|false]       Enable colorized output (Default: true)
  sd.base-branch [string]            Upstream branch to target instead of main/master
                                     (Default: tracked upstream or origin/main). Useful for
                                     testing against a branch without main's approval guards.

Note: 'git sd merge' delegates approval/CI enforcement entirely to GitHub's
native Stacks async-merge API and whatever branch protection is configured
on the repo — git-sd does not check approvals or CI status itself.

Note: Use 'git config' for project config or 'git config --global' for home config.
EOF
}

show_legend() {
    cat <<EOF
Commit States:
  —   Untracked   Commit has no git-sd-id yet; assign, move or fixup tags it.
      Ready       Commit has a git-sd-id and is ready to be assigned to a Stack Set.
                  Action: Run 'git sd assign <set-name>:<index-list>' to group it.
  📦  Assigned    Commit is assigned to a Stack Set and has no PR yet.
                  Action: Run 'git sd sync' to create/update the PR.
  📤  Unsynced    A sub-commit that is not in its PR yet: added, changed or moved into the PR since the last sync.
                  Action: Run 'git sd sync' to put it in the PR.

PR States:
  Each commit shows the first state below that applies; Merged and Closed always win.
  ❓  Unknown     The PR's state could not be read (the lookup failed); the PR itself may be fine.
                  Action: Re-run 'git sd status'.
  🔄  Syncing     A 'git sd sync' or 'git sd merge' is running on this Stack Set right now (seen from another terminal).
  💥  Conflict    GitHub reports a merge conflict between the PR and its base branch; it cannot be merged as it is.
                  Action: Rebase onto the updated base and resolve the conflicts ('git sd rebase', or a standard 'git rebase'), then 'git sd sync'.
  ❌  Changes     Address the reviewer's feedback.
                  Action: Create a fix commit, then 'git sd fixup <target-index>' to attach it as the next sub-commit of that PR (or 'git sd move' for manual placement/reordering).
  🚨  CI failed   Code or tests failed.
                  Action: Fix the code and sync.
  🟡  CI running  Wait for results.
  🚧  Draft       The PR is a work-in-progress; it can't be merged yet.
  👀  Review      Waiting for a review (none yet, or more required).
  ✅  Approved    Ready to merge.
  🚢  Merged      Final state; the PR landed.
  🌑  Closed      The PR was closed without merging.
EOF
}

show_status_untracked() {
    status_header
    row "—" "Untracked" "0" "" "" "a1b2c3" "Initial layout" ""
    row "—" "Untracked" "1" "" "" "b2c3d4" "Add sidebar" ""
    row "—" "Untracked" "2" "" "" "c3d4e5" "Implement navigation" ""
}

show_status_initial() {
    status_header
    row "" "Ready" "0" "" "s9876" "a1b2c3" "Initial layout" ""
    row "" "Ready" "1" "" "s8765" "b2c3d4" "Add sidebar" ""
    row "" "Ready" "2" "" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_assigned() {
    status_header
    row "📦" "Assigned" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" ""
    row "📦" "Assigned" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" ""
    row "📦" "Assigned" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_syncing_all() {
    status_header
    row "🔄" "Syncing" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" ""
    row "🔄" "Syncing" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" ""
    row "🔄" "Syncing" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_pushed() {
    status_header
    row "👀" "Review" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👀" "Review" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👀" "Review" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
}

show_status_pushed_interleaved() {
    status_header
    row "👀" "Review" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👀" "Review" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" "https://github.com/user/repo/pull/105"
    row "👀" "Review" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "📤" "Unsynced" "2.1" "" "s1111" "d1e2f3" "└─ Fix sidebar padding (Feedback)" ""
    row "👀" "Review" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
}

# feat-a:0 (PR #100) and bugfix-b:0 (PR #105) merged and dropped locally --
# everything else shifts down and retargets onto main. feat-a itself is NOT
# fully finished (PR #101/#102 still open), so it stays a live Stack Set.
show_status_after_partial_merge() {
    status_header
    row "👀" "Review" "0" "feat-a:0" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "" "" "0.1" "" "s1111" "d1e2f3" "└─ Fix sidebar padding (Feedback)" ""
    row "👀" "Review" "1" "feat-a:1" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
}

# A brand-new commit ("Add search filters") joined feat-a's back after the
# partial merge + rebase/sync -- proof the Stack Set kept living.
show_status_living_stack_extended() {
    status_header
    row "👀" "Review" "0" "feat-a:0" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "" "" "0.1" "" "s1111" "d1e2f3" "└─ Fix sidebar padding (Feedback)" ""
    row "👀" "Review" "1" "feat-a:1" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "👀" "Review" "2" "feat-a:2" "s3333" "e5f6a7" "Add search filters" "https://github.com/user/repo/pull/106"
}

# --- Walkthrough ---

run_walkthrough() {
    header "0: Usage Information"
    type_out "git sd"
    show_usage
    wait_for_user

    header "1: Startup & Initial Status"
    type_out "git sd status"
    info "'status' is read-only: fresh commits show as Untracked until assign, move or fixup tags them."
    show_status_untracked
    wait_for_user

    header "2: Configuring a Custom Base Branch (Testing)"
    info "sd.base-branch is a repo-level config: it lets git-sd target any branch instead of main/master."
    info "This is useful for testing against a real repo whose main has approval guards you don't want to fight while iterating."
    type_out "git config sd.base-branch origin/test-integration"
    echo "(no output; this is a plain 'git config' write)"
    type_out "git sd status"
    info "Merge-base calculations, and every PR git-sd creates or merges from here on, now target 'test-integration' instead of main."
    show_status_untracked
    wait_for_user

    header "3: Displaying the Legend"
    type_out "git sd status --legend"
    show_legend
    echo ""
    show_status_untracked
    wait_for_user

    header "4: Stack Set Assignment"
    type_out "git sd assign feat-a:0-2"
    echo "3 commits assigned to 'feat-a'."
    type_out "git sd status"
    show_status_assigned
    wait_for_user

    header "5: Syncing (Consolidated Sync Command)"
    type_out "git sd sync feat-a"
    echo "Created PR #100  [feat-a]  Initial layout  https://github.com/user/repo/pull/100"
    echo "Created PR #101  [feat-a]  Add sidebar  https://github.com/user/repo/pull/101"
    echo "Created PR #102  [feat-a]  Implement navigation  https://github.com/user/repo/pull/102"
    echo "Sync completed."
    info "Sync creates PRs and updates branches in one shot, and tells you which PRs it created or updated -- no need to run status just to find the URLs."
    sleep 1
    type_out "git sd status"
    show_status_syncing_all
    sleep 1
    type_out "git sd status"
    show_status_pushed
    wait_for_user

    header "6: Interleaving & PR Feedback (Move / Fixup)"
    info "Developer creates bugfix-b and a feedback commit for feat-a:1."
    type_out "git sd status"
    status_header
    row "👀" "Review" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👀" "Review" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👀" "Review" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "Ready" "3" "" "s2222" "f4e5d6" "Fix race condition in auth" ""
    row "" "Ready" "4" "" "s1111" "d1e2f3" "Fix sidebar padding" ""
    
    type_out "git sd assign bugfix-b:3"
    echo "commit 3 assigned to 'bugfix-b'."
    type_out "git sd move 3:1"
    echo "Moving commit 3 to index 1..."
    type_out "git sd status"
    status_header
    row "👀" "Review" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "📦" "Assigned" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" ""
    row "👀" "Review" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👀" "Review" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "Ready" "4" "" "s1111" "d1e2f3" "Fix sidebar padding" ""

    info "Commit 4 is the fix, sitting at HEAD -- 'fixup' attaches it without any index/rank math."
    type_out "git sd fixup 2"
    echo "Attached as sub-commit 2.1."
    type_out "git sd status"
    show_status_pushed_interleaved
    wait_for_user

    header "7: Promoting a Sub-commit & Conflict Handling"
    info "Developer tries to move the sub-commit to a position that causes a conflict."
    type_out "git sd move 2.1:0"
    echo "Moving commit 2.1 to index 0..."
    echo -e "\e[1;31mError: Conflict while reordering.\e[0m"
    echo "CONFLICT (content): Merge conflict in main.go"
    echo "Resolve it, then run 'git rebase --continue' to finish, or 'git rebase --abort' to restore the original order."
    wait_for_user
    info "Developer resolved the conflict and finished the reorder with plain git."
    type_out "git rebase --continue"
    type_out "git sd status"
    show_status_pushed_interleaved
    wait_for_user

    header "8: Merging the Bottom of a Stack (Partial Merge)"
    info "PR #100 (the bottom of feat-a) and PR #105 (bugfix-b's only PR) are approved. Nothing else is ready yet."
    info "'--upto 0' merges just feat-a's bottom PR -- not the whole stack -- via GitHub's async stack-merge API."
    type_out "git sd merge feat-a --upto 0"
    echo "Merging stack set 'feat-a' up to PR #100..."
    echo "Merge completed successfully."
    echo "Run 'git sd rebase' to drop the merged commits locally."
    type_out "git sd merge bugfix-b"
    echo "Merging stack set 'bugfix-b' up to PR #105..."
    echo "Merge completed successfully."
    echo "Run 'git sd rebase' to drop the merged commits locally."
    info "'merge' only talks to GitHub -- the local branch is untouched. 'rebase' verifies the merged content landed and drops it locally."
    type_out "git sd rebase"
    echo "Rebased onto origin/main: dropped 2 landed PR(s), retired 2 commit(s)."
    type_out "git sd status"
    info "The merged commits are dropped locally and everything re-indexes; #101/#102 auto-retarget onto main."
    show_status_after_partial_merge
    wait_for_user

    header "9: Living Stack Sets - Reconciling Mid-Lifecycle & Continued Work"
    info "The 'git sd rebase' above already dropped and retired the merged commits (it works the same for PRs merged in GitHub's UI)."
    info "Running it again at any point is safe; with nothing new to reconcile it does nothing."
    type_out "git sd rebase"
    echo "Already up to date."
    info "If a merged PR also had local changes that never landed (e.g. amended after its last sync), 'rebase' keeps just those as one follow-up commit (shown for a different run):"
    type_out "git sd rebase"
    echo "Kept local changes from PR #101 as a new commit (assigned to 'feat-a'): 1 file changed, 4 insertions(+); run 'git sd sync' to open a PR."
    echo "Rebased onto origin/main: dropped 1 landed PR(s), retired 1 commit(s)."
    info "If a surviving commit conflicts with the updated base, 'rebase' leaves a standard git rebase in progress (shown here for a different run):"
    type_out "git sd rebase"
    echo -e "\e[1;31mError: Conflict while rebasing onto origin/main.\e[0m"
    echo "Stopped at: c3d4e5 \"Implement navigation\" (PR #102, set 'feat-a')"
    echo "Conflicting files:"
    echo "  src/nav.go"
    echo "Resolve it with plain git, then:"
    echo "  git add <files> && git rebase --continue   finish the rebase"
    echo "  git rebase --skip                          drop this commit (treat it as landed)"
    echo "  git rebase --abort                         restore your branch exactly as it was"
    type_out "git add src/nav.go && git rebase --continue"
    type_out "git sd status"
    echo "Finished the rebase: retired 1 commit(s)."
    show_status_after_partial_merge
    info "git sd settles the bookkeeping (retiring landed PRs) the next time it runs; --abort instead discards the backup and changes nothing."
    info "Retired PRs' remote branches are deleted by the next sync -- after it retargets the survivors onto main."
    type_out "git sd sync feat-a"
    echo "Deleted 1 finished branch."
    echo "Updated PR #101  [feat-a]  Add sidebar  https://github.com/user/repo/pull/101"
    echo "1 PR unchanged."
    echo "Sync completed."
    info "PR #100's branch is gone, but #101/#102 -- and the 'feat-a' Stack Set itself -- are untouched."
    type_out "git sd status"
    show_status_after_partial_merge
    wait_for_user

    info "A Stack Set isn't a one-shot construct: a new commit can still join feat-a from here."
    status_header
    row "👀" "Review" "0" "feat-a:0" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "" "" "0.1" "" "s1111" "d1e2f3" "└─ Fix sidebar padding (Feedback)" ""
    row "👀" "Review" "1" "feat-a:1" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "Ready" "2" "" "s3333" "e5f6a7" "Add search filters" ""
    type_out "git sd assign feat-a:2"
    echo "commit 2 assigned to 'feat-a'."
    type_out "git sd sync feat-a"
    echo "Created PR #106  [feat-a]  Add search filters  https://github.com/user/repo/pull/106"
    echo "2 PRs unchanged."
    echo "Sync completed."
    type_out "git sd status"
    show_status_living_stack_extended
    wait_for_user

    header "10: Merging the Rest & Final Cleanup"
    info "The remaining feat-a PRs are approved. A plain 'merge' (no --upto) ships the whole remaining stack."
    type_out "git sd merge feat-a"
    echo "Merging stack set 'feat-a' up to PR #106..."
    echo "Merge completed successfully."
    echo "Run 'git sd rebase' to drop the merged commits locally."
    type_out "git sd rebase"
    echo "Rebased onto origin/main: dropped 3 landed PR(s), retired 4 commit(s)."
    type_out "git sd status"
    echo "No local commits found since main."
    type_out "git sd sync --all"
    echo "Deleted 4 finished branches."
    echo "Stack Set 'bugfix-b' fully finished; dissolved."
    echo "Stack Set 'feat-a' fully finished; dissolved."
    echo "Sync completed."
    wait_for_user
}

# --- Execution ---

run_walkthrough

echo -e "\n\e[1;32mSimulation Complete!\e[0m"
