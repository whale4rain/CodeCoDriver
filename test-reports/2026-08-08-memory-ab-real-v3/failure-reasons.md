# V3 A/B Failure Reasons

This file records why each non-passing case failed in the V3 memory A/B run.

| Case | with_memory | without_memory | Root failure reason |
|---|---|---|---|
| health-response-contract | Passed | Passed | - |
| pagination-validation | Failed | Failed | Patch failed to apply repeatedly at `pkg/pagination/pages_test.go:95`; proposed validation helper was dead code, so tests never ran successfully. |
| pagination-edge-cases | Passed | Failed | Patch failed to apply at `pkg/pagination/pages_test.go:95`; proposed tests were not applied and some required coverage was already present but not extended. |
| health-endpoint-version | Failed | Failed | with_memory used nonexistent `test.DoRequest` and missing `strings` import; without_memory passed tests but Reviewer required explicit HEAD body/version/header verification that the patch did not cover. |
| pagination-link-header | Failed | Failed | Patch hunks were stale at `pkg/pagination/pages_test.go:67`; after one attempt applied, expected link strings were incorrect (missing leading slash). |
| server-db-logging | Failed | Failed | Generated invalid/unified diffs after compile errors (unterminated string literal, wrong `logDBQuery`/`logDBExec` signatures); final proposals were not valid patches. |

## Conclusion

Most failures were caused by Patch Agent diff generation, hunk context, or test implementation quality, not by memory content. This makes it difficult to attribute pass-rate differences to memory in this round.
