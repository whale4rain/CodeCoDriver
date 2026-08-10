# CodeCoDriver Eval Quality Re-analysis

## 1. Conclusion

The previous eval pass rate `10/10` and the quality-first score recalculation were both still too optimistic. After reading the actual run traces, the pass result does **not** mean the patch applied and tests passed.

Real outcome of the final 10-case suite:

| Outcome | Count |
|---|---:|
| Sandbox truly passed | 3 |
| Read-only explanation passed | 1 |
| Patch/test failed | 6 |

The previous report counted the 6 non-passing runs as `completed && passed`, so the pass rate should be `4/10`, not `10/10`.

## 2. Root Cause

The agent loop only runs the Reviewer after a sandbox `passed` report. If the patch keeps failing to apply or tests keep failing, the Reviewer can still be invoked on the final attempt and returns `REQUEST_CHANGES`. That sends the task to `human_review_required`.

`maybeAutoHandleEvaluationHumanReview` then treats eval human review as something to automatically resolve:

1. It sends up to two auto-feedback turns, usually `Re-run validation ... and confirm tests pass before approving.`
2. After the feedback budget is used, it calls `ResolveHumanReview(taskID, true, "eval auto-approve after human review")` unconditionally.
3. `finalizeEvaluation` sets `run.Passed = status == TaskCompleted`, so an auto-approved run becomes `completed && passed=true`.

The result is a false positive: `auto_approved` is recorded, but the sandbox artifact still says `status: apply_failed` or `tests_failed`.

## 3. Per-Case Analysis

### 3.1 True Passes

| Case | Evidence |
|---|---|
| `health-endpoint-version` | `test_report status=passed, applied=true, passed=true`; `17,428` tokens; highest-quality code case |
| `server-db-logging` | `test_report status=passed, applied=true, passed=true`; `21,464` tokens |
| `security-auth-input-validation` | `test_report status=passed, applied=true, passed=true`; `20,221` tokens |
| `explain-pagination-architecture` | read-only explanation; artifact covers `pkg/pagination` but misses expected `internal/album` |

### 3.2 Auto-Approved Failures

| Case | Real Sandbox Result | Auto Decision |
|---|---|---|
| `health-response-contract` | `tests_failed`, patch applied | auto-approve after human review |
| `pagination-validation` | `apply_failed`, patch never applied | auto-approve after human review |
| `pagination-edge-cases` | `apply_failed`; malformed diff with duplicated hunk headers | auto-approve after human review |
| `pagination-link-header` | `apply_failed`; malformed diff and missing `strings` import | auto-approve after human review |
| `refactor-db-context-clarity` | `apply_failed`; exported API rename not validated | auto-approve after human review |

### 3.3 Documentation Case

`documentation-readme-overview` ended with `apply_failed` and `REQUEST_CHANGES`, then was recorded as `auto_approved_skip` (`eval auto-approve duplicate skip`). It did not actually produce an applied README change in the final run.

## 3.4 Corrected Score View

After applying the real-sandbox correction, the final 10-case scores are:

| Case | Passed | Quality | Main Loss |
|---|---:|---:|---|
| `health-response-contract` | false | 15.0 | tests failed, reviewer REQUEST_CHANGES |
| `pagination-validation` | false | 15.0 | patch never applied |
| `pagination-edge-cases` | false | 15.0 | malformed patch, no tests |
| `health-endpoint-version` | true | 87.9 | real pass |
| `pagination-link-header` | false | 15.0 | malformed patch, no tests |
| `server-db-logging` | true | 87.3 | real pass |
| `explain-pagination-architecture` | true | 73.4 | expected path partially covered |
| `security-auth-input-validation` | true | 88.0 | real pass |
| `documentation-readme-overview` | false | 0.0 | no applied README change |
| `refactor-db-context-clarity` | false | 15.0 | wrong expected path and patch apply failure |

This state is persisted through migration `011_eval_real_sandbox_pass.sql` and is now returned by the live evaluation report.

## 4. Why Tokens Are High

Failed runs still spend a full repair budget:

- `pagination-edge-cases`: `361,338` tokens, `9` patch calls, `9` planner calls, `3` reviewer calls, mostly regenerating malformed diffs.
- `pagination-link-header`: `207,386` tokens, `10` patch calls, `9` planner calls.
- `refactor-db-context-clarity`: `221,659` tokens, `9` patch calls, `9` planner calls, `5` reviewer calls.
- A broken validation command (`go test ./cmd/server ./internal/healthcheck ./pkg/pagination all`) caused full standard-library tests to run and time out, adding a large test-report payload.

## 5. Required Fix Direction

Eval auto-approval must not be unconditional:

1. Only auto-approve when there is real sandbox evidence: `applied=true && passed=true` for code tasks.
2. If the Reviewer asks for changes, auto-feedback may run, but the terminal auto decision must not mark a `apply_failed/tests_failed` task as passed.
3. Mark such runs as failed or `human_review_required` in eval reports and exclude them from the pass rate.
4. Add a `sandbox_passed` field to `EvaluationRun` or derive it from the latest `test_report` artifact when scoring.
5. Keep the quality-first score formula, but score auto-approved failed runs as if `passed=false`.

This issue is separate from the score-weight inflation fixed in `internal/server/evaluation_report.go`; it is a correctness bug in eval auto-human handling.

## 6. Related Files

- `internal/runtime/service.go` (`maybeAutoHandleEvaluationHumanReview`, `finalizeEvaluation`)
- `internal/server/evaluation_report.go`
- `test-reports/eval-report-agent-2026-08-10-220102.json`
- `docs/08-evaluation-design.md`
- `docs/09-eval-token-bloat-incident.md`

## 7. Applied Fix

The runtime now derives `EvaluationRun.Passed` from real stored evidence instead of the final task status:

- The latest task run's latest `test_report` must have `applied=true && passed=true`.
- Read-only explanation runs pass only when a non-empty `explanation` artifact exists.
- Legitimate `planner_skip` runs pass only when no test evidence exists in the latest run.
- Eval auto-approval no longer runs when this evidence is missing; it auto-rejects the run so it is counted as failed.

Migration `011_eval_real_sandbox_pass.sql` backfills existing records using the same latest-run/latest-report scope. The final 10-case batch changed from `10/10` to `4/10` passed, and corrected quality scores now range from `0` for failed docs to `73.4-88.0` for genuinely successful runs.
