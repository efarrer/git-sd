# Tasks

## Implemented

## Pending

### 1. Project Infrastructure
- [ ] **Project Setup:** Create a `.gitignore` file to exclude Go binaries and common build artifacts.
- [ ] **Package Structure:** Initialize Go modules and create the directory structure defined in `DESIGN.md` (`internal/git`, `internal/github`, `internal/metadata`, `internal/ui`).
- [ ] **Integration Test Framework:**
    - [ ] Implement test runner in `cmd/git-sd-test`.
    - [ ] Add safety check for `.git-sd-test-marker`.
    - [ ] Implement logic to clone/reset the sacrificial repository using `GIT_SD_TEST_REPO`.
- [ ] **Simulation Script:** Update `sd-sim.sh` to match the latest `SPEC.md` requirements.

### 2. Core CLI & Environment
- [ ] **Startup & Environment Validation:**
    - [ ] Check for `gh` installation.
    - [ ] Verify `gh auth status`.
- [ ] **Base Command (`git sd`):** Implement usage/help output.
- [ ] **Configuration Support:** Implement `git config` reading for `sd.*` keys.

### 3. Commit & Metadata Management
- [ ] **Metadata Management:** Implement loading/saving of `.git/sd/metadata.json` with hierarchical support (`index`, `sub_index`).
- [ ] **Commit Identification (Trailers):**
    - [ ] Implement 5-character alphanumeric ID generation.
    - [ ] Implement `AddTrailer` using `git interpret-trailers` and `git commit-tree` as defined in `DESIGN.md`.
    - [ ] Implement `ensureIDs` helper to idempotenly tag commits with `git-sd-id`.

### 4. `git sd status`
- [ ] **Basic Table:** Implement dynamic column width calculation and basic commit listing.
- [ ] **Base Branch Detection:** Implement logic for `sd.base-branch` and tracked upstream detection.
- [ ] **PR State Integration:**
    - [ ] Implement `internal/github` wrappers for `gh api`.
    - [ ] Query GitHub for PR states and map to specified emojis (🔄, 💥, ❌, etc.).
- [ ] **Formatting & UX:**
    - [ ] Implement `--legend` / `--no-legend` support.
    - [ ] Implement hierarchical indexing (e.g., `1.1`) display.

### 5. `git sd assign` & `move`
- [ ] **`git sd assign`:** Implement assignment string parsing (`set:1,3-5`) and metadata updates for top-level and sub-commits.
- [ ] **`git sd move`:**
    - [ ] Implement rebase/cherry-pick logic for reordering.
    - [ ] Implement atomic conflict handling.

### 6. `git sd sync`
- [ ] **Two-Pass Sync Logic:**
    - [ ] **Pass 1:** Branch management, force-pushing, and PR creation/update to get URLs.
    - [ ] **Pass 2:** Update metadata and generate "Stack" table in PR descriptions.
- [ ] **PR Linking:** Implement "Stack" table generation with "Previous"/"Next" links and HTML markers.
- [ ] **Partial Merge Handling:** Update PR base to `main` if previous PRs in the stack are merged.

### 7. `git sd merge` & `cleanup`
- [ ] **`git sd merge`:**
    - [ ] Implement "top-mergeable" strategy with top-most PR reuse.
    - [ ] Implement sub-commit and lower-level top-level commit consolidation.
    - [ ] Implement PR closing with redirection comments and links.
- [ ] **`git sd cleanup`:** Implement metadata and remote branch removal.

### 8. Validation
- [ ] **Full Integration Suite:** Implement all scenarios defined in `DEVELOPMENT.md`.
- [ ] **Autonomous Validation:** Perform validation loop using `skill-validation`.
