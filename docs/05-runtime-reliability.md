# Runtime Reliability

## Scope

This stage implements single-process reliability and Redis-backed distributed execution leases.

- Configurable local worker concurrency through CODECODRIVER_WORKERS
- Redis task leases with renewal and release
- Per-task fencing tokens that reject stale database writes
- Per-task cancellation contexts
- Cancellation API for queued and running tasks
- Startup recovery for unfinished PostgreSQL tasks
- Queue deduplication inside one API process
- Explicit terminal CANCELLED state

## Recovery Semantics

Execution is currently at-least-once. On startup, CREATED tasks are picked up by workers. Tasks left in an active state are treated as interrupted: unfinished runs are marked FAILED, the task returns to CREATED, and a fresh run starts from the beginning.

## Distributed Execution

When `CODECODRIVER_REDIS_ADDR` is set, workers do not rely on an in-process queue. They poll PostgreSQL for claimable tasks, acquire a Redis lease with `SET NX PX`, renew it during execution, and release it when the task finishes.

- Lease keys are `codecodriver:task:{taskID}:lease`.
- Fencing keys are `codecodriver:task:{taskID}:fencing`.
- Each successful claim increments the fencing key and stores the token in `task_runs.fencing_token`.
- Task status updates and run completion are guarded by `UpdateTaskForRun` and `FinishRunWithToken`, so a worker whose lease expired cannot overwrite a newer run.
- If the lease expires, another worker can claim the same task and start a replacement run.

The runtime deliberately does not resume from an arbitrary Agent step yet. Agent context is partly stored in JSONB steps and artifacts, but reconstructing an exact in-memory context requires a versioned checkpoint schema. Restarting from the beginning is safer and auditable until checkpoints exist.

## Cancellation Semantics

Queued cancellation marks the task CANCELLED; a worker that later receives the stale queue item exits before creating a run. Running cancellation invokes the task-specific context cancel function, propagating to DeepSeek HTTP requests and sandbox commands. Error handling checks persisted task state so context cancellation cannot overwrite CANCELLED with FAILED.

## Important Challenges

1. Cancellation and failure race: the persisted terminal state is the source of truth, not the order in which goroutines return.
2. Crash recovery leaves open runs: they must be closed before a replacement run starts, otherwise trace data implies concurrent execution.
3. Redis leases prevent duplicate execution, but tasks are still retried from the beginning after crash recovery; checkpoint-based resume remains future work.
4. Step-level resume is unsafe without checkpoints: replaying from the beginning is intentionally preferred over rebuilding partial Agent context heuristically.

## Next Reliability Increment

The API now has a process-local per-client sliding-window rate limiter and configurable HTTP read-header, write, and idle timeouts. These protections apply before route handlers and are intentionally independent of task state.

DeepSeek calls also emit usage records linked to the current execution step. Token counts and latency are always captured; cost estimates are opt-in through pricing environment variables so changing provider pricing does not require a code change.

The current implementation uses PostgreSQL as the durable queue source and Redis for coordination. Future work can add a Redis-backed delivery queue, checkpoint-based step resume, and worker heartbeats exposed through the Dashboard.
