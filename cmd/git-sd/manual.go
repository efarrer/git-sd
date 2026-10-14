package main

import "strings"

// manualText is the full manual, printed by `git sd help` and `git-sd --help`.
// The notification variables and examples are filled in from the same tables
// that `notify-test` and `--agent` use, so none of them can drift.
func manualText() string {
	text := strings.NewReplacer(
		"{{NOTIFY_VARIABLES}}", indentLines(notifyVariablesTable(nil), strings.Repeat(" ", 14)),
		"{{NOTIFY_EXAMPLES}}", indentLines(notifyExamples, strings.Repeat(" ", 14)),
	).Replace(manual)
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ") // the generated tables pad their last column
	}
	return strings.Join(lines, "\n")
}

const manual = `NAME
       git-sd - manage a stacked-diff workflow with GitHub Pull Requests

SYNOPSIS
       git sd command [args...]

DESCRIPTION
       git-sd manages a stacked-diff workflow: a linear sequence of commits on
       the current branch. Each commit is referenced everywhere by its 0-based
       physical position in git sd status output (the "Index" column), not by
       hash or git-sd-id -- physical order is the source of truth. Commits may
       be grouped into a named Stack Set (1-10 alphanumeric/hyphen characters)
       and pushed as GitHub Pull Requests, one PR per top-level commit plus any
       sub-commits folded into it.

       Every command except status prints exactly one line of output on success.
       Errors print one or more lines starting with "Error: " and set a non-zero
       exit status. A conflict during move or fixup leaves a standard git rebase
       in progress: resolve it and run git rebase --continue, or run git rebase
       --abort to restore the original order. A conflict during rebase behaves
       the same way.

       A commit's identity trailer (git-sd-id) is added by track, or implicitly
       by assign and move (both track any untracked commits in range before
       acting).  status never mutates commits -- a commit with no id yet shows
       Status '--' (Untracked).

       Commit states, in order: Untracked (--) -> Ready (blank Status, has an
       id, no Stack Set) -> Assigned ([], has a Stack Set, no PR yet) -> has a
       PR (one of the PR-state indicators below) -> Merged.

       PR-state indicators shown in the Status column once a commit has a PR:
       state unavailable (the lookup failed; status then prints one warning), in
       progress, conflict/base mismatch, changes requested, CI failing, CI
       running, draft, review required, approved, merged, closed (run git sd
       status --legend to see the exact symbol for each).

COMMANDS
       track  Assigns a git-sd-id trailer to every commit since the base branch
              that doesn't already have one. Idempotent. Not required before
              assign or move, which both self-track; useful on its own to move
              commits from Untracked to Ready in status without performing any
              other action.

       status [--legend] [--no-legend]
              Lists every commit since the base branch: Icon (the state's emoji,
              blank header), Status (the state's word), Index, Stack Set, git-
              sd-id, Hash, Message, and PR columns. Read-only. Index is N for a
              top-level commit or N.M for a sub-commit attached to top-level
              index N.

              --legend
                     Force-show the Status/PR-state legend above the table (and
                     above the "No local commits" message or any error).

              --no-legend
                     Force-hide it (default comes from git config
                     sd.status.legend, unset = hidden).

       assign <assignment>...
              Assigns, reassigns, or unassigns commits to Stack Sets by index.
              One or more assignment arguments, each one of:

              <set-name>:<index-list>
                     assign/reassign to a Stack Set

              :<index-list>
                     unassign (remove from any Stack Set)

              index-list is comma-separated; each item is a single physical
              index (N), an inclusive range (A-B), or a direct sub-index
              assignment (N.M, which sets physical index N's sub-index to M and
              assigns it to <set-name> without validating that a top-level
              parent precedes it -- for that guarantee use move instead).
              Reassigning a commit already in one set to another moves it
              implicitly. Tracks any untracked commits in range first.

       move <source-index>:<destination-index>[.<sub-index>]
              Reorders commits and/or attaches a commit as a sub-commit. If sub-
              index is given, the commit is attached to the nearest top-level
              commit at or before destination-index in the resulting order,
              inheriting its Stack Set -- that commit must already be a top-
              level commit of a Stack Set, or the move fails with no changes
              made. Otherwise rebuilds the affected range via real cherry-picks,
              which can genuinely conflict if content depends on physical order.
              Tracks any untracked commits in range first.

       sync [<set-name> | --all]
              Synchronizes local branch state with GitHub: pushes each Stack
              Set's branch(es) and creates/updates its PR(s), then registers the
              set's PR chain as a GitHub Stack (once it has 2+ PRs). Also
              deletes the remote branches of commits retired by rebase or merge
              (after retargeting surviving PRs), and dissolves a Stack Set none
              of whose commits remain. Omitted or --all processes every Stack
              Set; a specific set-name processes only that one. After the passes
              it lists what changed, one line per PR created or updated (number,
              Stack Set, title, URL), then the number of PRs left unchanged. A
              PR's title, body and base are sent to GitHub only when they
              changed since the last sync.

       merge [<set-name>] [--upto <index>] | --all
              Submits the top-most in-range PR to GitHub's native async stack-
              merge API, which atomically merges every PR up through it and
              retargets everything above it onto the new base. Approval/CI
              gating is entirely GitHub branch protection. GitHub only: it never
              touches the local branch; run rebase afterwards to drop the merged
              commits locally.

              --upto index
                     Limit the merge to the bottom prefix of the set: top-level
                     commits with physical index <= index.  Every group at or
                     below the target must already be synced. Cannot combine
                     with --all.

              --all  Merge every Stack Set, each up to its own top. Cannot
                     combine with --upto.

              A set-name is required unless --all is given.

       rebase [--rollback [<n>] | --list]
              Reconciles the local branch after PRs have landed, however they
              were merged (squash, merge commit, or rebase-merge). Fetches the
              base branch and compares each merged PR with what landed by net
              content: fully represented upstream (identical, or upstream has
              more) -> dropped; local has extra changes that never landed ->
              replaced by one follow-up commit carrying only those changes, kept
              in the same Stack Set (run sync to open its PR); local and
              upstream changed the same lines differently -> a git rebase
              conflict on that follow-up commit.  The remaining commits are
              rebuilt onto the updated base. A merged PR whose merge commit is
              not on the base branch aborts the command with nothing changed. A
              closed PR's commits stay on the branch but lose their tracking.
              Strictly local: remote branches of finished PRs are deleted by the
              next sync.  Refuses to run with uncommitted changes. A backup
              (HEAD plus metadata) is recorded first; a conflict during the
              rebuild leaves a standard git rebase in progress (git rebase
              --continue, --skip or --abort).

              --rollback [<n>]
                     Restore backup n (1, the default, is the most recent):
                     resets the branch and restores metadata together.

              --list List the available backups, most recent first.

       watch  Follow the GitHub state of the PRs on the current branch until
              stopped with Ctrl-C, and report every change (CI running, CI
              failed or passed, approved, conflict, merged, closed) as it
              happens: on a terminal the screen is redrawn in place and the bell
              rings; otherwise one line per change is printed. It is read-only.
              It polls at sd.watch.min-interval after a change and backs off up
              to sd.watch.max-interval while nothing changes (20 seconds at most
              while CI is running). A change also runs sd.watch.notify-command
              if set, with the details in GIT_SD_* environment variables, so it
              can show a desktop notification. GitHub being unreachable is
              retried and announced, not fatal. Only one watch runs per
              repository: a second one refuses to start and names the lock file,
              .git/sd/watch.pid, which can be removed if the first is gone.
              While a watch is running, status and the commands that change
              something ask it to check now: it polls at once without changing
              its schedule (the wait restarts from the minimum only if the check
              finds a change). It still announces what it finds as usual, so a
              change that status just showed may be announced too.

       notify-test [--from state] [--to state] [--offline] [--list]
              Try sd.watch.notify-command without waiting for a real change. It
              builds a sample notification the way watch does (a made-up PR
              #123, the second of three in Stack Set "demo"), shows the
              environment variables the command receives, and runs the command
              with them.  --from and --to choose the change to simulate, as
              Status words in any case (Review, Draft, "CI running", "CI
              failed", Changes, Conflict, Approved, Merged, Closed); the default
              is Review to Approved.  --offline simulates the "cannot reach
              GitHub" notice and --list prints every GIT_SD_EVENT value. It
              needs no repository or GitHub access. The variables:
{{NOTIFY_VARIABLES}}

       Desktop notification examples (the command runs through sh -c and reads
       the variables from its environment; use git config --global for every
       repository):
{{NOTIFY_EXAMPLES}}

OPTIONS
       --help Print this manual and exit. The same as 'git sd help'. (git itself
              answers 'git sd --help' by looking for a man page, which git-sd does
              not install, so use 'git sd help', or run 'git-sd --help' directly.)

       -h     Print a short usage summary and exit.

       --agent
              Print a detailed, complete command reference intended for AI
              agents/scripts driving git-sd programmatically -- includes exact
              argument grammar and error conditions for every command in plain
              text, not just this manual's prose.

CONFIGURATION
       Set with git config <key> <value> for the current project, or git config
       --global <key> <value> for all projects.

       sd.status.legend
              [true|false] Always show the status legend in status.  Default:
              false.

       sd.status.columns
              [string] Comma-separated column list for status.  Default:
              "Icon,Status,Index,Stack Set,git-sd-id,Hash,Message,PR". An
              unknown column name is an error.

       sd.status.color
              [true|false] Color the Status word of status by state. Default:
              true. Only used when standard output is a terminal, and off when
              NO_COLOR is set or TERM is dumb.

       sd.watch.notify-command
              [string] Shell command run by watch for each change, with the set,
              the PR's place in it and the new state in GIT_SD_TITLE and
              GIT_SD_MESSAGE (and more in other GIT_SD_* variables; see notify-
              test for the list and for desktop notification examples). Default:
              none.

       sd.watch.min-interval
              [duration] Fastest watch poll: seconds, or a duration such as
              300ms. Default: 5s.

       sd.watch.max-interval
              [duration] Slowest watch poll while nothing changes. Default: 60s.

       sd.base-branch
              [string] Upstream branch to target, as "<remote>/<branch>"; remote
              must be a configured git remote. If unset: the "origin" remote's
              main branch if origin exists, else the sole remote's main branch
              if exactly one remote is configured; otherwise commands fail
              asking you to set this explicitly.  Useful for testing against a
              branch without main's approval guards.

EXIT STATUS
       0 on success, non-zero on any error.

SEE ALSO
       git-sd --agent for a complete machine-readable reference; git-sd -h for a
       short summary.
`
