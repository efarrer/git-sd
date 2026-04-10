#!/bin/bash

# sd-sim: High-fidelity simulation of the git-sd workflow.
# This script walks through scenarios defined in SPEC.md.

SCENARIO="${1:-walkthrough}"

# --- Helpers ---

# Row Helper to handle Emoji width issues
# Usage: row <status> <index> <stack-set> <sd-id> <hash> <message> <pr>
row() {
    local status="$1"
    local index="$2"
    local set="$3"
    local sdid="$4"
    local hash="$5"
    local msg="$6"
    local pr="$7"
    
    # We use explicit spacing to ensure the Index column starts at col 8.
    # 01234567
    # Status  Index
    if [ -n "$status" ]; then
        # Most terminal emojis are 2 cells.
        # However, 👁 (eye) can be tricky. 
        local pad="      " # Default 6 spaces for 2-wide emoji
        if [[ "$status" == "👁" ]]; then
            pad="       " # 7 spaces
        fi
        printf "%s%s%-7s%-15s%-11s%-8s%-29s%s\n" "$status" "$pad" "$index" "$set" "$sdid" "$hash" "$msg" "$pr"
    else
        printf "        %-7s%-15s%-11s%-8s%-29s%s\n" "$index" "$set" "$sdid" "$hash" "$msg" "$pr"
    fi
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
  assign      Assign commits to a Stack Set
  move        Reorder commits or manage sub-commits (PR feedback)
  sync        Synchronize GitHub PR state with local branch
  merge       Safely merge approved PRs in a stack
  cleanup     Remove metadata for merged Stack Sets

Options:
  --all       Apply command to all active Stack Sets (sync, merge, cleanup)
  --legend    Show the status emoji legend (status)

Configuration (via git config):
  sd.status.legend [true|false]      Always show the legend in 'status'
  sd.status.columns [string]         Comma-separated list of columns to show/order
  sd.status.color [true|false]       Enable colorized output
  sd.pr.required-reviewers [n]       Approvals needed for ✅ status
  sd.pr.branch-name-format [string]  Format for branches (requires {stack-set-name} and either {index} or {git-sd-id})
EOF
}

show_legend() {
    cat <<EOF
Commit States:
      Ready: Commit has an git-sd-id and is ready to be assigned to a Stack Set.
        Action: Run 'git sd assign <set-name>:<index-list>' to group it.
  📦  Assigned: Commit is assigned to a Stack Set.
        Action: Run 'git sd sync' to create/update the PR.

PR States (Prioritized Display):
  🔄  In Progress: A local 'sync' operation is currently active.
  💥  Conflict/Base Mismatch: Critical; commit cannot be applied to target base.
        Action: Run standard 'git rebase' to fix conflicts, then 'git sd sync'.
  ❌  Changes Requested: High priority; address feedback.
        Action: Create a feedback commit and move it using 'git sd move <src>:<dest>.1'.
  🚨  CI Failing: High priority; code or tests failed.
        Action: Fix the code and sync.
  🟡  CI Running: Intermediate; wait for results.
  👁   Review Required: Intermediate; ready for approval.
  ✅  Approved: Low priority; ready to merge.
  🚧  Draft: PR is a work-in-progress.
  🚢  Merged: Final state.
  🌑  Closed: Abandoned.
EOF
}

show_status_initial() {
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "" "0" "" "s9876" "a1b2c3" "Initial layout" ""
    row "" "1" "" "s8765" "b2c3d4" "Add sidebar" ""
    row "" "2" "" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_assigned() {
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "📦" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" ""
    row "📦" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" ""
    row "📦" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_syncing_all() {
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "🔄" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" ""
    row "🔄" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" ""
    row "🔄" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" ""
}

show_status_pushed() {
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "👁" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👁" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
}

show_status_pushed_interleaved() {
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "👁" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👁" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" "https://github.com/user/repo/pull/105"
    row "👁" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "" "2.1" "" "s1111" "d1e2f3" "└─ Fix sidebar padding (Feedback)" ""
    row "👁" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
}

# --- Walkthrough ---

run_walkthrough() {
    header "0: Usage Information"
    type_out "git sd"
    show_usage
    wait_for_user

    header "1: Startup & Initial Status"
    type_out "git sd status"
    info "Trailers are added automatically on first run."
    show_status_initial
    wait_for_user

    header "2: Displaying the Legend"
    type_out "git sd status --legend"
    show_legend
    echo ""
    show_status_initial
    wait_for_user

    header "3: Stack Set Assignment"
    type_out "git sd assign feat-a:0-2"
    echo "3 commits assigned to 'feat-a'."
    type_out "git sd status"
    show_status_assigned
    wait_for_user

    header "4: Syncing (Consolidated Sync Command)"
    type_out "git sd sync feat-a"
    echo "Initiating sync for feat-a..."
    info "Sync creates PRs and updates branches in one shot."
    sleep 1
    type_out "git sd status"
    show_status_syncing_all
    sleep 1
    type_out "git sd status"
    show_status_pushed
    wait_for_user

    header "5: Interleaving & PR Feedback (Move)"
    info "Developer creates bugfix-b and a feedback commit for feat-a:1."
    type_out "git sd status"
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "👁" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👁" "1" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "2" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "3" "" "s2222" "f4e5d6" "Fix race condition in auth" ""
    row "" "4" "" "s1111" "d1e2f3" "Fix sidebar padding" ""
    
    type_out "git sd assign bugfix-b:3"
    echo "commit 3 assigned to 'bugfix-b'."
    type_out "git sd move 3:1"
    echo "Moving commit 3 to index 1..."
    type_out "git sd status"
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "👁" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "📦" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" ""
    row "👁" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "4" "" "s1111" "d1e2f3" "Fix sidebar padding" ""

    type_out "git sd move 4:2.1"
    echo "Moving commit 4 into a sub-commit at 2.1..."
    type_out "git sd status"
    show_status_pushed_interleaved
    wait_for_user

    header "6: Promoting a Sub-commit & Conflict Handling"
    info "Developer tries to move the sub-commit to a position that causes a conflict."
    type_out "git sd move 2.1:0"
    echo "Moving commit 2.1 to index 0..."
    echo -e "\e[1;31mError: Conflict during automated rebase.\e[0m"
    echo "Conflict in main.go"
    echo "Aborting rebase and restoring branch state..."
    type_out "git sd status"
    show_status_pushed_interleaved
    type_out "git sd move 2.1:5"
    echo "Moving commit 2.1 to index 5..."
    type_out "git sd status"
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "👁" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "👁" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" "https://github.com/user/repo/pull/105"
    row "👁" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    row "" "4" "" "s1111" "d1e2f3" "Fix sidebar padding" ""
    wait_for_user

    header "7: Merging with git-sd (git-spr strategy)"
    info "PR #100 and #105 are approved. We use git sd merge to ship them."
    type_out "git sd status"
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "✅" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "✅" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" "https://github.com/user/repo/pull/105"
    row "👁" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    
    type_out "git sd merge --all"
    echo "Merging all mergable commits..."
    type_out "git sd status"
    echo "Status  Index  Stack Set      git-sd-id  Hash    Message                      PR"
    row "🚢" "0" "feat-a:0" "s9876" "a1b2c3" "Initial layout" "https://github.com/user/repo/pull/100"
    row "🚢" "1" "bugfix-b:0" "s2222" "f4e5d6" "Fix race condition in auth" "https://github.com/user/repo/pull/105"
    row "👁" "2" "feat-a:1" "s8765" "b2c3d4" "Add sidebar" "https://github.com/user/repo/pull/101"
    row "👁" "3" "feat-a:2" "s7654" "c3d4e5" "Implement navigation" "https://github.com/user/repo/pull/102"
    wait_for_user

    header "8: Final Cleanup"
    type_out "git sd cleanup --all"
    echo "Removing all local metadata and generated branches."
    type_out "git sd status"
    echo "No active stack sets."
    wait_for_user
}

# --- Execution ---

run_walkthrough

echo -e "\n\e[1;32mSimulation Complete!\e[0m"
