param(
  [string]$ApiUrl = "http://127.0.0.1:8080",
  [string]$Mode = "agent"
)

$stamp = Get-Date -Format "yyyy-MM-dd-HHmmss"
$payload = @{ name = "eval-$Mode-$stamp"; mode = $Mode; case_ids = @() } | ConvertTo-Json -Depth 4
$suite = Invoke-RestMethod -Method Post -Uri "$ApiUrl/evaluations/suites" -ContentType "application/json" -Body $payload
$batchId = $suite.batch.id
Write-Output "Suite: $batchId mode=$Mode cases=$($suite.runs.Count)"

$deadline = (Get-Date).AddMinutes(30)
do {
  Start-Sleep -Seconds 10
  $evaluation = Invoke-RestMethod -Uri "$ApiUrl/evaluations" -TimeoutSec 10
  $batch = @($evaluation.batches | Where-Object { $_.id -eq $batchId }) | Select-Object -First 1
} while ($batch -and $batch.status -eq "running" -and (Get-Date) -lt $deadline)

if (-not $batch) {
  throw "Batch $batchId not found"
}
if ($batch.status -eq "running") {
  throw "Batch $batchId timed out"
}

$metrics = $evaluation.metrics
Write-Output ""
Write-Output "=== SUMMARY ==="
Write-Output "Batch: $($batch.name) status=$($batch.status)"
Write-Output "Total=$($batch.total) completed=$($batch.completed) passed=$($batch.passed)"
Write-Output "Pass rate=$([Math]::Round($metrics.pass_rate * 100, 1))% auto_human=$($metrics.auto_human) external_errors=$($metrics.external_errors)"

Write-Output ""
Write-Output "=== BY CATEGORY ==="
foreach ($key in $metrics.by_category.PSObject.Properties.Name | Sort-Object) {
  $item = $metrics.by_category.$key
  Write-Output ("{0,-16} passed={1}/{2} auto_human={3}" -f $key, $item.passed, $item.total, $item.auto_human)
}

Write-Output ""
Write-Output "=== RUNS ==="
foreach ($run in $evaluation.runs | Where-Object { $_.batch_id -eq $batchId } | Sort-Object created_at) {
  Write-Output ("{0,-38} mode={1,-8} status={2,-22} passed={3}" -f $run.case_id, $run.mode, $run.status, $run.passed)
}

$reportDir = Join-Path $PSScriptRoot "..\test-reports"
$reportDir = (Resolve-Path $reportDir).Path
$reportPath = Join-Path $reportDir "eval-$Mode-$stamp.json"
$evaluation | ConvertTo-Json -Depth 12 | Set-Content -Encoding UTF8 $reportPath
$detailed = Invoke-RestMethod -Uri "$ApiUrl/evaluations/report" -TimeoutSec 20
$detailedReportPath = Join-Path $reportDir "eval-report-$Mode-$stamp.json"
$detailed | ConvertTo-Json -Depth 12 | Set-Content -Encoding UTF8 $detailedReportPath
Write-Output ""
Write-Output "Report: $reportPath"
Write-Output "Detailed report: $detailedReportPath"
