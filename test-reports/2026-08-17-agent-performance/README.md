# CodeCoDriver Agent Performance Run

## 1. Purpose

This run measures the current agent loop against a small, category-balanced
benchmark suite. The goal is to record result usability, planning quality,
efficiency, tool behavior, and failure modes after the patch editing and
context compaction changes.

## 2. Method

The run used the registered `qiangxue/go-rest-api` demo repository. Six cases
were selected so each major workload is represented:

| Case | Category | Reason |
|---|---|---|
| `health-endpoint-version` | test | Focused API test generation |
| `pagination-edge-cases` | test | Higher-difficulty pagination behavior |
| `explain-pagination-architecture` | explanation | Read-only architecture reasoning |
| `security-auth-input-validation` | security | Security review and regression tests |
| `documentation-readme-overview` | documentation | Repository documentation delivery |
| `refactor-db-context-clarity` | refactor | Behavior-preserving code clarity |

The suite was started through `POST /evaluations/suites` in `agent` mode with
`memory_mode=with_memory`. The run used the default compact configuration.

Raw artifacts:

- `evaluation-report.json`
- `evaluation-state.json`

## 3. Results

Batch summary:

| Metric | Value |
|---|---:|
| Cases | 6 |
| Passed | 3 |
| Failed | 3 |
| Pass rate | 50% |
| Average quality | 43.8 |
| Total tokens | 2,727,388 |
| Prompt tokens | 2,643,914 |
| Completion tokens | 83,474 |
| Total estimated cost | ~$0.394 |
| Average duration | 648.2s |

Per-case results:

| Case | Category | Result | Quality | Duration | Tokens | Cost |
|---|---:|---:|---:|---:|---:|---:|
| `health-endpoint-version` | test | PASS | 85.4 | 498.6s | 116,057 | $0.02 |
| `pagination-edge-cases` | test | FAIL | 15.0 | 1,466.3s | 1,466,253 | $0.21 |
| `explain-pagination-architecture` | explanation | PASS | 72.2 | 518.0s | 9,314 | $0.00 |
| `security-auth-input-validation` | security | FAIL | 15.0 | 743.1s | 659,154 | $0.09 |
| `documentation-readme-overview` | documentation | PASS | 75.2 | 822.8s | 188,998 | $0.03 |
| `refactor-db-context-clarity` | refactor | FAIL | 0.0 | 886.7s | 287,612 | $0.04 |

## 4. Failure Analysis

The three failures share a clear cause.

`pagination-edge-cases` and `refactor-db-context-clarity` both failed with
`patch edit loop exceeded 16 calls`. The model repeatedly emitted tool calls
without converging on an editable patch, consuming most of the batch tokens and
wall-clock time.

`security-auth-input-validation` failed with
`tool run_test is not allowed in patch edit loop`. The patch agent attempted to
use a tool outside the patch workflow allowlist. This is a routing or prompt
constraint issue rather than a sandbox validation issue.

The successful cases are the low-risk and read-only cases:

- `health-endpoint-version` produced a real passing patch.
- `explain-pagination-architecture` produced a useful explanation without patch.
- `documentation-readme-overview` applied a documentation change.

## 5. Performance Characterization

The current agent is reliable on simple, narrowly scoped tasks and read-only
workflows. It is not yet reliable on higher-difficulty test generation,
refactoring, or tasks that require the patch agent to iterate over many tool
results.

The efficiency profile is dominated by patch repair loops. The two runaway
patch cases account for about 79% of total token consumption. This means
improving patch-loop convergence and tool selection is the highest-leverage
optimization target.

Planning quality is acceptable for the passing cases, but result usability
collapses when patch generation diverges. Safety remained clean: no sensitive
files were changed and no sandbox tool errors were recorded.

## 6. Recommended Next Step

The highest-value next improvement is not a new page or memory feature. It is
making the patch edit loop terminate earlier when the model stops making
progress, for example by detecting repeated identical tool calls, disallowed
tools, or long stretches without an actual `edit_file` or `write_file`.

## 7. Patch Loop Fix Verification

The patch edit loop was changed to recover from a disallowed tool request
instead of terminating the whole task, and to stop early when no file edit is
made, repeated tool calls are detected, or `generate_patch` repeatedly returns
an empty diff.

| Case | Pre-fix | Post-fix |
|---|---:|---:|
| `security-auth-input-validation` | FAIL, 743.1s, 659,154 tokens | PASS, 50.9s, 60,523 tokens |
| `pagination-edge-cases` | FAIL, 1,466.3s, 1,466,253 tokens | FAIL early, 27.9s, 62,786 tokens |

The security case now recovers from the disallowed `run_test` call and reaches
`APPROVE_PROPOSAL`. The pagination case still fails because the model emits
final answers without editing the workspace, but the runtime now stops after
three such answers instead of burning the full tool budget.

After separating direct-diff prompts from the edit-workspace workflow and
aligning skill tool declarations with the actual runtime tools, the pagination
case passed in a real run:

| Case | Result | Duration | Tokens | Cost | Quality |
|---|---:|---:|---:|---:|---:|
| `pagination-edge-cases` | PASS | 108.5s | 258,684 | $0.038 | 85.2 |
