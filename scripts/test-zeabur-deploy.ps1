param(
    [string]$BaseUrl = "https://cpaplusplus.zeabur.app",
    [string]$ApiKey = $env:CPA_API_KEY
)

$ErrorActionPreference = "Stop"

Write-Host "=== Zeabur deploy smoke test ==="
Write-Host "Target: $BaseUrl"

function Test-Endpoint {
    param(
        [string]$Name,
        [string]$Path,
        [hashtable]$Headers = @{}
    )

    $uri = "$BaseUrl$Path"
    try {
        $response = Invoke-WebRequest -Uri $uri -Headers $Headers -TimeoutSec 20 -UseBasicParsing
        Write-Host "[PASS] $Name -> HTTP $($response.StatusCode)"
        return $true
    }
    catch {
        $status = $null
        if ($_.Exception.Response) {
            $status = [int]$_.Exception.Response.StatusCode
        }
        Write-Host "[FAIL] $Name -> HTTP $status ($($_.Exception.Message))"
        return $false
    }
}

$rootOk = Test-Endpoint -Name "root" -Path "/"
$modelsOk = $false

if ($ApiKey) {
    $modelsOk = Test-Endpoint -Name "models" -Path "/v1/models" -Headers @{
        Authorization = "Bearer $ApiKey"
    }
}
else {
    Write-Host "[SKIP] models endpoint (set CPA_API_KEY to test authenticated route)"
}

if ($rootOk -and ($modelsOk -or -not $ApiKey)) {
    Write-Host "Smoke test finished."
    exit 0
}

Write-Host "Smoke test failed."
exit 1
