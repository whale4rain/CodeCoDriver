# Test Process

## Timeline

| Step | Command | Result |
|---|---|---|
| 1 | `go test ./... -json -count=1` | Exit `0` |
| 2 | `go test ./... -coverprofile=...` | Exit `0` |
| 3 | `go vet ./...` | Exit `0` |
| 4 | `npm run build` | Exit `0` |
| 5 | PostgreSQL integration tests | Exit `0` |
| 6 | Redis lease integration | Exit `0` |
| 7 | Memory unit tests | Exit `0` |
| 8 | Runtime async memory recovery | Exit `0` |

## Raw Outputs

- `raw/go-test-json.txt`
- `raw/go-test-cover.txt`
- `raw/coverage-func.txt`
- `raw/go-vet.txt`
- `raw/frontend-build.txt`
- `raw/postgres-integration.txt`
- `raw/redis-integration.txt`
- `raw/memory-tests.txt`
- `raw/runtime-memory-tests.txt`

## Key Observations

- The full Go suite completed without a failed test.
- PostgreSQL tests passed with the pgvector image and migration `006_memory_refinement.sql`.
- The real Doubao embedding test passed and used the 2560-dimension `doubao-embedding-text-240715` model.
- The Redis lease integration passed against the local Redis container.
- The async memory recovery test confirmed that a raw unrefined memory is picked up and refined after service startup.
