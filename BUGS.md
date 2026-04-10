# git-sd Bug Ledger (BUGS.md)

This file tracks implementation bugs and anomalies discovered during automated validation.

## Status Legend
- **OPEN:** Issue identified, awaiting fix.
- **FIXED:** Autonomous or manual fix applied and verified.
- **AMBIGUOUS:** Identified behavior requiring human confirmation to proceed.
- **DISMISSED:** Human confirmed the behavior is intended or out of scope.

## Bug Log

| ID | Severity | Status | Category | Description | Fix/Commit |
|:---|:---|:---|:---|:---|:---|
| B-001 | High | FIXED | OBVIOUS | `git interpret-trailers` --message flag error | a1b2c3 |
| B-002 | Med | FIXED | OBVIOUS | `git-sd status` duplicates trailers | d4e5f6 |
| B-003 | High | FIXED | OBVIOUS | Nil map panic in `runStatus` when no trailers exist | Fixed in `internal/git/git.go` |
| B-004 | High | FIXED | OBVIOUS | `assign` fails if `status` hasn't been run (missing IDs) | Added `ensureIDs` helper |

## Unconfirmed / Ambiguous Behavior
*Add new items here that require human confirmation before action is taken.*
