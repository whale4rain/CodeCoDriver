# CodeCoDriver

CodeCoDriver is a repository-aware multi-agent engineering runtime. It indexes a local codebase, accepts an engineering task, plans the work, retrieves relevant code, proposes a patch, validates it in a sandbox, reviews the result, records an auditable trace, and reuses long-term memory across tasks.

[中文文档](README.zh-CN.md)

## Quick Start

Prerequisites: Go, Node.js/npm, Docker Desktop, and a `DEEPSEEK_API_KEY`.

1. Start PostgreSQL and Redis:

```powershell
docker compose up -d postgres redis
```

2. Start the Go API:

```powershell
$env:DEEPSEEK_API_KEY="your-api-key"
$env:DOUBAO_API_KEY="your-doubao-api-key"
$env:GOTELEMETRY="off"
go run ./cmd/api
```

Set `DOUBAO_API_KEY` to enable real semantic embeddings through Volcano Ark's
`doubao-embedding-text-240715` model. If it is not set, CodeCoDriver falls back to
the deterministic local embedding provider so local development still works.

Set `CODECODRIVER_REDIS_ADDR=localhost:6379` before starting the API to enable Redis leases and multi-worker coordination. Without this variable, the runtime falls back to the single-process in-memory queue.

3. Start the Dashboard:

```powershell
cd web
npm install
npm run dev
```

Open `http://127.0.0.1:5173`. The API listens on `http://localhost:8080`, and Vite proxies API requests to it.

4. Seed the demo repository and benchmark cases:

```powershell
./scripts/seed-demo.ps1
```

This registers the local `demo/go-rest-api` repository and creates benchmark cases so the Dashboard can be used immediately.

## Dashboard Pages

### Overview

The Overview page is the main entry point:

- Register a new repository by entering a repository name and a local filesystem path, then click `Register repo`.
- Create an engineering task by selecting a repository, entering a title and description, then clicking `Create task`.
- Review runtime metrics: active runs, completed tasks, human reviews, average runtime, completion rate, and failed tasks.
- Click a recent task to jump into its execution trace.

### Task Trace

The Task Trace page shows all tasks and a detailed audit trail for the selected task:

- Click any task in the left list to load its timeline.
- The timeline shows Planner, Codebase, Patch, Test, Reviewer, ToolCall, and LLM usage events.
- If a task is `HUMAN_REVIEW_REQUIRED`, enter an optional decision reason and click `Approve` or `Reject`.
- Approving marks the task completed; rejecting marks it failed.

### Memory

The Memory page inspects repository-scoped long-term memory:

- Enter the repository ID in the `Repository ID` field. The ID is printed by `seed-demo.ps1` and shown in the repository selector.
- Enter a query such as `retry timeout` or `pagination validation`.
- Click `Search memory` to see memory hits with kind, score, source, recall count, and creation time.

### Evaluation

The Evaluation page runs and compares benchmark cases:

- Select `Agent` or `Baseline` mode.
- Click `Run suite` to execute all registered benchmark cases as one batch.
- Review pass rate, total runs, benchmark cases, recent batches, metric history, agent-versus-baseline comparison, and individual run results.

## Typical Workflow

1. Start PostgreSQL, API, and the Dashboard.
2. Run `seed-demo.ps1` if you want a reproducible demo repository.
3. In Overview, register another repository or create a task for the demo repository.
4. Open Task Trace to inspect each Agent step and failure evidence.
5. If the runtime requests human review, approve or reject the task from the trace page.
6. Search Memory for related historical experience before starting a similar task.
7. Run an Evaluation suite to measure Agent performance against the benchmark.

## How It Works

- `Planner Agent` creates an execution plan and, on repair attempts, creates a focused repair plan.
- `Codebase Agent` retrieves relevant files and pairs source files with existing test files when the task asks for test coverage.
- `Patch Agent` generates a unified diff and receives explicit rules for current source state, new files, diff headers, and hunk context.
- `Sandbox` copies the repository to a temporary directory, normalizes and validates the diff, applies it, and runs tests without mutating the original workspace.
- `Reviewer Agent` checks correctness, regression risk, evidence, and test coverage before approving a proposal.
- Distributed workers acquire Redis leases for task IDs, renew them during execution, release them afterward, and use fencing tokens so stale workers cannot overwrite current task state.
- Long-term memory stores execution summaries, success patterns, and failure patterns with structured fields such as symptom, root cause, changed files, symbols, test command, verification evidence, and success score. Doubao embeddings are persisted in pgvector `halfvec(2560)` with an HNSW index, and recall combines semantic, keyword, freshness, and access-frequency signals. Mid-loop agent failures are also recorded so future tasks can avoid the same stage-level errors.
- `Tool Gateway` supports local tools, the Python document sidecar, and MCP JSON-RPC stdio servers.

The runtime uses DeepSeek's OpenAI-compatible API with the `deepseek-v4-flash` model.

## Configuration

Common environment variables:

| Variable | Purpose |
|---|---|
| `DEEPSEEK_API_KEY` | DeepSeek API key. |
| `DEEPSEEK_BASE_URL` | Override the DeepSeek API base URL. |
| `DEEPSEEK_TIMEOUT_SECONDS` | Override the model request timeout. |
| `DOUBAO_API_KEY` | Volcano Ark embedding API key. Alias: `CODECODRIVER_EMBEDDING_API_KEY`. |
| `CODECODRIVER_EMBEDDING_BASE_URL` | Override the embedding API base URL, default `https://ark.cn-beijing.volces.com/api/v3`. |
| `CODECODRIVER_EMBEDDING_MODEL` | Override the embedding model, default `doubao-embedding-text-240715`. |
| `CODECODRIVER_EMBEDDING_TIMEOUT_SECONDS` | Override the embedding request timeout, default `30`. |
| `DATABASE_URL` | Override the PostgreSQL connection string. |
| `CODECODRIVER_ADDR` | Override the API listen address. |
| `CODECODRIVER_WORKERS` | Local worker concurrency, default `1`. |
| `CODECODRIVER_REDIS_ADDR` | Redis address used for distributed task leases and fencing tokens. |
| `CODECODRIVER_RATE_LIMIT` | API requests per minute per client; `0` disables it. |
| `DEEPSEEK_INPUT_COST_PER_MILLION` | Enable estimated input cost tracking. |
| `DEEPSEEK_OUTPUT_COST_PER_MILLION` | Enable estimated output cost tracking. |

## API Surface

Core API routes:

- `GET /dashboard/overview`
- `GET /repositories`, `POST /repositories`, `POST /repositories/{id}/index`
- `GET /tasks`, `POST /tasks`, `GET /tasks/{id}/timeline`, `POST /tasks/{id}/cancel`
- `GET /memory/search?repository_id=...&query=...`
- `GET /evaluations`, `POST /evaluations/cases`, `PUT /evaluations/cases/{id}`
- `POST /evaluations/runs`, `POST /evaluations/suites`
- `GET /human-reviews`, `POST /human-reviews/{taskId}/approve`, `POST /human-reviews/{taskId}/reject`

## Documentation

- [Project design](docs/01-project-design.md)
- [Architecture](docs/02-architecture-design.md)
- [Data model](docs/03-data-model.md)
- [Implementation plan](docs/04-implementation-plan.md)
- [Runtime reliability](docs/05-runtime-reliability.md)
- [Demo runbook](docs/06-demo-runbook.md)
- [Resume summary](docs/07-resume-project-summary.md)

## Current Status

CodeCoDriver is a local engineering-runtime prototype. It supports real task execution, patch validation, long-term memory, distributed worker leases, Dashboard operation, and benchmark evaluation, but it is not yet a production multi-user product: there is no authentication, no container-level isolation, and benchmark results depend on model output quality.
