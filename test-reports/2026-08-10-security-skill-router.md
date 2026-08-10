# Security Audit Skill Auto-Routing Test

## Objective

Verify that TaskRouter autonomously selects a Skill from the `skills/` folder when a task matches its keywords, without manually setting `skill_name`.

## Setup

- Added `skills/security-audit.json` with `security-audit` keywords, path patterns, and Planner/Patch/Reviewer prompt templates.
- Restarted PostgreSQL, Redis, Go API, and Vite.
- Verified `GET /skills` and the Vite proxy both returned: `documentation, go-testing, general, security-audit`.

## Flow

1. Created a task with no `skill_name`:
   - Title: `Security audit of demo Go REST API`
   - Description mentions security vulnerabilities, injection, unsafe input handling, auth checks, and CVE risks.
2. Waited for the Agent loop to reach a terminal state.
3. Inspected `/tasks/{id}/trace` for `skill_selection` and per-step `selected_skill`.

## Result

- Task: `task-fa7dc7cb662574809374430e`
- Status: `HUMAN_REVIEW_REQUIRED`
- `skill_selection.primary_skill`: `security-audit`
- Routing score: `security-audit=10.0`, `go-testing=2.5`, `documentation=2.0`, `general=0`
- Every step input recorded `selected_skill=security-audit`: planner, codebase, patch, test, reviewer, and repair planner.
- Planner output used the Security Audit prompt direction: auth/authorization, input validation, injection, secrets, dependency CVE risks, evidence-first scope.
- Reviewer output used the Security Audit prompt direction: concrete evidence, minimal secure fix, regression test coverage, secure defaults.

## Conclusion

The folder-based SkillRegistry and TaskRouter are working end-to-end. The model did not receive an explicit `skill_name`; the runtime selected `security-audit` automatically and the actual Agent prompts/plans/reviews reflected the selected Skill.
