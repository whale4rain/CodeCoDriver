# CodeCoDriver Evaluation Token Bloat Incident Report

## 1. Summary

During the first standard evaluation runs, tasks were functionally passing, but token usage was far higher than expected. A single code task could consume more than 800k input tokens, and the detailed eval report initially showed zero token/cost data. The root causes were:

- LLM usage rows failed to persist because `llm_usages.step_id` referenced a `task_steps` row that had not been inserted yet.
- Patch and Reviewer agents serialized the full runtime context, including large memory records, and sent it to the model on every repair attempt.
- The context contained full `memory_candidates` and `memory` objects, whose `verification_evidence` fields stored large sandbox outputs and prior patches.
- Auto human feedback extracted incomplete `go test ...` commands from Reviewer output, causing repeated low-quality feedback turns.
- Evaluation batches counted `HUMAN_REVIEW_REQUIRED` as completed before auto resolution had finished.
- Worker concurrency was effectively low during recovery, leaving multiple `CREATED` tasks waiting longer than expected.

## 2. Timeline

1. Initial eval suite completed, but token usage was not measurable because `LLMUsage` rows were empty.
2. Detailed report was added, exposing that per-run tokens could be hundreds of thousands.
3. Debug logging showed a single Patch context JSON was about `411KB`.
4. Memory context accounted for most of the payload:
   - `memory_candidates` JSON: ~275KB
   - `memory` JSON: ~121KB
5. A same-task comparison showed:
   - Before optimization: ~865k tokens
   - After optimization: ~43k tokens
   - Reduction: ~95%
6. Final optimized evaluation suite: 10/10 passed.

## 3. Memory Module Design

### 3.1 Structured Memory

`MemoryEntry` stores structured execution knowledge:

- `title`, `summary`, `content`
- `symptom`, `root_cause`, `condition`
- `changed_files`, `symbols`
- `test_command`, `verification_evidence`
- `success_score`, `score`, `source`
- `metadata`, `links`
- `duplicate_of`, `conflict_group_id`

### 3.2 Retrieval Strategy

Memory retrieval runs before the Agent loop:

1. `SearchMemoryLimit(repoID, query, 10)` returns raw candidates.
2. `selectMemoryForContext` keeps a small selected set for Agent context.
3. Success/resolved/refined memories are prioritized.
4. Failure memories are included only when symptom/root cause is relevant.

### 3.3 Refinement Strategy

An asynchronous memory worker:

- Batches raw memories.
- Calls DeepSeek to refine summaries and fields.
- Deduplicates similar memories.
- Merges conflicting success/failure pairs into conditional `resolved_pattern`.
- Retries with bounded attempts and survives process restarts.

### 3.4 Problem in Memory Context

The raw retrieval result (`memory_candidates`) and selected memories both included full structured objects. `verification_evidence` could contain complete patch text and sandbox output. When Patch/Reviewer serialized the entire runtime context, these large memory objects became the dominant prompt payload.

## 4. Error Causes

### 4.1 LLM Usage Not Persisted

`llm_usages.step_id` had a foreign key to `task_steps`. The usage callback ran while the agent was executing, but the step row was only inserted after the agent returned. The insert failed silently because the runtime used `_ = s.store.AddLLMUsage(...)`.

Fix: migration `010_llm_tool_usage_fk.sql` drops the `step_id` foreign key on `llm_usages` and `tool_calls`.

### 4.2 Full Context Serialization

Patch and Reviewer used:

```go
contextJSON, _ := json.Marshal(r.Context)
prompt := "Prior agent context:\n" + contextJSON
```

This included:

- `memory_candidates`: raw retrieval candidates
- `memory`: selected memory entries
- `codebase.context_pack`: structured source snippets
- `codebase.context_pack_text`: rendered source text
- planner output, skill selection, attempt history

The same large JSON was repeated in every patch attempt and every review.

### 4.3 Prompt Duplication

Source code existed twice:

- `codebase.context_pack`
- `codebase.context_pack_text`

Memory existed twice:

- `memory_candidates`
- `memory`

`verification_evidence` inside both lists was serialized even though agents only needed a short memory guidance summary.

### 4.4 Auto Feedback Extraction

Reviewer output could contain:

```text
Required: go test ./cmd/server ./internal/healthcheck → ok). and confirm tests pass.
```

The first feedback parser captured trailing explanation text after the command, producing invalid instructions. This caused extra repair turns and unnecessary token consumption.

Fix: only tokens that look like command arguments (`./...`, `-flag`, paths with `/`) are kept after `go test`.

### 4.5 Batch Accounting

`refreshEvaluationBatch` treated `human_review_required` as a completed run. When auto resolution needed another run, the batch could be marked complete before the real result existed.

Fix: only `completed`, `failed`, and `cancelled` count toward batch completion.

### 4.6 External API Errors

DeepSeek returned `402 Insufficient Balance` during a clean rerun. These are infrastructure failures, not benchmark failures. The report now separates `external_errors` from benchmark failures and excludes them from pass-rate denominators.

## 5. Agent Loop Injection Points

The large context was injected into LLM calls at:

- Patch attempt 1: full context before code generation
- Patch attempt 2: full context plus repair feedback and attempt history
- Patch attempt 3: full context plus accumulated repair evidence
- Reviewer: full context plus patch proposal and test report
- Auto human feedback: a new run repeating all of the above

This is why usage grew multiplicatively, not linearly.

## 6. Fixes

### 6.1 Lean Context

`leanContextJSON` now:

- Removes `memory_candidates`
- Replaces `memory` with `memory_guidance`
- Removes `codebase.context_pack`
- Removes `codebase.context_pack_text`
- Keeps file lists, symbols, planner output, and skill selection

Source is appended separately once:

```text
RETRIEVED SOURCE:
===== FILE: pkg/pagination/pages.go =====
...
```

### 6.2 Prompt Budget

Future changes should add a prompt budget and emit context size metrics before every LLM call.

### 6.3 Evaluation Report

`GET /evaluations/report?batch_id=...` now returns:

- Per-run quality score and score breakdown
- Token and estimated cost per run
- Per-Agent calls, steps, prompt/completion tokens, cost
- Repair attempts, memory hits, artifact sizes
- Category aggregates
- External error classification

## 7. Results

### 7.1 Same Task Before/After

| Metric | Before | After |
|---|---:|---:|
| Total tokens | ~865k | ~43k |
| Patch calls | 3 | 3 |
| Reviewer calls | 1 | 1 |
| Planner calls | 3 | 3 |

### 7.2 Final Optimized Suite

| Metric | Value |
|---|---:|
| Pass rate after real-sandbox correction | 4/10 |
| Average quality after real-sandbox correction | 41.2 |
| Average tokens | 138,211 |
| Average cost (official cache-miss price) | ~$0.0221 / run |
| Average duration | ~915s / run |

### 7.3 Category Results

| Category | Pass | Avg Quality | Avg Tokens | Avg Cost |
|---|---:|---:|---:|---:|
| explanation | 1/1 | 73.4 | 5,798 | $0.0011 |
| security | 1/1 | 88.0 | 20,221 | $0.0031 |
| documentation | 0/1 | 0.0 | 126,607 | $0.0206 |
| refactor | 0/1 | 15.0 | 221,659 | $0.0338 |
| test | 2/6 | 39.2 | 167,972 | $0.0221 |

The quality score was revised after this report to make task quality the dominant signal. `completion` was reduced from `50` to `20`, deliverable quality was raised to `60`, and repair/token efficiency are now at most `5` points each and are only awarded to passed runs. The averages above are recalculated from the same run artifacts/token data with the current formula; no model calls were repeated.

The pass status was then corrected to require real sandbox evidence from the latest task run. The final 10-case batch changed from `10/10` to `4/10`; the 6 non-passing runs were auto-approved earlier despite `apply_failed` or `tests_failed` sandbox reports.

Per-case quality scores under the current formula after the real-sandbox correction:

| Case | Category | Quality |
|---|---|---:|
| health-response-contract | test | 15.0 |
| pagination-validation | test | 15.0 |
| pagination-edge-cases | test | 15.0 |
| health-endpoint-version | test | 87.9 |
| pagination-link-header | test | 15.0 |
| server-db-logging | test | 87.3 |
| explain-pagination-architecture | explanation | 73.4 |
| security-auth-input-validation | security | 88.0 |
| documentation-readme-overview | documentation | 0.0 |
| refactor-db-context-clarity | refactor | 15.0 |

### 7.4 Official DeepSeek Pricing

Pricing is based on the official DeepSeek API pricing page for `deepseek-v4-flash`:

| Item | CNY / 1M tokens | USD / 1M tokens |
|---|---:|---:|
| Input, cache miss | ¥1 | ~$0.14 |
| Input, cache hit | ¥0.02 | ~$0.0028 |
| Output | ¥2 | ~$0.28 |

The eval numbers above use the cache-miss input price, so they are an upper bound. With DeepSeek context caching, repeated prompts across repair/review turns should reduce the effective input cost further.

### 7.5 Final Suite Cost Summary

Source: `test-reports/eval-report-agent-2026-08-10-220102.json`

| Metric | Value |
|---|---:|
| Prompt tokens | 1,183,847 |
| Completion tokens | 198,272 |
| Total tokens | 1,382,119 |
| Estimated cost (cache miss) | ¥1.5804 / $0.2213 |
| Average cost per run | ¥0.1580 / $0.0221 |

Per-case estimated cost:

| Case | Category | USD |
|---|---|---:|
| health-response-contract | test | $0.0373 |
| pagination-validation | test | $0.0256 |
| pagination-edge-cases | test | $0.0611 |
| health-endpoint-version | test | $0.0027 |
| pagination-link-header | test | $0.0326 |
| server-db-logging | test | $0.0033 |
| explain-pagination-architecture | explanation | $0.0011 |
| security-auth-input-validation | security | $0.0031 |
| documentation-readme-overview | documentation | $0.0206 |
| refactor-db-context-clarity | refactor | $0.0338 |

The most expensive case is `pagination-edge-cases` at ~$0.0611, driven by a long repair loop. The cheapest deep code task is `server-db-logging` at ~$0.0033. This shows the current cost is dominated by patch/repair/review cycles rather than model prices.

## 8. Lessons

1. Do not serialize full runtime state into LLM prompts. Separate audit data from prompt data.
2. Keep memory evidence in storage, but only send summaries and targeted evidence to agents.
3. Instrument every LLM call with prompt size, token count, and cost.
4. Persist LLM usage independently of step insertion order.
5. Keep repair loops and auto-human feedback bounded and observable.
6. Distinguish infrastructure failures from benchmark failures.
7. Make batch completion depend only on true terminal statuses.

## 9. Related Files

- `internal/runtime/skill_prompts.go`
- `internal/runtime/agents.go`
- `internal/runtime/service.go`
- `internal/server/evaluation_report.go`
- `internal/store/migrations/010_llm_tool_usage_fk.sql`
- `scripts/run-eval-suite.ps1`
- `test-reports/eval-report-agent-2026-08-10-220102.json`
