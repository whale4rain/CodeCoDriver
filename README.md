# CodeCoDriver

CodeCoDriver is a repository-aware multi-agent engineering backend. It indexes a local codebase, plans an engineering task, retrieves relevant context, proposes a patch, validates it, reviews the result, and records an auditable trace plus reusable memory.

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

Run a single API connectivity check with:

```powershell
go run ./cmd/deepseek-smoke
```

The API listens on `http://localhost:8080`. Set `CODECODRIVER_ADDR` or `DEEPSEEK_BASE_URL` to override the defaults. See [docs/README.md](docs/README.md) for the full design.
