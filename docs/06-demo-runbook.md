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

The script shallow-clones and registers `qiangxue/go-rest-api` into `demo/go-rest-api`, then creates two benchmark cases. The repository is a small MIT-licensed Go REST API with layered packages, authentication, database access, and tests. The printed repository ID can be used in Memory Inspector.

`demo/sample-repo` remains available as a dependency-free smoke repository. `demo/ardan-service` is an optional larger Go service checkout for indexing stress tests and is ignored by the main repository.

## Demonstration Flow

1. Open Evaluation and click `Run suite` with `Agent` mode.
2. Open Task trace and inspect Planner, Codebase, Patch, Test, Reviewer, ToolCall, and LLM usage events.
3. Return to Evaluation and inspect batch progress and metric history.
4. Run the same suite in `Baseline` mode after recording baseline results through `POST /evaluations/runs`.
5. Open Memory Inspector and search the demo repository for `divide` or `subtract`.

## Useful Checks

```powershell
go run ./cmd/deepseek-smoke
go test ./...
go vet ./...
```

The original repository is never modified by generated patches. Sandbox validation runs against a temporary copy.
