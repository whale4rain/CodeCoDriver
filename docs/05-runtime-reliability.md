# Runtime Reliability

## Scope

This stage implements single-process execution reliability before introducing Redis-based distributed leases.

- Configurable local worker concurrency through CODECODRIVER_WORKERS
- Per-task cancellation contexts
- Cancellation API for queued and running tasks
- Startup recovery for unfinished PostgreSQL tasks
- Queue deduplication inside one API process
- Explicit terminal CANCELLED state

## Recovery Semantics

Execution is currently at-least-once. On startup, CREATED tasks are queued directly. Tasks left in an active state are treated as interrupted: unfinished runs are marked FAILED, the task returns to CREATED, and a fresh run starts from the beginning.

The runtime deliberately does not resume from an arbitrary Agent step yet. Agent context is partly stored in JSONB steps and artifacts, but reconstructing an exact in-memory context requires a versioned checkpoint schema. Restarting from the beginning is safer and auditable until checkpoints exist.

## Cancellation Semantics

Queued cancellation marks the task CANCELLED; a worker that later receives the stale queue item exits before creating a run. Running cancellation invokes the task-specific context cancel function, propagating to DeepSeek HTTP requests and sandbox commands. Error handling checks persisted task state so context cancellation cannot overwrite CANCELLED with FAILED.

## Important Challenges

1. Cancellation and failure race: the persisted terminal state is the source of truth, not the order in which goroutines return.
2. Crash recovery leaves open runs: they must be closed before a replacement run starts, otherwise trace data implies concurrent execution.
3. Queue deduplication is process-local: multi-instance deployment still requires a distributed lease and idempotency key.
4. Step-level resume is unsafe without checkpoints: replaying from the beginning is intentionally preferred over rebuilding partial Agent context heuristically.

## Next Reliability Increment

Redis should provide durable queue delivery and a lease keyed by task ID. PostgreSQL remains the source of truth for task state. Workers must renew leases, use fencing tokens, and reject stale writers before CodeCoDriver can safely run multiple API or worker instances.
