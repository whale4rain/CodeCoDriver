# CodeCoDriver

CodeCoDriver is a repository-aware multi-agent engineering backend. It indexes a local codebase, plans an engineering task, retrieves relevant context, proposes a patch, validates it, reviews the result, and records an auditable trace plus reusable memory.

Start PostgreSQL before the API:

```powershell
docker compose up -d postgres
```

```powershell
$env:DEEPSEEK_API_KEY="your-api-key"
$env:GOTELEMETRY="off"
go run ./cmd/api
```

The runtime uses DeepSeek's OpenAI-compatible API with the `deepseek-v4-flash` model. Planner, Patch, and Reviewer are model-backed; repository retrieval and tests run locally in Go.

Repository context is assembled from indexed source files with line numbers. Retrieval is restricted to the repository root, rejects escaping symlinks and traversal, filters sensitive filenames, and applies a 5-file / 32 KiB context budget before sending content to the model.

DeepSeek requests use a 180-second timeout, an 8192-token output limit, and disabled thinking mode so the output budget is available for plans and diffs. The DEEPSEEK_TIMEOUT_SECONDS environment variable can override the timeout.

Patch proposals are validated and applied only in a temporary repository copy. The sandbox checks paths and patch limits, uses git apply recounting to tolerate model-generated hunk count errors, executes repository tests with a timeout, and sends the resulting evidence to Reviewer without mutating the original workspace.

Failed sandbox attempts enter a bounded repair loop. CodeCoDriver feeds compact validation evidence back to Planner and Patch, records every attempt in the trace, and stops after three patch attempts before final review.

Reviewer participates in the same loop: a passing sandbox result is not sufficient by itself. REQUEST_CHANGES feeds review findings back into replanning, APPROVE_PROPOSAL completes the task, and exhausted or ambiguous reviews end in HUMAN_REVIEW_REQUIRED.

Memory entries store a deterministic 32-dimensional text embedding in PostgreSQL JSONB. Memory recall combines keyword matching with cosine similarity, then reranks by freshness and access frequency. Each recall updates access metadata, so frequently useful and recently created execution patterns remain more prominent without requiring the current PostgreSQL image to include pgvector.

The default database is available on localhost:55432. Set DATABASE_URL to override it; schema migrations run automatically at API startup. Use docker compose down to stop it without deleting the named volume.

Run a single API connectivity check with:

```powershell
go run ./cmd/deepseek-smoke
```

The API listens on `http://localhost:8080`. Set `CODECODRIVER_ADDR` or `DEEPSEEK_BASE_URL` to override the defaults. See [docs/README.md](docs/README.md) for the full design.

The tool layer is available in `internal/tools`: it routes local tools, calls the optional Python document sidecar at `/parse`, and supports newline-delimited JSON-RPC MCP stdio servers. Start the dependency-free document service with `python python/document_service.py` when document parsing is needed.

Agents receive the configured Tool Gateway through their runtime request. Tool calls are policy-checked, capped at 30 seconds by default, and persisted in the task trace with request, response, status, and latency.

Set `CODECODRIVER_WORKERS` to control local worker concurrency (default `1`). Cancel a queued or running task with `POST /tasks/{id}/cancel`. Unfinished tasks are recovered with a fresh run when the API restarts.
