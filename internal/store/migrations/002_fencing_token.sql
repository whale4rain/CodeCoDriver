ALTER TABLE task_runs ADD COLUMN IF NOT EXISTS fencing_token BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS task_runs_task_fencing_idx ON task_runs(task_id, fencing_token);
