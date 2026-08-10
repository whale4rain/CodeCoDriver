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

## Runbook

```powershell
./scripts/seed-demo.ps1
./scripts/run-eval-suite.ps1 -Mode agent
./scripts/run-eval-suite.ps1 -Mode baseline
```

Each suite writes a JSON report to `test-reports/`.
