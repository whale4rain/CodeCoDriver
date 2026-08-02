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
  @{ name = "health-timeout"; title = "Harden health endpoint timeout behavior"; description = "Review the health endpoint and add focused coverage for its response contract and timeout-safe behavior."; expected = @("internal/healthcheck", "cmd/server") },
  @{ name = "pagination-validation"; title = "Improve pagination input validation"; description = "Review pagination request validation and add a focused test for invalid page parameters without changing the public API."; expected = @("pkg/pagination", "internal") }
)

${existingCases} = (Invoke-RestMethod -Uri "$ApiUrl/evaluations").cases
foreach ($case in $cases) {
  if ($existingCases | Where-Object { $_.name -eq $case.name }) { continue }
  $payload = @{ name = $case.name; repository_id = $repository.id; title = $case.title; description = $case.description; expected = $case.expected } | ConvertTo-Json
  Invoke-RestMethod -Method Post -Uri "$ApiUrl/evaluations/cases" -ContentType "application/json" -Body $payload | Out-Null
}

Write-Output "Repository: $($repository.id)"
Write-Output "Path: $repoPath"
Write-Output "Benchmark cases seeded: $($cases.Count)"
