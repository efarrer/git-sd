package main

import "fmt"

// showAgentUsage prints a complete, machine-oriented command reference:
// every command's exact argument grammar, options, behavior, output, and
// error conditions, so an agent can drive git-sd correctly without reading
// the source or SPEC.md. Kept in its own file since it's substantially more
// detailed (and longer) than showUsage()'s human-facing summary.
func showAgentUsage() {
	fmt.Print(`git-sd agent reference

OVERVIEW
  git-sd manages a stacked-diff workflow: a linear sequence of commits on the
  current branch. Each commit is referenced everywhere by its 0-based
  PHYSICAL POSITION in 'git sd status' output (the "Index" column), NOT by
  hash or git-sd-id — physical order is the source of truth. Commits may be
  grouped into a named "Stack Set" (1-10 alphanumeric/hyphen characters) and
  pushed as GitHub Pull Requests, one PR per top-level commit (plus any
  sub-commits folded into it).

GLOBAL CONVENTIONS
  - Exit code 0 on success, non-zero on any error.
  - Every command except 'status' prints exactly one line of output on
    success (e.g. "Commits tracked."). Errors print one or more lines
    starting with "Error: " (or "Warning: " for non-fatal issues) and set a
    non-zero exit code. A conflict during 'move'/'fixup' leaves a git
    rebase in progress for manual resolution ('git rebase --continue' to
    finish, 'git rebase --abort' to restore the original order) — do not
    retry automatically; surface it. 'rebase' behaves the same way
    ('git rebase --skip' drops the conflicting commit as landed). Other
    state-changing commands refuse while that rebase is in progress.
  - GitHub CLI errors are consistent: when a command needs 'gh' and it is
    missing, the error is "'gh' CLI not found. Please install it to use
    git-sd."; when it is not logged in or its token is invalid or expired,
    "You are not authenticated with 'gh'. Please run 'gh auth login'.".
    There is no startup check: track, assign, move, fixup and help work
    without gh, and 'status' with PRs to look up but no usable gh prints
    its table plus one "Warning: PR states unavailable: ..." and exits 0.
  - A commit's identity trailer (git-sd-id) is added by 'track', or
    implicitly by 'assign' and 'move' (both track any untracked commits in
    range before acting). 'status' never mutates commits — a commit with no
    id yet shows Status '—' (Untracked).
  - Commit states, in order: Untracked ('—') -> Ready (blank Status, has an
    id, no Stack Set) -> Assigned ('📦', has a Stack Set, no PR yet) ->
    has-a-PR (one of the PR-state emoji below) -> Merged.
  - PR-state emoji (status column, once a commit has a PR): ❓ PR state
    unavailable (the lookup failed; status ends with one warning naming
    the PR(s) and the error, and still exits 0), 🔄 sync/merge in
    progress, 💥 conflict/base mismatch, ❌ changes requested, 🚨 CI failing,
    🟡 CI running, 🚧 draft, 👀 review required (none yet or more needed), ✅ approved, 🚢 merged,
    🌑 closed.
  - "Base branch" = 'sd.base-branch' git config if set (format
    "<remote>/<branch>"), else the sole/origin remote's main branch; commands
    fail with a clear error if it can't be inferred and isn't set. All
    indices are relative to the merge-base with this branch.

COMMANDS

track
  Usage:       git sd track
  Arguments:   none (any given are ignored)
  Behavior:    Assigns a git-sd-id trailer to every commit since the base
               branch that doesn't already have one. Idempotent — a second
               run with no new commits is a no-op.
  Output:      "Commits tracked."
  Use when:    You want commits to show as Ready in 'status' without
               performing any other action. Not required before 'assign' or
               'move' — both self-track.

status
  Usage:       git sd status [--legend] [--no-legend]
  Arguments:   none
  Options:
    --legend      Force-show the Status/PR-state emoji legend above the table
                  (also shown when there are no commits).
    --no-legend   Force-hide it. (Default comes from git config
                  'sd.status.legend'; unset = false = hidden.)
  Behavior:    Read-only. Lists every commit since the base branch. Never
               adds a git-sd-id or changes a Stack Set/PR assignment; it does
               refresh each tracked commit's recorded physical Index to match
               current order.
  Output:      A table with columns Icon (blank header: the state's emoji),
               Status (the state's word, e.g. Assigned, Review, Approved,
               Ready), Index, Stack Set, git-sd-id, Hash, Message, PR
               (choose and order them via git config 'sd.status.columns';
               an unknown column name is an error). A sub-commit has a blank
               Icon/Status and an indented Message, unless it is Unsynced
               (📤: not in its PR yet; run 'git sd sync'). Index is
               "N" for a top-level commit, or "N.R" for a sub-commit, where N
               is its PARENT's current physical index and R is its rank (an
               opaque digit string, e.g. "1", "45" — see SPEC.md's
               "Hierarchical Indexing"; compare/copy it exactly as shown,
               never treat it as a plain integer). If there are no commits
               since the base branch:
               "No local commits found since <base-branch>."
  Config:
    sd.status.legend  [true|false]  default false
    sd.status.columns [string]      default "Icon,Status,Index,Stack Set,git-sd-id,Hash,Message,PR"
    sd.status.color   [true|false]  default true (terminal only; NO_COLOR/TERM=dumb turn it off)

assign
  Usage:       git sd assign <assignment>...
  Arguments:   One or more <assignment> strings (space-separated, each
               processed independently, in order given):
                 <set-name>:<index-list>   assign/reassign to a Stack Set
                 :<index-list>             unassign (remove from any Stack Set)
               <index-list> is comma-separated; each comma-separated item is
               one of:
                 N       a single physical index (a top-level commit)
                 A-B     an inclusive range of physical indices
                 N.R     addresses an EXISTING sub-commit (N = its parent's
                         current index, R = its exact rank as shown in
                         'status'). assign can never CREATE a sub-commit this
                         way — only 'move'/'fixup' do that. This form either
                         no-ops (if <set-name> already equals the parent's
                         Stack Set — a consistency confirmation) or errors,
                         directing you to 'git sd move N.R:<dest>' to promote
                         it to top-level first, then assign it separately.
               Assigning/unassigning a top-level commit (N or A-B form)
               cascades: every existing sub-commit of that commit has its
               Stack Set re-stamped to match (including to "" on unassign) —
               no separate step needed.
               Reassigning a commit already in Set A to Set B moves it
               implicitly (no need to unassign first).
  Behavior:    Tracks any untracked commits in range first, then applies
               each assignment.
  Output:      "Commits assigned successfully."
  Errors:      Missing argument; malformed "<set>:<indices>" (no colon);
               out-of-range index; N.R addressing a sub-commit that doesn't
               exist, or one whose parent is in a different Stack Set
               (skipped with a printed error, other valid assignments in the
               same call still apply).
  Examples:
    git sd assign feat-a:0-2
    git sd assign feat-a:0,2,4
    git sd assign :1                (unassign index 1, cascades to its subs)
    git sd assign feat-a:0 bugfix-b:1,3-4   (multiple assignments in one call)

move
  Usage:       git sd move <src>:<dest>[.<rank>]
  Arguments:   Exactly one positional argument, colon-separated:
                 <src>    physical index of a top-level commit, OR N.R
                          addressing an existing sub-commit (N = its parent's
                          index, R = its exact rank from 'status')
                 <dest>   physical index of a TOP-LEVEL commit (never a
                          sub-commit's address, even when reparenting one)
                 <rank>   optional. Omitted: <src> is reordered to physical
                          position <dest> AS A TOP-LEVEL COMMIT (if it was a
                          sub-commit, it's promoted — Stack Set unchanged).
                          Given: <src> is attached/reattached as a
                          sub-commit of the commit at <dest>, with this
                          exact rank, always inheriting <dest>'s Stack Set
                          (which may be "" — the attachment sits dormant
                          until <dest> itself gets assigned). Reject if
                          <dest> is itself a sub-commit (no 3-level nesting),
                          or <dest> == <src>.
  Behavior:    Tracks any untracked commits in range first (so the moved
               commit always has an id). <dest> is an INSERTION point, not a
               swap target: whatever currently sits at <dest> or later shifts
               later to make room (e.g. moving a commit to index 2 pushes
               what was at 2 to 3, 3 to 4, ...). If <src> is a top-level
               commit with its own sub-commits, the whole group (parent +
               subs, in their existing order) moves together as one block —
               others shift by the group's size, not just one; the subs'
               ParentID/SubRank never change, since their displayed label is
               derived from the parent's CURRENT position, not stored.
               Physically rebuilds the affected commit range via a real
               'git rebase -i' (one pick per commit in the new order), which
               can genuinely conflict if content actually
               depends on physical order. A ranked move inserts <src>
               immediately after whichever of <dest>'s existing sub-commits
               has the largest rank still less than <rank> (or after <dest>
               itself if none) — this is how reordering siblings works:
               'move 0.1:0.9' relabels/repositions an existing sub-commit
               among its siblings.
  Output:      "Commits reordered successfully." On conflict: a multi-line
               error; a git rebase is left in progress — resolve the
               conflict and run 'git rebase --continue', or run
               'git rebase --abort' to restore the original order, then run
               'git sd status'. Other mutating commands refuse while it is
               in progress. A validation failure (bad
               address, 3-level nesting, dest==src) leaves the repo
               untouched.
  Examples:
    git sd move 2:0          reorder: commit at index 2 becomes index 0
    git sd move 3:1.1        attach commit at index 3 as sub-commit "1.1" of
                              index 1's PR (index 1 may or may not be
                              assigned yet — dormant either way)
    git sd move 0.1:5        promote sub-commit "0.1" back to a top-level
                              commit at index 5 (Stack Set unchanged)
    git sd move 0.1:0.9      reorder sub-commit "0.1" to rank "9" (0.9),
                              still under the same parent (index 0)

fixup
  Usage:       git sd fixup <target-index>
  Arguments:   <target-index>  physical index of a top-level commit already
                                assigned to a Stack Set.
  Behavior:    Sugar for 'git sd move <index of HEAD>:<target-index>.<rank>'
               — always takes HEAD as the source, and computes <rank>
               automatically as the next rank after the target's current
               highest sub-commit rank (see SPEC.md's fixup section for the
               auto-rank algorithm). Use 'move' directly for anything more specific
               (a different source, an explicit rank, reordering).
  Output:      "Attached as sub-commit <target-index>.<rank>."
  Errors:      <target-index> not assigned to any Stack Set; <target-index>
               is HEAD itself; HEAD is a top-level commit that already has
               its own sub-commit(s) (attaching it elsewhere would orphan
               them — use 'move' for that manual case). A conflict during
               the underlying rebuild leaves a git rebase in progress, same as
               'move'.
  Examples:
    git sd fixup 0        attach HEAD as the next sub-commit of index 0's PR

sync
  Usage:       git sd sync [<set-name> | --all]
  Arguments:   Optional. Omitted or "--all": process every Stack Set. A
               specific <set-name>: process only that one.
  Behavior:    Three passes: (0) dissolve finished sets (an existing GitHub
               Stack of a live set is kept, and only torn down just before
               a PR's base must change), (1) cherry-pick each group's
               own commits (git-sd-id trailer stripped) onto a fresh chain,
               push, create/update its PR, (2) register the set's PR
               chain as a GitHub Stack if its registration is missing or no
               longer matches (only if it has 2+ PRs — a lone PR is left
               unregistered). Pass 0 also dissolves a set in scope
               whose commits have all been retired (removes it, no
               re-registration). Between Passes 1 and 2, deletes the remote
               branches of commits retired by 'rebase'/'merge' (only now,
               after surviving PRs have been retargeted). A branch that any
               open PR still uses as its base is kept queued instead, with
               "Warning: keeping finished branch <b> — open PR(s) #<n> are
               still based on it", and retried on the next sync. A per-group
               push/PR error is printed and that group is skipped; sync
               continues with the rest rather than aborting.
  Output:      What changed: one line per PR created or updated,
               "Created PR #<n>  [<set>]  <title>  <url>" or "Updated PR
               #<n>  [<set>]  <title>  <url>", then "<n> PR(s) unchanged."
               if any were left as they were, then "Deleted <n> finished
               branch(es)." if remote branches of retired commits were
               deleted, then "Stack Set '<name>' fully finished;
               dissolved." for each set removed because all its commits
               are gone, then "Sync completed.". Merged/closed PRs and
               groups that failed are not counted. A failed group or step
               prints "Error: could not <action>: <reason>", sync carries
               on with the rest, and the closing line becomes "Sync
               completed with <n> error(s)." with exit status 1 (warnings
               don't change the exit status).
               A PR's title, body and base are sent to GitHub only when
               they changed since the last sync, so re-syncing an unchanged
               stack makes no PR edits (a title edited by hand on GitHub is
               not overwritten until the local commit message or base
               changes).
  Examples:
    git sd sync              sync every Stack Set
    git sd sync feat-a       sync only 'feat-a'

merge
  Usage:       git sd merge [<set-name>] [--upto <index>] | --all
  Arguments:
    <set-name>      required unless --all is given
    --upto <index>  optional; limits the merge to the BOTTOM PREFIX of the
                    set — top-level commits with physical index <= <index>.
                    Cannot be combined with --all. Every group at or below
                    the target must already have a PR (be synced), or the
                    command errors asking you to sync first.
    --all           merge every Stack Set (each up to its own top); cannot
                    combine with --upto.
  Behavior:    Submits the top-most in-range PR to GitHub's async
               stack-merge API, which atomically merges every PR up through
               it, retargeting everything above onto the new base
               server-side. Approval/CI gating is entirely GitHub branch
               protection — git-sd does not check it itself; a rejected
               merge surfaces GitHub's own error. GitHub only: never
               touches the local branch or working tree (works with
               uncommitted changes). Run 'git sd rebase' afterwards to
               drop the merged commits locally and retire their metadata;
               their remote branches are deleted by the next 'sync'.
  Output:      "Merging stack set '<name>' up to PR #<n>...", "Merge
               completed successfully." per set (or, with --all, per set
               that succeeds — failures print "Skipping stack set '<name>':
               <reason>" and processing continues), then a final line
               telling you to run 'git sd rebase' to drop the merged
               commits locally.
  Errors:      No set name and no --all; --upto combined with --all;
               --upto given a non-integer or negative value; --upto's cap
               below the first synced group.
  Examples:
    git sd merge feat-a
    git sd merge feat-a --upto 1
    git sd merge --all

rebase
  Usage:       git sd rebase [--rollback [<n>] | --list]
  Arguments:   none, or one of:
    --rollback [<n>]  restore backup <n> (1 = most recent, the default)
    --list            print available backups, newest first:
                      "<n>  <timestamp>  <short-hash>" per line, or
                      "No rebase backups."
  Behavior:    Strictly local — never pushes, edits PRs, or deletes remote
               branches. Refuses to run with uncommitted changes. Fetches
               the base branch, then for each synced group:
                 MERGED  compares the group with what landed by NET content
                         (2 local commits vs 1 squashed commit is fine; the
                         merge commit must be on the base branch): fully
                         represented upstream (identical, or upstream has
                         more) -> dropped and its tracking retired; local
                         has extra changes that never landed -> replaced by
                         ONE follow-up commit carrying only those changes,
                         kept at the group's position, assigned to the same
                         Stack Set with a new git-sd-id and no PR (run
                         'git sd sync' to open one); local and upstream
                         changed the same (or adjacent) lines differently -> a git rebase
                         conflict on the follow-up commit. A merge commit
                         not on the base, or a group that can't be replayed,
                         aborts the whole command with nothing changed.
                 CLOSED  commits stay on the branch; metadata is retired
                         (they become Ready).
                 other   untouched.
               Records a backup (HEAD + full metadata copy; at most 10 kept),
               rebuilds the remaining commits onto the base (real
               git rebase; commits whose change is already upstream are
               dropped as landed), then retires the metadata of dropped/closed
               commits and of any tracked commit no longer in local history.
               A still-open or never-synced commit is never retired (B-018).
               Retired top-level commits' remote branches are queued for the
               next 'sync' to delete. On a rebuild conflict, a git rebase is
               left in progress naming the conflicting commit and files:
               'git rebase --continue' to finish (landed PRs are then
               retired), 'git rebase --skip' to drop that commit as landed,
               'git rebase --abort' to restore the branch exactly and
               discard the backup. The command exits non-zero.
               --rollback resets HEAD to the backup's commit and restores its
               metadata together, then removes that backup (older ones stay).
  Output:      One "Kept local changes from PR #<n> as a new commit ..." line
               per follow-up commit, then "Rebased onto <base>: dropped <n>
               landed PR(s), retired <m> commit(s)." / "Already up to date."
               / "Rolled back to backup #<n> (<timestamp>, <short-hash>)."
  Errors:      Uncommitted changes; a MERGED PR whose merge commit is not on
               the base branch, or whose group cannot be replayed to compare
               (names the PR — nothing changed); a PR state lookup failure
               (nothing changed); rebuild conflict (rebase left in progress);
               --rollback <n> with no such backup.
  Examples:
    git sd rebase               after PRs were merged in GitHub's UI
    git sd rebase --list

watch
  Usage:       git sd watch
  Arguments:   none
  Behavior:    Long-running and READ-ONLY: it never changes the branch, the
               metadata or GitHub. Polls GitHub for the PRs of the current
               branch (the same live lookup 'status' uses) until stopped with
               Ctrl-C, and reports every change of a PR's GitHub state (CI
               running -> CI failed/passed, Review -> Approved, Conflict,
               Merged, Closed, ...). Local-only states and CI progress are not
               changes. Poll interval: 'sd.watch.min-interval' (5s) after a
               change, doubling while nothing changes up to
               'sd.watch.max-interval' (60s; 20s while CI is running).
  Output:      On a terminal: a table redrawn in place plus recent changes
               and a footer. Otherwise: the table once, then one line per
               event: "<HH:MM:SS>  <set> <pos>/<size>  PR #<n>  <from> -> <to>
               <title>", plus
               "Now watching PR #<n>" for a PR seen for the first time,
               "Cannot reach GitHub (...); still watching, will keep retrying."
               after 3 failed polls in a row, and "Back online.". Ends with
               "Stopped watching." (exit 0) on Ctrl-C/SIGTERM.
  Notify:      Terminal bell once per cycle with changes (terminal only), and
               'sd.watch.notify-command' (run with sh -c per change) with the
               environment GIT_SD_TITLE (short: "feat-a 3/5: Approved"),
               GIT_SD_MESSAGE (a sentence naming the PR: "CI passed on PR 3
               of 5 in feat-a (#120), now Approved: <title>"), GIT_SD_EVENT
               (ci-running, ci-passed, ci-failed, approved,
               changes-requested, conflict, merged, closed, draft,
               ready-for-review, state-changed, offline), GIT_SD_STACK_SET,
               GIT_SD_POSITION, GIT_SD_SET_SIZE, GIT_SD_PR_NUMBER,
               GIT_SD_PR_URL, GIT_SD_PR_TITLE, GIT_SD_FROM and GIT_SD_TO.
  Errors:      A problem on the first poll (no usable gh, no base branch, no
               metadata, invalid interval settings) prints "Error: ..." and
               exits 1. Later GitHub outages are retried, not fatal.
  Examples:
    git sd watch
    git config sd.watch.notify-command 'notify-send "$GIT_SD_TITLE" "$GIT_SD_MESSAGE"'
    git sd rebase --rollback    undo the most recent rebase
    git sd rebase --rollback 2

TYPICAL AGENT WORKFLOW
  1. git sd status                    inspect current commits/state
  2. git sd assign <name>:<indices>   group commits into a Stack Set
  3. git sd sync <name>               push branches, create/update PRs
  4. git sd status                    confirm PR URLs and review state
  5. git sd merge <name>              once approved (respects branch protection);
                                      GitHub only, local branch untouched
     (or merge the PRs in GitHub's UI instead)
     git sd rebase                    drop landed commits, retire their tracking
  6. git sd sync <name>               retarget survivors, delete finished
                                      branches, dissolve a fully finished set
                                      (safe mid-lifecycle — a Stack Set can
                                      keep growing after a partial merge)

  PR-feedback fixup workflow (make the fix, it lands at HEAD):
  1. git sd fixup <target-index>      attach HEAD as the next sub-commit of
                                      that PR
  2. git sd sync <name>               push the updated PR

CONFIGURATION (git config; see 'git config' for project scope, 'git config
--global' for home scope)
  sd.status.legend    [true|false]  default false
  sd.status.columns   [string]      default "Icon,Status,Index,Stack Set,git-sd-id,Hash,Message,PR"
  sd.status.color     [true|false]  default true (terminal only; NO_COLOR/TERM=dumb turn it off)
  sd.watch.notify-command [string]  default none (run per change by 'git sd watch')
  sd.watch.min-interval   [duration] default 5s (number of seconds, or e.g. 300ms)
  sd.watch.max-interval   [duration] default 60s
  sd.base-branch      [string]      "<remote>/<branch>"; <remote> must be a
                                     configured git remote. If unset: the
                                     "origin" remote's main branch if origin
                                     exists, else the sole remote's main
                                     branch if exactly one remote is
                                     configured; otherwise commands fail
                                     asking you to set this explicitly.
`)
}
