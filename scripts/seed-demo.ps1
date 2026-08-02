param(
  [string]$ApiUrl = "http://127.0.0.1:8080"
)

$repoPath = (Resolve-Path (Join-Path $PSScriptRoot "..\demo\sample-repo")).Path
$repository = Invoke-RestMethod -Uri "$ApiUrl/repositories"
$repository = $repository | Where-Object { $_.path -eq $repoPath } | Select-Object -First 1
if (-not $repository) {
  $repository = Invoke-RestMethod -Method Post -Uri "$ApiUrl/repositories" -ContentType "application/json" -Body (@{ name = "CodeCoDriver Demo"; path = $repoPath } | ConvertTo-Json)
}

$cases = @(
  @{ name = "add-subtract"; title = "Add subtraction helper"; description = "Add a public Subtract helper to calculator.go and add a focused unit test."; expected = @("calculator.go", "calculator_test.go") },
  @{ name = "divide-error"; title = "Improve divide error coverage"; description = "Review Divide error handling and add a test that documents the zero divisor contract."; expected = @("calculator.go", "calculator_test.go") }
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
