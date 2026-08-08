param(
    [string]$ApiUrl = "http://localhost:8080",
    [int]$TimeoutSeconds = 1800,
    [switch]$WarmUp
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

if ($WarmUp) {
    $warmUpBatch = Start-Suite "with_memory"
    Wait-Suite $warmUpBatch | Out-Null
    Write-Host "Warm-up complete"
}

$withBatch = Start-Suite "with_memory"
$withResult = Wait-Suite $withBatch
$withoutBatch = Start-Suite "without_memory"
$withoutResult = Wait-Suite $withoutBatch

$allRuns = @((Get-Evaluation).runs)

function Get-BatchSummary([string]$BatchID) {
    $subset = @($allRuns | Where-Object { $_.batch_id -eq $BatchID })
    $passed = @($subset | Where-Object { $_.passed }).Count
    $duration = ($subset | Measure-Object -Property duration_ms -Sum).Sum
    $memoryHits = 0
    $repairs = 0
    $successHits = 0
    $failureHits = 0
    $resolvedHits = 0
    $refinedHits = 0
    if ($subset.Count -gt 0) {
        $hitsSum = ($subset | Measure-Object -Property memory_hits -Sum).Sum
        $repairSum = ($subset | Measure-Object -Property repair_attempts -Sum).Sum
        $successSum = ($subset | Measure-Object -Property memory_success_hits -Sum).Sum
        $failureSum = ($subset | Measure-Object -Property memory_failure_hits -Sum).Sum
        $resolvedSum = ($subset | Measure-Object -Property memory_resolved_hits -Sum).Sum
        $refinedSum = ($subset | Measure-Object -Property memory_refined_hits -Sum).Sum
        if ($null -ne $hitsSum) { $memoryHits = $hitsSum }
        if ($null -ne $repairSum) { $repairs = $repairSum }
        if ($null -ne $successSum) { $successHits = $successSum }
        if ($null -ne $failureSum) { $failureHits = $failureSum }
        if ($null -ne $resolvedSum) { $resolvedHits = $resolvedSum }
        if ($null -ne $refinedSum) { $refinedHits = $refinedSum }
    }
    [pscustomobject]@{
        total = $subset.Count
        passed = $passed
        pass_rate = if ($subset.Count -gt 0) { [math]::Round($passed / $subset.Count, 4) } else { 0 }
        avg_duration_ms = if ($subset.Count -gt 0) { [math]::Round($duration / $subset.Count) } else { 0 }
        memory_hits = $memoryHits
        repair_attempts = $repairs
        memory_success_hits = $successHits
        memory_failure_hits = $failureHits
        memory_resolved_hits = $resolvedHits
        memory_refined_hits = $refinedHits
    }
}

$withSummary = Get-BatchSummary $withBatch
$withoutSummary = Get-BatchSummary $withoutBatch
Write-Host "Memory A/B complete"
Write-Host "with_memory: total=$($withSummary.total) passed=$($withSummary.passed) pass_rate=$($withSummary.pass_rate) avg_duration_ms=$($withSummary.avg_duration_ms) memory_hits=$($withSummary.memory_hits) repair_attempts=$($withSummary.repair_attempts) success_hits=$($withSummary.memory_success_hits) failure_hits=$($withSummary.memory_failure_hits) resolved_hits=$($withSummary.memory_resolved_hits) refined_hits=$($withSummary.memory_refined_hits)"
Write-Host "without_memory: total=$($withoutSummary.total) passed=$($withoutSummary.passed) pass_rate=$($withoutSummary.pass_rate) avg_duration_ms=$($withoutSummary.avg_duration_ms) memory_hits=$($withoutSummary.memory_hits) repair_attempts=$($withoutSummary.repair_attempts) success_hits=$($withoutSummary.memory_success_hits) failure_hits=$($withoutSummary.memory_failure_hits) resolved_hits=$($withoutSummary.memory_resolved_hits) refined_hits=$($withoutSummary.memory_refined_hits)"
