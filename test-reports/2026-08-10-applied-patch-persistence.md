# Applied Patch Persistence Fix

## Symptom

After clicking `Apply to repo`, returning to the task still showed `Apply to repo` instead of the persisted apply result.

## Root Cause

`ApplyTaskPatch` wrote the `applied_patch` artifact with an empty `run_id`. PostgreSQL declares `artifacts.run_id` as `NOT NULL REFERENCES task_runs(id)`, so the insert failed silently (`_ = s.store.AddArtifact(...)`) and no apply state was persisted.

## Fix

`ApplyTaskPatch` now resolves the latest task run ID before writing the artifact and reuses it for the apply success memory.

## Verification

- Task `task-22ee222cc7575af4ed86b10d` apply response: `already_applied`.
- `GET /tasks/{id}/trace` now contains:
  - type: `applied_patch`
  - run_id: `run-94c958ae5cb7917ce2612d25`
- `GET /tasks/{id}/timeline` now returns an `applied` object with `status`, `applied`, `passed`, `output`, and `warnings`.
- Dashboard reopens the task using this persisted object and shows `Apply success` plus `Apply again if wrong`.
