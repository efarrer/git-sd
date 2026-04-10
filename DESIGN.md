# Stacked Diff CLI Tool (git-sd) - Design

## Architecture & Design
- **Command Name:** `git-sd`.
- **Git Integration:** Subcommand `git sd`.
- **Metadata:** Stored in `.git/sd/metadata.json` to prevent cross-checkout conflicts.
- **Integration Test Tool:** Separate tool that calls `git-sd`.

## Implementation Details

### Go Implementation
- **Project Structure:**
    - `cmd/git-sd`: Entry point for the CLI tool.
    - `cmd/git-sd-test`: Entry point for the integration test suite.
    - `internal/git`: Low-level git command wrappers.
    - `internal/github`: GitHub CLI (`gh`) wrappers and API interactions.
    - `internal/metadata`: Logic for reading and writing `.git/sd/metadata.json`.
    - `internal/ui`: Terminal UI and formatting logic (emojis, tables).
- **Trailer Management:**
    - `AddTrailer` uses `git interpret-trailers --where end --if-exists replace --trailer git-sd-id=<id>` to append or update the `git-sd-id`.
    - **ID Generation:** `git-sd-id` is a 5-character alphanumeric hash (e.g., `s3f2a`). It is generated when a commit is first identified as "Ready" (lacking an ID) by `git sd status` or `git sd assign`.
    - The existing commit message is passed via **stdin**.
    - The resulting message is used with `git commit-tree` to create the new commit.

### Metadata Schema (`metadata.json`)
The metadata file tracks the relationship between `git-sd-id`s and their assigned Stack Sets.
```json
{
  "commits": {
    "s3f2a": {
      "stack_set": "feat-a",
      "index": 1,
      "sub_index": 0,
      "pr_url": "https://github.com/...",
      "pr_number": 123
    }
  },
  "stack_sets": {
    "feat-a": {
      "base_branch": "main",
      "target_branch": "main",
      "status": "active"
    }
  }
}
```
- **Hierarchical Tracking:** `index` represents the top-level PR position; `sub_index` (if > 0) indicates a sub-commit addressable as `index.sub_index`.

### Sync Strategy (Two-Pass)
- `git sd sync` ensures all PR metadata is current before building the "Stack" table:
    1. **Pass 1: Push & PR Creation/Update:**
        - Creates temporary branches and cherry-picks commits.
        - Pushes to remote.
        - Creates or updates PRs to obtain/confirm `pr_url` and `pr_number`.
    2. **Pass 2: Metadata & Description Update:**
        - Generates the "Stack" table with "Previous" and "Next" links using the URLs gathered in Pass 1.
        - Updates PR descriptions using `gh pr edit --body`.

### Merge Strategy (Top-Most Reuse)
- `git sd merge`:
    1. Identifies the highest index PR that is approved and ready.
    2. Squashes all commits from index 1 up to the target index.
    3. Updates the PR for the target index with the consolidated content.
    4. Merges that top-most PR.
    5. Closes all PRs with lower indices.
    6. Posts a redirection comment on the closed PRs: *"This PR's contents have been consolidated into [PR #XX](link) for final merge."*

## Prototype Phase (using `gh`)
- The initial prototype leverages the GitHub CLI (`gh`) for interaction with GitHub.
- This approach simplifies the initial build and allows for rapid iteration on features before potentially moving to direct API calls.
