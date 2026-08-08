param(
    [string]$ApiUrl = "http://localhost:8080",
    [int]$TimeoutSeconds = 1800
)

$ErrorActionPreference = "Stop"

function Get-Evaluation {
    Invoke-RestMethod -Uri "$ApiUrl/evaluations"
}

function Start-Suite([string]$Mode) {
    $evaluation = Get-Evaluation
    $caseIds = @($evaluation.cases | ForEach-Object { $_.id })
    if ($caseIds.Count -eq 0) {
        throw "No benchmark cases are registered. Run seed-demo.ps1 first."
    }
    $body = @{
        name     = "$Mode memory suite"
        mode     = $Mode
        case_ids = $caseIds
    } | ConvertTo-Json
    $result = Invoke-RestMethod -Method Post -Uri "$ApiUrl/evaluations/suites" -ContentType "application/json" -Body $body
    Write-Host "Started $Mode suite: $($result.batch.id)"
    return $result.batch.id
}

function Wait-Suite([string]$BatchID) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        $evaluation = Get-Evaluation
        $batch = $evaluation.batches | Where-Object { $_.id -eq $BatchID } | Select-Object -First 1
        if (-not $batch) {
            Start-Sleep -Seconds 1
            continue
        }
        if ($batch.status -in @("completed", "failed", "cancelled")) {
            return $batch
        }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for batch $BatchID"
}

$withBatch = Start-Suite "with_memory"
$withoutBatch = Start-Suite "without_memory"
Wait-Suite $withBatch | Out-Null
Wait-Suite $withoutBatch | Out-Null

$metrics = (Get-Evaluation).metrics.by_memory
Write-Host "Memory A/B complete"
Write-Host "with_memory: total=$($metrics.with_memory.total) passed=$($metrics.with_memory.passed) avg_duration_ms=$($metrics.with_memory.avg_duration_ms) memory_hits=$($metrics.with_memory.memory_hits) repair_attempts=$($metrics.with_memory.repair_attempts)"
Write-Host "without_memory: total=$($metrics.without_memory.total) passed=$($metrics.without_memory.passed) avg_duration_ms=$($metrics.without_memory.avg_duration_ms) memory_hits=$($metrics.without_memory.memory_hits) repair_attempts=$($metrics.without_memory.repair_attempts)"
