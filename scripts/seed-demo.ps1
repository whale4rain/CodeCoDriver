param(
  [string]$ApiUrl = "http://127.0.0.1:8080"
)

$repoPathValue = Join-Path $PSScriptRoot "..\demo\go-rest-api"
if (-not (Test-Path (Join-Path $repoPathValue "go.mod"))) {
  git clone --depth 1 https://github.com/qiangxue/go-rest-api.git $repoPathValue
}
$repoPath = (Resolve-Path $repoPathValue).Path
$repository = Invoke-RestMethod -Uri "$ApiUrl/repositories"
$repository = $repository | Where-Object { $_.path -eq $repoPath } | Select-Object -First 1
if (-not $repository) {
  $repository = Invoke-RestMethod -Method Post -Uri "$ApiUrl/repositories" -ContentType "application/json" -Body (@{ name = "CodeCoDriver Demo"; path = $repoPath; test_command = "go test ./cmd/server ./internal/healthcheck ./pkg/pagination" } | ConvertTo-Json)
}

$cases = @(
  @{ name = "health-response-contract"; title = "Add focused health endpoint tests"; description = "Review the health endpoint and add focused tests for GET and HEAD status code and body behavior. Do not modify internal/healthcheck/api.go or the existing response format."; expected = @("internal/healthcheck", "cmd/server") },
  @{ name = "pagination-validation"; title = "Improve pagination input validation"; description = "Review pagination request validation and add a focused test for invalid page parameters without changing the public API."; expected = @("pkg/pagination", "internal") },
  @{ name = "pagination-edge-cases"; title = "Cover pagination unknown total and invalid page size"; description = "Add focused tests in pkg/pagination for New with total -1, per_page over max, page beyond last, NewFromRequest with invalid per_page, and Offset/Limit behavior. Do not change production behavior."; expected = @("pkg/pagination") },
  @{ name = "health-endpoint-version"; title = "Cover health endpoint HEAD and version response"; description = "Add focused tests in internal/healthcheck that verify HEAD and GET return the same status and the body contains the registered version. Do not change the production response format."; expected = @("internal/healthcheck") },
  @{ name = "pagination-link-header"; title = "Cover pagination link header edge cases"; description = "Add focused tests in pkg/pagination for BuildLinkHeader when defaultPerPage differs from PerPage, baseURL already contains a query, and total is unknown. Do not change production behavior."; expected = @("pkg/pagination") },
  @{ name = "server-db-logging"; title = "Cover DB logging error and success paths"; description = "Add focused tests in cmd/server for logDBQuery and logDBExec success and error paths, including non-nil sql.Rows and sql.Result behavior. Keep tests self-contained and do not require a database."; expected = @("cmd/server") },
  @{ name = "explain-pagination-architecture"; title = "Explain pagination architecture"; description = "Explain the implementation path for pagination, including relevant functions, types, callers, and architectural boundaries. Do not change any code."; expected = @("pkg/pagination", "internal/album") },
  @{ name = "security-auth-input-validation"; title = "Security audit auth input validation"; description = "Review internal/auth login input handling for empty credentials, unsafe validation, and missing regression tests. Only change code when there is concrete evidence."; expected = @("internal/auth") },
  @{ name = "documentation-readme-overview"; title = "Document repository overview"; description = "Update README.md with an accurate repository overview, usage, and architecture summary. Do not invent endpoints or commands."; expected = @("README.md") },
  @{ name = "refactor-db-context-clarity"; title = "Improve db context code clarity"; description = "Refactor pkg/dbcontext for clearer naming or responsibility separation without changing public behavior. Add focused tests if behavior changes."; expected = @("pkg/dbcontext") }
)

${existingCases} = (Invoke-RestMethod -Uri "$ApiUrl/evaluations").cases
foreach ($case in $cases) {
  $legacyNames = @()
  if ($case.name -eq "health-response-contract") {
    $legacyNames = @("health-timeout")
  }
  $existing = @($existingCases | Where-Object { $_.name -eq $case.name -or $legacyNames -contains $_.name }) | Select-Object -First 1
  $payload = @{ name = $case.name; repository_id = $repository.id; title = $case.title; description = $case.description; expected = $case.expected } | ConvertTo-Json
  if ($existing) {
    Invoke-RestMethod -Method Put -Uri "$ApiUrl/evaluations/cases/$($existing.id)" -ContentType "application/json" -Body $payload | Out-Null
  } else {
    Invoke-RestMethod -Method Post -Uri "$ApiUrl/evaluations/cases" -ContentType "application/json" -Body $payload | Out-Null
  }
}

Write-Output "Repository: $($repository.id)"
Write-Output "Path: $repoPath"
Write-Output "Benchmark cases seeded: $($cases.Count)"
