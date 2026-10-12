# Stacked Diff CLI Tool (git-sd) - Development

## Git Version Compatibility
- `assign`'s Stack-Set-syncability check (`internal/git.SimulateChainBuild`) needs to simulate cherry-picking a chain of commits without touching the working tree or index. On **git >= 2.38** this uses `git merge-tree --write-tree --merge-base=` (the modern `merge-ort`-backed plumbing, with real content-based rename detection). On older git, it falls back to `git read-tree -m --aggressive` into a scratch index plus `git write-tree` — functionally equivalent, but its rename detection is weaker than `merge-ort`'s, so a path renamed on one side and edited on the other can occasionally report a conflict (or resolve differently) where the modern path would apply cleanly. The installed git version is probed once per process (`internal/git.gitVersion`, memoized) and compared against 2.38.0 (`internal/git.atLeast`) to pick a strategy; everything else in git-sd only uses long-stable git commands with no version floor.

## Breaking Metadata Schema Change (sub-commits)

`CommitMetadata`'s `SubIndex int` field was replaced by `ParentID string` + `SubRank string` (see DESIGN.md's "Metadata Schema" for the field-level model, and SPEC.md's "Hierarchical Indexing" and `git sd move`/`fixup` sections for the user-facing behavior — sub-commits are now explicitly parent-referenced and rank-ordered rather than inferred from physical adjacency). This is **not** backward-compatible with an existing `.git/sd/metadata.json`: any `sub_index` values in an old file are silently ignored (not migrated), which will make a repo with pre-existing sub-commits show them as plain top-level commits after upgrading. No migration code was written — this is pre-release tooling (only a sacrificial test repo found in use), so the fix is to clear/re-track `.git/sd/metadata.json` after upgrading rather than carry migration logic.

## Integration Testing
- A comprehensive integration test suite must be developed alongside the tool.
- **Spec-First Testing:** Whenever a new feature is added to the specification, corresponding test cases MUST be added to the "Integration Test Scenarios" section below.
- **Sacrificial Repository:**
    - Integration tests run against a dedicated "sacrificial" repository.
    - **`GIT_SD_TIMING=1`:** every command prints, to stderr when it finishes, where its time went: the phases `status` marks (base branch, commits, metadata, PR lookups, patch-ids, rows), how many `gh` processes it started, how many `git` processes of each kind (counted by git's own trace2 log, so every call is covered, including ones made by `gh` itself), each with its summed time (concurrent ones overlap, so the sum can exceed the wall time), and the total. A permanent diagnostic, not a debugging leftover: unset, nothing is measured or printed. Needs git 2.22 or later for the `git` counts (older git just omits them). Hooks live in `internal/timing`. The opt-in scenario `TestSDStatusTiming` (run with `GIT_SD_TEST_TIMING=1`, against the real test repo) builds a realistic stack and prints these reports for `status` and `sync`.
    - **`GIT_SD_TEST_REPO` environment variable:** MUST point to a GitHub repository URL (e.g., `https://github.com/user/git-sd-test-repo`).
    - **`.env` convenience:** `GIT_SD_TEST_REPO`/`GIT_SD_TEST_AUTHOR_TOKEN`/`GIT_SD_TEST_REVIEWER_TOKEN` can be set in a `.env` file at the repo root instead of exported in the shell — copy `.env.example` to `.env` and fill in real values. `.env` is gitignored; a real environment variable always takes precedence over it (see `dotEnvValues`/`getTestEnv` in `integration/helpers_test.go`).
    - **Cloning Behavior:** The test suite clones the repo into a local `--mirror` once at startup (`TestMain`), then clones a fresh, isolated working copy into a temporary directory for each test run via `git clone --reference <mirror>` — this borrows objects from the mirror instead of re-fetching them from GitHub for every parallel test, while each test's own `.git` config/remotes/refs stay fully independent.
    - **Merge latency:** stack merges go through GitHub's (public preview) async Stacked Pull Requests API, which can genuinely take on the order of minutes per merge server-side — this is a GitHub-side latency/reliability characteristic of the preview feature itself (see e.g. `github/gh-stack` discussions #212 and #404), not something client-side retry/backoff tuning or spreading load across repos can fix. `-parallel` can be raised freely without triggering additional per-merge slowdown from concurrency (tested up to `-parallel 4`), so it remains the main lever for total suite wall-clock time.
    - **Stacked Pull Requests access required:** the repo must have GitHub's (public preview) Stacked Pull Requests feature enabled — `sync`/`merge` now register and merge through the Stacks REST API, not `gh pr create`/`edit`/`merge` directly. Without it, `internal/github.CreateStack`/`MergeStack` fail with `ErrAsyncMergeUnavailable`.
    - **Branch protection for safeguard tests:** approval/CI enforcement is delegated entirely to GitHub branch protection (git-sd no longer checks review/CI status itself). `testing-stacked-diffs` (the sacrificial repo in active use) is private, and GitHub requires a paid plan to configure branch protection on a private repo — so no integration scenario there can actually exercise a real rejection. `internal/github/github_test.go`'s `TestMergeStackFailedSurfacesBranchProtectionMessage` (and neighbors) cover `MergeStack`'s own parsing/error-surfacing logic instead, via a fake `gh` returning canned async-merge responses — no network, no plan requirement. Still untested: a genuinely *rejected* merge under real branch protection — that needs a public sacrificial repo with protection configured, or a paid plan, neither of which is in place today.
    - **Second reviewer account (optional):** `GIT_SD_TEST_AUTHOR_TOKEN` / `GIT_SD_TEST_REVIEWER_TOKEN` let the suite drive two distinct GitHub identities — needed because GitHub forbids self-approval, so no single-account scenario can ever exercise "a PR gets approved by someone else, and merge actually completes" (`TestSDMergeSucceedsWithSecondReviewerApproval`). Both are optional and additive: unset, the suite behaves exactly as if neither existed (every author-side call falls back to the ambient `gh auth login` session, and the two-account scenario skips with a clear message). The token is passed only via the specific `exec.Cmd.Env` of each call that should act as that identity (see `ghEnv` in `helpers_test.go`) — never written into the test binary's own process environment, so it stays safe alongside the suite's `t.Parallel()` usage.
        - **Setup:** add the reviewer account as a collaborator on the private sacrificial repo (repo Settings → Collaborators → Add people; the invite must be accepted before the account has any access) and generate a token for it — either a fine-grained PAT (`github.com/settings/tokens?type=beta`) scoped to just that repo with "Pull requests: Read and write," or a classic PAT (`github.com/settings/tokens`) with the `repo` scope. For the author token, if that account already has an active `gh auth login` session on the test machine, `gh auth token` prints its current token directly — no need to generate a separate PAT.
        - **`gh auth setup-git` prerequisite:** `gh` CLI calls (`gh pr ...`, `gh api ...`) read `GH_TOKEN` natively, but raw `git push`/`git fetch`/`git clone` don't — they need `gh auth setup-git` run once on the machine, which wires git's credential helper (`credential.https://github.com.helper`) to `!gh auth git-credential`. That helper itself reads `GH_TOKEN` from its invocation's inherited environment, so it correctly picks up whichever token a given `git` subprocess was launched with. Check with `git config --get credential.https://github.com.helper`; run `gh auth setup-git` if it's unset.
- **Safety Marker File:**
    - Tool MUST verify the presence of `.git-sd-test-marker` in the root of the sacrificial repository before performing test operations to prevent accidental data loss in other repositories.
- **Running the tests:**
    - The integration suite is a standard `go test` package (`./integration/`). Run it with `make test` or directly `go test -v -count=1 ./integration/`.
    - `TestMain` builds the `git-sd` binary itself, so a separate `make build` is not required to run the tests.
    - The suite is gated on `GIT_SD_TEST_REPO`: when it is unset the tests skip cleanly (exit 0) instead of failing, so it must be set to the sacrificial repo URL to actually exercise anything.

## UX Mocks & Simulation
- **Simulation Script (`sd-sim`):** Maintained to validate the UX before and during implementation.
- **Spec-First Simulation:** Updated whenever a new feature or UX change is added to the specification.

## Integration Test Scenarios

1. **Environment Validation**
    - Verify a command that needs `gh` reports the right message when `gh` is missing from PATH (`'gh' CLI not found`) and when `gh` is unauthenticated (exit code 4 / `HTTP 401` -> `not authenticated`): `sync` fails before pushing anything, and `status` still prints its table plus one warning and exits 0. Verify local-only commands (`track`, `assign`, `move`, `fixup`, `help`) work without `gh`.
    - Verify tool refuses to run tests if `GIT_SD_TEST_REPO` is unset or missing the marker file.

2. **Commit Identification (Trailers)**
    - `git sd track` adds `git-sd-id` trailers.
    - Amending/deleting commits is handled gracefully.

3. **Stack Set Assignment**
    - Assign/remove/reassign top-level commits in various configurations.
    - Verify `status` correctly labels interleaved commits.
    - Verify the PR markers on real PRs: a commit with no PR shows Assigned (📦); a freshly synced, unreviewed PR shows Review Required (👀); once approved it shows ✅; converting it to a draft shows 🚧 even though it is approved (skipped where the repo's plan does not support drafts).
    - Verify a failed PR lookup shows `❓` for that PR only (other rows keep their real state) and `status` ends with one warning naming the PR(s) and the first error, exit 0 (fake `gh` exiting 1 with a non-authentication error).
    - Verify `assign` rejects a new top-level commit whose content depends on a different Stack Set's change (B-017), and that assigning several mutually-dependent commits together to the *same* Stack Set still succeeds.
    - Verify `assign <set>:<N>.<R>` (a real Stack Set name) is rejected unconditionally, directing to `move` — even when `<set>` already matches the target's parent's Stack Set (no no-op case; it never changes state). Verify `assign :<N>.<R>` (unassign) still succeeds and fully detaches the commit in one step.
    - Verify assigning/unassigning a top-level commit cascades its Stack Set to every existing sub-commit.

4. **Reordering & Sub-commits (Move / Fixup)**
    - Move a top-level commit to a new position; verify a promoted sub-commit keeps its Stack Set.
    - Attach a commit as a sub-commit via `move <src>:<dest>.<rank>`; verify it inherits `<dest>`'s Stack Set (including the dormant, unassigned case), and that `<dest>=0` works (regression for the original off-by-one bug in the pre-fix targeting scheme, which searched backward from an insertion slot instead of naming the target directly).
    - Reorder two existing sub-commits relative to each other via a new rank.
    - Verify `move` rejects attaching under a commit that's itself a sub-commit (no 3-level nesting).
    - Verify conflict handling: after a conflicting `move`/`fixup`, `git rebase --abort` restores the original order and metadata, and `git rebase --continue` yields the new order with the attachment recorded (pending-move reconcile); mutating commands refuse while the rebase is in progress.
    - Verify moving an untracked commit auto-tracks it (assigns a `git-sd-id`) instead of silently dropping the attachment.
    - `fixup <target>`: verify it attaches HEAD with an auto-computed rank, rejects an unassigned target, rejects when HEAD already has its own sub-commits, and progresses correctly across a rank-tier boundary (9 existing sub-commits -> 10th gets `"91"`).
    - Verify `fixup` self-tracks an untracked HEAD commit (no prior `track`/`assign`/`status` call), the same way `move` does — a fresh fix commit should attach in one step, never requiring a separate tracking step first.

5. **Syncing & PR Creation**
    - `git sd sync` creates a chain of PRs and registers/extends the chain's GitHub Stack.
    - Verify isolated diffs, no `git-sd-id` trailer in the pushed commits, and branch names following the subject-slug scheme.

6. **Updating & Force-Pushing**
    - Amend a middle commit and `sync`. Verify downstream force-pushes and comment preservation.
    - Verify re-syncing unchanged PRs leaves their remote branch SHAs untouched, even after every local commit was rewritten with only a new committer date; changing only the top PR re-pushes only that PR. Verify the sync summary: the first sync of a new set prints one `Created PR #<n>` line per PR with the same URL `status` shows; an immediate second sync prints only `<n> PR(s) unchanged.` and does not edit any PR on GitHub (its updated-at timestamp is unchanged); amending a commit message and syncing prints `Updated PR #<n>`.

7. **Partial Merges & Sync**
    - Handle PRs merged on GitHub.
    - Verify subsequent PRs update their base to `main`.

8. **Merging with git-sd**
    - Verify `git sd merge` submits the top-most PR to GitHub's async stack-merge API and every PR in the range lands on the base branch as its own commit (not a squash). Verify it is GitHub-only: the local branch and working tree are untouched (it works with uncommitted changes), and a following `git sd rebase` drops the merged commits and retires their metadata.
    - With `GIT_SD_TEST_REVIEWER_TOKEN` set: verify a PR genuinely approved by a second GitHub account merges successfully (skipped otherwise).
    - With `GIT_SD_TEST_REVIEWER_TOKEN` set: adding a new, unrelated Stack Set and running a bare `git sd sync` (no set name — which reprocesses every set, including ones with nothing to do) must not disturb an already-approved existing set's PR identity, base, or approval.
    - With `GIT_SD_TEST_REVIEWER_TOKEN` set: `move`-reordering commits within an approved Stack Set changes the affected PR's base (a real, expected mutation) but must preserve its identity (same PR, not recreated) and its approval.

9. **Rebase (Upstream Reconciliation)**
    - For each merge shape GitHub supports — squash, merge commit, rebase-merge (via `gh pr merge --squash|--merge|--rebase`) — verify `git sd rebase` detects the landed group by content, drops it from the local branch, rebuilds the rest onto the updated base, and retires its metadata.
    - Verify a merged group classified by net content: amended locally after its last sync, then its PR merged -> `rebase` keeps one follow-up commit (same Stack Set, new id, no PR) and `sync` then opens a PR for it; a group whose local and upstream edits touch the same lines -> a git rebase conflict on the follow-up commit (`--continue`, `--skip`, `--abort`); a group where upstream has more than local -> dropped. Covered by `cmd/git-sd/landed_test.go` and an integration scenario.
    - Verify a conflict during the rebuild leaves a standard `git rebase` in progress, and that afterwards `git rebase --abort` restores `HEAD` and `metadata.json` exactly (and removes the backup), `git rebase --continue` retires the landed and closed PRs and keeps the backup for `rebase --rollback`, and `git rebase --skip` treats the skipped commit as landed. Other state-changing commands refuse while the rebase is in progress. (Covered by `cmd/git-sd/rebase_test.go` local-repo unit tests plus an integration scenario.)
    - Verify `rebase --rollback` after a successful rebase restores both the branch and the retired metadata, and that `--list` shows the backups.
    - Verify `rebase` retires a `CLOSED` PR's metadata but keeps its commits, and never retires a still-open or unsynced commit, even when other commits in the same Stack Set have finished (B-018).
    - Verify `rebase` never touches the remote (the finished PR's remote branch still exists afterward).

10. **Sync Remote Teardown & Living Stack Sets**
    - Verify `sync` deletes the queued remote branches of retired commits, and only after retargeting the surviving PRs.
    - Verify `sync` keeps (and keeps queued) a retired branch that an open PR — here one created outside git-sd — still uses as its base, and deletes it on a later sync once that PR is gone.
    - Verify `sync` dissolves a Stack Set whose commits have all been retired (its entry is removed and no Stack is re-registered).
    - Verify that after merging PRs off the front of a Stack Set and running `rebase`, a new commit can still be `assign`ed to the same Stack Set and `sync`ed onto the back, chaining behind the surviving PR (B-018).
