# CodeCoDriver Demo Runbook

## Prerequisites

- Docker Desktop is running.
- PostgreSQL is available through `docker compose up -d postgres`.
- `DEEPSEEK_API_KEY` is set when model-backed execution is required.
- Go, Python, Node.js, and npm are installed.

## Start Services

From the repository root:

```powershell
docker compose up -d postgres
go run ./cmd/api
```

In a second terminal:

```powershell
cd web
npm install
npm run dev
```

Open `http://127.0.0.1:5173`.

## Seed Demo Data

With the API running:

```powershell
./scripts/seed-demo.ps1
```

The script shallow-clones and registers `qiangxue/go-rest-api` into `demo/go-rest-api`, then creates two benchmark cases. The repository is a small MIT-licensed Go REST API with layered packages, authentication, database access, and tests. It is registered with the focused command `go test ./cmd/server ./internal/healthcheck ./pkg/pagination`, avoiding the upstream integration tests that require a separate PostgreSQL password. The printed repository ID can be used in Memory Inspector.

`demo/sample-repo` remains available as a dependency-free smoke repository. `demo/ardan-service` is an optional larger Go service checkout for indexing stress tests and is ignored by the main repository.

## Demonstration Flow

1. Open Evaluation and click `Run suite` with `Agent` mode.
2. Open Task trace and inspect Planner, Codebase, Patch, Test, Reviewer, ToolCall, and LLM usage events.
3. Return to Evaluation and inspect batch progress and metric history.
4. Run the same suite in `Baseline` mode after recording baseline results through `POST /evaluations/runs`.
5. Open Memory Inspector and search the demo repository for `divide` or `subtract`.

## Observed Benchmark Behavior

The first live suite exposed upstream database-test configuration as an environment dependency. After registering the focused test command, both cases reached Sandbox patch application without database failures. The observed run then correctly entered `HUMAN_REVIEW_REQUIRED` because the model produced one patch for an already-existing test file and one patch with stale hunk context. These are useful demonstration signals: the runtime preserves evidence and refuses to claim success when a model diff cannot be applied.

## BugFix Validation

On the `bugFix` branch, patch reliability was improved with test-aware context selection, diff normalization, structured apply feedback, and repair-state prompts. A live rerun produced one real success and one evidence-preserving human review:

- `pagination-validation`: `COMPLETED`, `passed=true`. The patch applied on the first attempt and all focused tests passed.
- `health-timeout`: all three attempts now progress through patch application, with attempt 3 applying cleanly and `go test ./cmd/server ./internal/healthcheck ./pkg/pagination` passing. Reviewer still requested changes because the generated timeout logic was a synchronous no-op rather than a real timeout path. This is a task-semantics gap, not a diff-application failure.

## Useful Checks

```powershell
go run ./cmd/deepseek-smoke
go test ./...
go vet ./...
```

The original repository is never modified by generated patches. Sandbox validation runs against a temporary copy.
