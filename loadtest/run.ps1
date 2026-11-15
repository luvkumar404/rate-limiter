param(
    [int[]]$Rates = @(1000, 5000, 10000),
    [string]$Duration = '20s',
    [string]$Run = (Get-Date -Format 'yyyyMMdd-HHmmss'),
    [switch]$SkipBuild
)
$ErrorActionPreference = 'Stop'
$PSDefaultParameterValues = @{ 'Out-File:Encoding' = 'utf8' }
if ($Run -notmatch '^[a-zA-Z0-9_-]+$') { throw 'Run must be a simple directory name' }
Set-Location (Split-Path $PSScriptRoot -Parent)
$env:RATELIMITER_HTTP_PORT = '18090'
$env:RATELIMITER_PROMETHEUS_PORT = '19090'
$compose = @('compose', '-p', 'ratelimiter-bench', '-f', 'docker-compose.yml', '-f', 'loadtest/compose.yaml')
$output = Join-Path $PSScriptRoot "results/$Run"
if (Test-Path -LiteralPath $output) { throw "Results already exist: $output; select another Run" }
New-Item -ItemType Directory -Force $output | Out-Null
if ($env:OS -ne 'Windows_NT') {
    # k6's container UID must be able to write this disposable results folder.
    & chmod a+rwx $output
    if ($LASTEXITCODE -ne 0) { throw "Cannot make results writable: $output" }
}
Get-FileHash "$PSScriptRoot/check.js", "$PSScriptRoot/rules.yaml" | Format-List | Out-File "$output/source-sha256.txt"
function Docker-Step([string[]]$Arguments) {
    & docker @Arguments
    if ($LASTEXITCODE -ne 0) { throw "Docker command failed: $Arguments" }
}
$buildArgs = @('--build')
if ($SkipBuild) { $buildArgs = @('--no-build', '--pull', 'never') }
$failed = @()
try {
    Docker-Step ($compose + @('up', '-d') + $buildArgs + @('--wait', '--wait-timeout', '120'))
    Docker-Step @('version') | Out-File "$output/environment.txt"
    Docker-Step @('info') | Out-File "$output/environment.txt" -Append
    Docker-Step ($compose + @('config')) | Out-File "$output/compose.yaml"
    Docker-Step ($compose + @('images', '--format', 'json')) | Out-File "$output/images.json"
    $cases = @(@{ Mode = 'correctness'; Rule = 'sliding_window'; Rate = 1000; Name = 'correctness' })
    foreach ($rule in @('token_bucket', 'sliding_window', 'fixed_window')) {
        foreach ($rate in $Rates) {
            $cases += @{ Mode = 'throughput'; Rule = $rule; Rate = $rate; Name = "$rule-$rate" }
        }
    }
    foreach ($case in $cases) {
        Write-Host "Running $($case.Name)"
        # Windows PowerShell treats redirected native stderr as ErrorRecords.
        # Docker progress belongs in the log; the exit code decides success.
        $ErrorActionPreference = 'Continue'
        & docker @compose run --rm --no-deps -e "MODE=$($case.Mode)" -e "RULE=$($case.Rule)" -e "RATE=$($case.Rate)" -e "DURATION=$Duration" -e "OUTPUT=/results/$Run/$($case.Name).json" k6 run /scripts/check.js *> "$output/$($case.Name).log"
        $caseExit = $LASTEXITCODE
        $ErrorActionPreference = 'Stop'
        if ($caseExit -ne 0) { $failed += $case.Name }
    }
    $containers = @(Docker-Step ($compose + @('ps', '-q')))
    Docker-Step (@('stats', '--no-stream') + $containers) | Out-File "$output/stats.txt"
} finally {
    try {
        Docker-Step ($compose + @('logs', '--no-color', '--tail', '100')) | Out-File "$output/cluster.log"
    } finally {
        Docker-Step ($compose + @('down', '--volumes'))
    }
}
& "$PSScriptRoot/summarize.ps1" -Directory $output
if ($failed.Count) { throw "Thresholds failed (raw results preserved): $failed" }
