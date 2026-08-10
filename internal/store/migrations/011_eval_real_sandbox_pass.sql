-- Backfill evaluation pass status from real stored artifacts.
-- Evidence is scoped to the latest task run, because an evaluation run can span
-- multiple task runs after human feedback. Earlier successful artifacts must not
-- make a later failed run pass.
WITH latest_task_run AS (
    SELECT DISTINCT ON (task_id) id, task_id
    FROM task_runs
    ORDER BY task_id, started_at DESC, id DESC
),
latest_test_report AS (
    SELECT DISTINCT ON (a.task_id)
           a.task_id,
           content::jsonb ->> 'applied' AS applied,
           content::jsonb ->> 'passed' AS passed
    FROM artifacts a
    JOIN latest_task_run ltr ON ltr.id = a.run_id
    WHERE a.type = 'test_report'
    ORDER BY a.task_id, a.created_at DESC, a.id DESC
)
UPDATE evaluation_runs er
SET passed = CASE
    WHEN EXISTS (SELECT 1 FROM latest_test_report ltr WHERE ltr.task_id = er.task_id)
    THEN COALESCE((
        SELECT LOWER(ltr.applied) IN ('true', '1') AND LOWER(ltr.passed) IN ('true', '1')
        FROM latest_test_report ltr
        WHERE ltr.task_id = er.task_id
    ), FALSE)
    WHEN EXISTS (
        SELECT 1
        FROM latest_task_run ltr
        JOIN artifacts a ON a.task_id = ltr.task_id AND a.run_id = ltr.id
        WHERE ltr.task_id = er.task_id AND a.type = 'explanation' AND length(a.content) > 0
    )
    THEN TRUE
    WHEN EXISTS (
        SELECT 1
        FROM latest_task_run ltr
        JOIN artifacts a ON a.task_id = ltr.task_id AND a.run_id = ltr.id
        WHERE ltr.task_id = er.task_id AND a.type = 'planner_skip'
    )
    THEN TRUE
    ELSE FALSE
END
WHERE er.status = 'completed';

-- Recompute batch counters after correcting runs.
UPDATE evaluation_batches b
SET passed = sub.passed,
    completed = sub.completed
FROM (
    SELECT er.batch_id,
           COUNT(*) FILTER (WHERE er.status IN ('completed', 'failed', 'cancelled')) AS completed,
           COUNT(*) FILTER (WHERE er.status IN ('completed', 'failed', 'cancelled') AND er.passed) AS passed
    FROM evaluation_runs er
    WHERE er.batch_id IS NOT NULL
    GROUP BY er.batch_id
) sub
WHERE b.id = sub.batch_id;

-- Recompute metric snapshots after correcting runs.
UPDATE evaluation_metric_snapshots snap
SET passed = sub.passed,
    total = sub.total,
    pass_rate = CASE WHEN sub.total > 0 THEN sub.passed::double precision / sub.total ELSE 0 END
FROM (
    SELECT b.id AS batch_id,
           b.passed,
           b.total AS total
    FROM evaluation_batches b
) sub
WHERE snap.batch_id = sub.batch_id;
