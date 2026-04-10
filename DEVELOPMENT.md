# Stacked Diff CLI Tool (git-sd) - Development

## Integration Testing
- A comprehensive integration test suite must be developed alongside the tool.
- **Spec-First Testing:** Whenever a new feature is added to the specification, corresponding test cases MUST be added to the "Integration Test Scenarios" section below.
- **Sacrificial Repository:**
    - Integration tests run against a dedicated "sacrificial" repository.
    - **`GIT_SD_TEST_REPO` environment variable:** MUST point to a GitHub repository URL (e.g., `https://github.com/user/git-sd-test-repo`).
    - **Cloning Behavior:** The test suite will clone this repository into a temporary directory for each test run.
- **Safety Marker File:**
    - Tool MUST verify the presence of `.git-sd-test-marker` in the root of the sacrificial repository before performing test operations to prevent accidental data loss in other repositories.

## UX Mocks & Simulation
- **Simulation Script (`sd-sim`):** Maintained to validate the UX before and during implementation.
- **Spec-First Simulation:** Updated whenever a new feature or UX change is added to the specification.

## Integration Test Scenarios

1. **Environment Validation**
    - Verify tool fails gracefully if `gh` is missing or `gh auth status` fails.
    - Verify tool refuses to run tests if `GIT_SD_TEST_REPO` is unset or missing the marker file.

2. **Commit Identification (Trailers)**
    - `git sd status` adds `git-sd-id` trailers.
    - Amending/deleting commits is handled gracefully.

3. **Stack Set Assignment**
    - Assign/remove/reassign commits in various configurations.
    - Verify `status` correctly labels interleaved commits.

4. **Reordering & Feedback Handling (Move)**
    - Move to top-level or sub-index (`2.1`).
    - Verify atomic conflict handling.

5. **Syncing & PR Creation**
    - `git sd sync` creates a chain of PRs.
    - Verify isolated diffs and Stack table in descriptions.

6. **Updating & Force-Pushing**
    - Amend a middle commit and `sync`. Verify downstream force-pushes and comment preservation.

7. **Partial Merges & Sync**
    - Handle PRs merged on GitHub.
    - Verify subsequent PRs update their base to `main`.

8. **Merging with git-sd**
    - Verify `git sd merge` squashes sub-commits and uses a single temporary PR for the merge.

9. **Cleanup**
    - Verify `git sd cleanup` removes metadata and remote branches.
