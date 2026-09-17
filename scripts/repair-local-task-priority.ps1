param(
    [string]$TaskName = "CLIProxyAPI-Local"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$watchdogPath = Join-Path $PSScriptRoot "keep-alive-cliproxy.ps1"
$exePath = Join-Path $projectRoot "cli-proxy-api.exe"
$task = Get-ScheduledTask -TaskName $TaskName
if (-not ($task.Actions | Where-Object {
    $_.Arguments -and $_.Arguments.IndexOf($watchdogPath, [StringComparison]::OrdinalIgnoreCase) -ge 0
})) {
    throw "Task $TaskName does not run this checkout's watchdog"
}

# Task Scheduler defaults to priority 7 (BelowNormal), inherited by the proxy.
# Priority 4 gives this latency-sensitive service the normal scheduling class.
$settings = $task.Settings
$settings.Priority = 4
Set-ScheduledTask -TaskName $TaskName -Settings $settings | Out-Null

# Updating task settings does not update processes that are already running.
Get-CimInstance Win32_Process | Where-Object {
    ($_.ExecutablePath -and [string]::Equals($_.ExecutablePath, $exePath, [StringComparison]::OrdinalIgnoreCase)) -or
    ($_.Name -match '^(pwsh|powershell)\.exe$' -and $_.CommandLine -and
        $_.CommandLine.IndexOf($watchdogPath, [StringComparison]::OrdinalIgnoreCase) -ge 0)
} | ForEach-Object {
    $process = Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue
    if ($process) {
        $process.PriorityClass = [Diagnostics.ProcessPriorityClass]::Normal
        Write-Output "Normal priority: $($process.ProcessName) pid=$($process.Id)"
    }
}

if ((Get-ScheduledTask -TaskName $TaskName).Settings.Priority -ne 4) {
    throw "Task priority did not persist"
}
Write-Output "Task $TaskName now starts at normal priority"
