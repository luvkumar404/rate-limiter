param([Parameter(Mandatory = $true)][string]$Directory)
$ErrorActionPreference = 'Stop'
$lines = @(
    '| Algorithm | Offered /s | Whole-run completed /s | Whole-run valid /s | p50 ms | p95 ms | p99 ms | Allowed | Blocked | Invalid | Dropped |',
    '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|'
)
foreach ($file in Get-ChildItem -LiteralPath $Directory -Filter '*.json' | Sort-Object Name) {
    $r = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json
    if ($r.metadata.mode -notin @('correctness', 'throughput')) { continue }
    $m = $r.metrics
    $latency = $m.'http_req_duration{phase:load}'.values
    $validRate = ($m.allowed.values.count + $m.blocked.values.count) / ($r.state.testRunDurationMs / 1000)
    $lines += '| {0} | {1} | {2:F1} | {10:F1} | {3:F2} | {4:F2} | {5:F2} | {6} | {7} | {8:P2} | {9} |' -f `
        $r.metadata.rule, $r.metadata.requested_rate, $m.iterations.values.rate, `
        $latency.med, $latency.'p(95)', $latency.'p(99)', `
        $m.allowed.values.count, $m.blocked.values.count, `
        $m.invalid_decisions.values.rate, $m.dropped_iterations.values.count, $validRate
}
$lines | Set-Content -LiteralPath (Join-Path $Directory 'summary.md') -Encoding utf8
$lines
