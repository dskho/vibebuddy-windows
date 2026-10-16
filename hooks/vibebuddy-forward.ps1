<#
.SYNOPSIS
  vibebuddy-forward.ps1 — fail-open event forwarder for AI coding CLIs (Windows port).

.DESCRIPTION
  PowerShell port of hooks/vibebuddy-forward.sh from semantic-craft/iOS-vibebuddy.
  Reads the CLI's hook event JSON on stdin and POSTs it verbatim to the local
  vibebuddy daemon at /hook?agent=<source>. Never blocks the CLI: any failure
  is swallowed and we always exit 0.

  Usage (in a CLI's command hook):
    powershell -NoProfile -ExecutionPolicy Bypass -File "<bin>\vibebuddy-forward.ps1" claude
    powershell -NoProfile -ExecutionPolicy Bypass -File "<bin>\vibebuddy-forward.ps1" --source grok
#>
param(
    [string]$Source = "claude",
    [string]$Event = ""
)
if ($Source -eq "--source") { $Source = $Event; $Event = "" }
if (-not $Source) { $Source = "claude" }

$port = $env:VIBEBUDDY_PORT
if (-not $port) { $port = "9876" }
$timeoutMs = 3000
if ($Source -eq "codex") {
    # Codex status hooks run asynchronously, except SessionEnd. Bound delivery
    # to a missing/wedged daemon to one second.
    $timeoutMs = 1000
}

$supportDir = $env:VIBEBUDDY_SUPPORT_DIR
if (-not $supportDir) { $supportDir = Join-Path $env:APPDATA "vibebuddy" }
$tokenFile = $env:VIBEBUDDY_TOKEN_FILE
if (-not $tokenFile) { $tokenFile = Join-Path $supportDir "token" }
$token = $env:VIBEBUDDY_TOKEN
if (-not $token -and (Test-Path -LiteralPath $tokenFile)) {
    try { $token = (Get-Content -LiteralPath $tokenFile -Raw -ErrorAction Stop).Trim() }
    catch { $token = $null }
}
# No token -> no Authorization header -> the daemon 401s and we swallow it (fail-open).

$body = [Console]::In.ReadToEnd()
if ($null -eq $body) { $body = "" }
if ($Source -eq "antigravity" -and @("PreInvocation", "PreToolUse", "PostToolUse", "Stop") -contains $Event) {
    $body = '{"event":"' + $Event + '","payload":' + $body + '}'
}

try {
    Add-Type -AssemblyName System.Net.Http -ErrorAction SilentlyContinue | Out-Null
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromMilliseconds($timeoutMs)
    $url = "http://127.0.0.1:${port}/hook?agent=${Source}"
    $content = New-Object System.Net.Http.StringContent($body, [System.Text.Encoding]::UTF8, "application/json")
    if ($token) {
        $client.DefaultRequestHeaders.Authorization =
            New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $token)
    }
    $task = $client.PostAsync($url, $content)
    $task.Wait() | Out-Null
    $client.Dispose()
}
catch {
    # fail-open: never break the agent
}
exit 0
