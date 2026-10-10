# Autonomous Validation Runbook

This document defines a portable, agent-neutral process for validating the `git-sd` implementation. Any agent acting on this repository MUST follow this loop when instructed to "Validate Implementation."

## 1. Target Environment & Prerequisites
- **Sacrificial Repo:** `git@github.com:efarrer/testing-stacked-diffs.git`
- **Verification:**
    - `git` and `gh` are installed and functional.
    - `GIT_SD_TEST_REPO` environment variable is set to the sacrificial repo path.
    - The file `.git-sd-test-marker` MUST exist in the root of the sacrificial repo.

## 2. The Validation Loop (Background Iteration)

The agent should iterate autonomously through these steps:

### Phase A: Build & Test
1.  **Rebuild:** Execute `make clean && make build`.
2.  **Test Execution:** Run `make test` (equivalently `go test -v -count=1 ./integration/`). The suite builds the `git-sd` binary itself in `TestMain` and validates both low-level `git` operations and high-level `gh` PR management. It is gated on `GIT_SD_TEST_REPO`: if that variable is unset, the tests skip cleanly (exit 0) instead of failing, so it MUST be set to the sacrificial repo for validation.
3.  **Data Capture:** Log all stdout/stderr from the test run.

### Phase B: Analysis & Categorization
For every failure or anomaly, update `BUGS.md` and categorize the item:

- **OBVIOUS:** Syntax errors, build failures, or regressions where the fix is unambiguous (e.g., a missing trailer or a clear logic error).
- **AMBIGUOUS:** Behavior that is technically functional but may deviate from intended UX, or areas where `SPEC.md` is underspecified.

### Phase C: Autonomous Fixing (The Loop)
1.  **Fix Obvious Bugs:** For every item categorized as **OBVIOUS**, the agent MUST attempt a fix based on `SPEC.md` and `DESIGN.md`.
2.  **Re-Validate:** Immediately re-run Phase A to verify the fix.
3.  **Update Ledger:** Mark the bug as `FIXED` in `BUGS.md` once verified.
4.  **Repeat:** Continue until no **OBVIOUS** bugs remain.

### Phase D: Human Intervention
- If only **AMBIGUOUS** items remain, or if the agent is stuck on a persistent OBVIOUS bug after 3 failed fix attempts:
    - Stop the autonomous loop.
    - Present the current `BUGS.md` status to the user.
    - **Wait for confirmation** before attempting to fix AMBIGUOUS items.

## 3. Security & Safety
- NEVER run tests without verifying the `.git-sd-test-marker`.
- NEVER commit or push to non-sacrificial repositories during validation.
