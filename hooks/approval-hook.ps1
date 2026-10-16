<#
.SYNOPSIS
  approval-hook.ps1 — blocking PreToolUse approval forwarder (Windows port).

.DESCRIPTION
  PowerShell port of hooks/approval-hook.sh from semantic-craft/iOS-vibebuddy.
  Reads the hook JSON on stdin, asks the local daemon, and echoes its permission
  decision verbatim. On any failure it prints nothing. Cursor exits 1 (hook
  failure -> Cursor's native permission fallback); other agents exit 0 to
  preserve their existing hook contracts.

  Usage: approval-hook.ps1 [source [hold]]
    no argument  -> Claude Code (snake_case envelope, hookSpecificOutput reply)
    grok         -> Grok Build  (camelCase envelope, {"decision":...} reply)
    cursor       -> Cursor      ({"permission": "allow"|"deny"} reply)
  The source is forwarded as /approval?agent=<source>.

  hold: how many seconds the daemon may keep this call waiting for the phone.
  Clamped to the daemon's own 25-120 s, so we never give up before it does.
#>
param(
    [string]$Source = "",
    [string]$Hold = ""
)

# Only Grok's own gate may ask on Grok's behalf (mirrors approval-hook.sh).
if ($env:GROK_HOOK_EVENT -and $Source -ne "grok") { exit 0 }

$port = $env:VIBEBUDDY_PORT
if (-not $port) { $port = "9876" }
$url = "http://127.0.0.1:${port}/approval"
if ($Source) { $url += "?agent=${Source}" }

$maxSec = 30
if ($Hold -match '^\d+$') {
    $h = [int]$Hold
    if ($h -gt 120) { $h = 120 }
    if ($h -lt 25) { $h = 25 }
    $sep = if ($url.Contains('?')) { '&' } else { '?' }
    $url += "${sep}hold=${h}"
    $maxSec = $h + 10
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

$body = [Console]::In.ReadToEnd()
if ($null -eq $body) { $body = "" }

$resp = ""
try {
    Add-Type -AssemblyName System.Net.Http -ErrorAction SilentlyContinue | Out-Null
    $client = New-Object System.Net.Http.HttpClient
    $client.Timeout = [TimeSpan]::FromSeconds($maxSec)
    $content = New-Object System.Net.Http.StringContent($body, [System.Text.Encoding]::UTF8, "application/json")
    if ($token) {
        $client.DefaultRequestHeaders.Authorization =
            New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $token)
    }
    $r = $client.PostAsync($url, $content).GetAwaiter().GetResult()
    if ($r.IsSuccessStatusCode) {
        $resp = $r.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    }
    $client.Dispose()
}
catch {
    # HTTP/transport failures never forward an error body or a partial decision.
    $resp = ""
}

# Exit 1 with an empty response can block Cursor permission hooks. Exit 1
# gives no approval decision and leaves Cursor's own permission check in charge.
if ($Source -eq "cursor" -and -not $resp) { exit 1 }
if ($resp) { [Console]::Out.Write($resp) }
exit 0
