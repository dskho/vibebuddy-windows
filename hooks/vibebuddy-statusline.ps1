<#
.SYNOPSIS
  vibebuddy-statusline.ps1 — status line wrapper for Claude Code and Grok Build (Windows port).

.DESCRIPTION
  PowerShell port of hooks/vibebuddy-statusline.sh from
  semantic-craft/iOS-vibebuddy. The CLI runs the configured status line command
  on every state change with a JSON document on stdin. This script copies that
  document to the local vibebuddy daemon's /statusline (fail-open, 1 s cap),
  then hands the very same input to the status line command that was configured
  before vibebuddy was installed and prints its output — so what the terminal
  shows never changes. With no original command, it prints nothing.

  The original command lives in a plain file next to the daemon token
  (the installer writes it; uninstall restores it), so this script never
  parses JSON.

  Recursion guard: a wrapper that ends up running itself would recurse forever.
  A nested run forwards nothing and prints nothing.
#>
param(
    [string]$Arg1 = "",
    [string]$Arg2 = ""
)

if ($env:VIBEBUDDY_STATUSLINE_ACTIVE) { exit 0 }
$env:VIBEBUDDY_STATUSLINE_ACTIVE = "1"

# `grok` first: installed into Grok's [ui.status_line]; otherwise Claude.
$agent = "claude"
$key = $Arg1
if ($Arg1 -eq "grok") { $agent = "grok"; $key = $Arg2 }

$port = $env:VIBEBUDDY_PORT
if (-not $port) { $port = "9876" }
$supportDir = $env:VIBEBUDDY_SUPPORT_DIR
if (-not $supportDir) { $supportDir = Join-Path $env:APPDATA "vibebuddy" }
$tokenFile = $env:VIBEBUDDY_TOKEN_FILE
if (-not $tokenFile) { $tokenFile = Join-Path $supportDir "token" }
$token = $env:VIBEBUDDY_TOKEN
if (-not $token -and (Test-Path -LiteralPath $tokenFile)) {
    try { $token = (Get-Content -LiteralPath $tokenFile -Raw -ErrorAction Stop).Trim() }
    catch { $token = $null }
}

$stdinText = [Console]::In.ReadToEnd()
if ($null -eq $stdinText) { $stdinText = "" }

# Forward in the background of the CLI's render: the daemon must never delay it.
if ($token) {
    $route = "statusline"
    if ($agent -eq "grok") { $route = "statusline?agent=grok" }
    try {
        Add-Type -AssemblyName System.Net.Http -ErrorAction SilentlyContinue | Out-Null
        $client = New-Object System.Net.Http.HttpClient
        $client.Timeout = [TimeSpan]::FromSeconds(1)
        $content = New-Object System.Net.Http.StringContent($stdinText, [System.Text.Encoding]::UTF8, "application/json")
        $client.DefaultRequestHeaders.Authorization =
            New-Object System.Net.Http.Headers.AuthenticationHeaderValue("Bearer", $token)
        $client.PostAsync("http://127.0.0.1:${port}/${route}", $content).Wait() | Out-Null
        $client.Dispose()
    }
    catch {
        # fail-open
    }
}

# $Arg1 is the installer's key for the config this wrapper was installed into,
# so each Claude directory / Grok home runs its own saved original.
$origFile = $null
if ($agent -eq "grok") {
    if ($key -match '^[0-9a-f]+$') { $origFile = Join-Path $supportDir "grok-statusline-original.${key}.cmd" }
}
else {
    if ($key -match '^[0-9a-f]+$') { $origFile = Join-Path $supportDir "statusline-original.${key}.cmd" }
    else { $origFile = Join-Path $supportDir "statusline-original.cmd" }
}

if ($origFile -and (Test-Path -LiteralPath $origFile)) {
    $original = (Get-Content -LiteralPath $origFile -Raw -ErrorAction SilentlyContinue)
    if ($original) { $original = $original.Trim() }
    # Never run a saved command that names this wrapper (recursion guard).
    if ($original -and $original -notmatch 'vibebuddy-statusline\.ps1') {
        try {
            if ($original -match '\.ps1(\s|$)') {
                $stdinText | powershell -NoProfile -ExecutionPolicy Bypass -Command $original
            }
            else {
                $stdinText | cmd /c $original
            }
        }
        catch {
            # fail-open: the terminal display degrades, the daemon still got its copy
        }
    }
}
exit 0
