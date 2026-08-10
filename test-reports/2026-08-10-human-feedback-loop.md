# Human Feedback Loop Test

## Objective

Verify that a `HUMAN_REVIEW_REQUIRED` task can accept free-form feedback, re-enter the Agent loop, and continue until a real validation gap is closed, instead of stopping at a single approve/reject decision.

## Scenario

Task `task-fa7dc7cb662574809374430e` initially ended with Reviewer `REQUEST_CHANGES` because the sandbox ran the repository default test command and did not execute the changed package:

> Required: Re-run validation with `go test ./internal/auth/`.

## Feedback Sent

Sent via `POST /human-reviews/{taskId}/feedback`:

> Re-run validation with `go test ./internal/auth/` and confirm the new regression tests pass before approving.

## Runtime Behavior

1. `ContinueTaskWithFeedback` persisted a `human_feedback` artifact and re-queued the task as `CREATED`.
2. The new run loaded the previous review, previous patch, and human feedback into Agent context.
3. `go test ./internal/auth/` was extracted from feedback and used as this run's sandbox `test_command_override`.
4. The TaskRouter again selected `security-audit`, so all agents continued under the same Skill.

## Result

- Final task status: `COMPLETED`
- Latest run: `run-a7212bd68ef72438f260f076`
- Test command actually executed: `go test ./internal/auth/`
- Test report: `ok github.com/qiangxue/go-rest-api/internal/auth 2.953s`
- Changed files: `internal/auth/api.go`, `internal/auth/api_test.go`
- Reviewer decision: `APPROVE_PROPOSAL`
- Reviewer explicitly confirmed: "The prior gap is resolved. The affected package's tests now execute and pass."

## Conclusion

Human review is now a multi-turn loop: approve to finish, reject to fail, or send free-form feedback to continue with the next Agent run. Feedback can override the sandbox test command, and the selected Skill remains active across turns.
