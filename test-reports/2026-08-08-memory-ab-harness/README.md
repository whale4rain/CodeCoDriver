# Memory A/B Harness Report

Date: 2026-08-08

## Method

The harness runs the same deterministic Agent loop twice:

- `with_memory`: the task starts with historical memory injected into Planner context.
- `without_memory`: the same task runs with an empty memory context and no memory persistence.

Both tasks use the same repository, benchmark title, file index, patch flow, sandbox result, and reviewer decision.

## Verified Results

| Mode | Memory hits | Memory injected | Task completion |
|---|---|---|---|
| `with_memory` | 1 | 1 entry | COMPLETED |
| `without_memory` | 0 | 0 entries | COMPLETED |

`TestFinalizeEvaluationRecordsMemoryMetrics` also verifies that completed evaluation runs persist `memory_hits` and `repair_attempts`.

## Commands

```powershell
go test ./internal/runtime -run 'TestMemoryModeABHarness|TestMemoryModeForEvaluation|TestFinalizeEvaluationRecordsMemoryMetrics' -v -count=1
```

## Real Benchmark Limitation

`DEEPSEEK_API_KEY` is not set in this environment, so a real DeepSeek A/B comparison cannot run yet. The API now supports evaluation modes `with_memory` and `without_memory`, and `scripts/memory-ab-test.ps1` is ready to run the real suite once the key is available.
