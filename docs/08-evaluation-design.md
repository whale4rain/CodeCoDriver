# CodeCoDriver Evaluation Design

## Goal

Measure the end-to-end Agent runtime with a diverse, reproducible benchmark suite, including automatic coverage of human-review cases so the suite can run unattended.

## Task Diversity

Benchmark cases cover these categories:

- `test`: focused Go test coverage and behavior hardening.
- `documentation`: README or markdown changes.
- `security`: security audit and concrete input-validation fixes.
- `explanation`: read-only code/architecture explanation.
- `refactor`: behavior-preserving code clarity refactors.

## Metrics

Core metrics per suite:

- Completion: terminal runs / total runs.
- Pass rate: completed and passed / completed + failed.
- Human review auto actions: auto feedback, auto-approve, auto-approve skip.
- Duration: total and average run duration.
- Repair effort: average repair attempts per run.
- Memory impact: average memory hits, success/failure/refined hits.
- Category and mode breakdown: agent vs baseline, explanation/security/test/docs/refactor.

## Automatic Human Policy

For evaluation runs only:

1. Planner skip suggestion -> auto-approve the skip.
2. Reviewer requires a concrete rerun or `go test ...` command -> send one or two automatic feedback turns.
3. Otherwise -> auto-approve after the human-review checkpoint and record `auto_approved` in run notes.

Human review for interactive user tasks is unchanged.

## Detailed Report

`GET /evaluations/report` returns a detailed report with:

- Per-run quality score (`0-90`).
- Score breakdown: completion, deliverable, repair efficiency, token efficiency.
- Per-task token usage and estimated cost.
- Per-Agent calls, steps, prompt/completion tokens, and cost.
- Patch size, explanation length, expected-path hits, changed files, repair attempts, memory hits.
- Category aggregates for test, documentation, security, explanation, and refactor.

The scoring rubric is category-specific: code/test/security/refactor reward patch existence, passing validation, expected-path hits, low repair effort, and token efficiency; explanation and documentation reward artifact quality, expected-path evidence, and useful output length.

## Quality Score

The quality score is quality-first rather than completion-first. The score range is `0-90`; the `10` points that used to reward efficiency have been folded into a smaller secondary dimension so a cheap run can never outscore a correct one:

- Completion (`20`): only `completed && passed` gets `20`; a completed-but-failed run gets `0`.
- Deliverable quality (`60`): measures whether the agent produced a real result in the expected location.
  - Code/test/security/refactor: expected-path hit (`30`) is the dominant signal, followed by patch content and validation pass (`15` each).
  - Explanation: artifact existence and expected-path coverage.
  - Documentation: expected-path hit, patch content, patch size, and validation pass.
- Repair efficiency (`5`): only awarded for passed runs; one repair is fine, each extra repair costs one point.
- Token efficiency (`5`): only awarded for passed runs and is capped at the category ideal-token target.

Failed runs therefore cannot receive completion, expected-path, or efficiency points. They can only reach at most the patch-content share of deliverable quality, and documentation tasks that were not applied receive no deliverable credit. `passed` means real sandbox evidence from the latest task run (`applied=true && passed=true`), not merely an auto-approved human-review status. Passing quality signals such as tests, expected paths, and artifact coverage dominate the score, matching the SWE-bench-style principle that correctness/result quality is the primary signal and efficiency is a secondary dimension.

## Cost Model

The default cost estimator follows the official `deepseek-v4-flash` pricing:

| Item | CNY / 1M tokens | USD / 1M tokens |
|---|---:|---:|
| Input, cache miss | ¥1 | ~$0.14 |
| Input, cache hit | ¥0.02 | ~$0.0028 |
| Output | ¥2 | ~$0.28 |

`estimateCost` is applied to every LLM usage record. The environment variables `DEEPSEEK_INPUT_COST_PER_MILLION` and `DEEPSEEK_OUTPUT_COST_PER_MILLION` can override the defaults when provider pricing changes. Eval reports currently use the cache-miss input price, so they represent an upper bound; DeepSeek context caching should reduce effective input cost across repair/review turns.

## Runbook

```powershell
./scripts/seed-demo.ps1
./scripts/run-eval-suite.ps1 -Mode agent
./scripts/run-eval-suite.ps1 -Mode baseline
```

Each suite writes a JSON report to `test-reports/`.
