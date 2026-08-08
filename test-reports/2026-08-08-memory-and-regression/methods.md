# Test Methods

## Objective

Verify the current CodeCoDriver implementation after the async memory refinement worker, batch refinement, similarity deduplication, and conflict resolution changes.

## Test Scope

1. Full Go package test suite with `go test ./...`.
2. Static analysis with `go vet ./...`.
3. Frontend production build with `npm run build`.
4. PostgreSQL persistence, pgvector, structured memory, and memory links.
5. Real Doubao embedding + pgvector halfvec integration.
6. Redis lease and fencing-token integration.
7. Memory refinement, deduplication, conflict resolution, and async recovery tests.

## Environment

| Component | Version |
|---|---|
| Go | 1.24.1 windows/amd64 |
| Node.js | 22.14.0 |
| npm | 11.6.2 |
| PostgreSQL | pgvector/pgvector:pg14 |
| Redis | redis:7-alpine |
| Python | 3.8.4 with matplotlib 3.8.4 |

## Pass Criteria

- `go test ./...` exits `0`.
- `go vet ./...` exits `0`.
- `npm run build` exits `0`.
- PostgreSQL integration tests pass.
- Real Doubao embedding test returns a 2560-dimension vector and passes vector search.
- Redis integration test passes.
- Memory tests pass, including async startup recovery.

## Commands

```powershell
go test ./... -json -count=1
go test ./... -coverprofile="<absolute-path>/coverage.out" -count=1
go vet ./...
npm run build
go test ./internal/store -run 'TestPostgres' -v -count=1
go test ./internal/lease -run 'TestRedisLeaserIntegration' -v -count=1
go test ./internal/memory -v -count=1
go test ./internal/runtime -run 'TestAsyncMemoryRefinementRecoversOnStart' -v -count=1
```

## Not Executed

- Real Agent benchmark using DeepSeek was not executed because `DEEPSEEK_API_KEY` was not set.
- `go test -race` could not run reliably on this Windows environment; the Go runtime exited with `0xc0000139` instead of running the race tests.
