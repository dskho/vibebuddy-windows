<#
.SYNOPSIS
  Install-VibeBuddyHooks.ps1 — install / repair / uninstall vibebuddy hooks on Windows.

.DESCRIPTION
  PowerShell port of `vibebuddyd hooks install|uninstall|status` (HookInstaller
  in VibeBuddyMacCore) from semantic-craft/iOS-vibebuddy.

  Copies the hook scripts to a stable location (%APPDATA%\vibebuddy\bin) and
  wires them into each CLI's config. Only vibebuddy's own entries are ever
  added, replaced or removed; your other hooks and settings are left alone.
  Every changed config is backed up first under %APPDATA%\vibebuddy\backups.

  Usage:
    powershell -ExecutionPolicy Bypass -File Install-VibeBuddyHooks.ps1 [-Agent all|claude|codex|grok|cursor] [-Approval] [-Statusline] [-Uninstall]

  Environment overrides: VIBEBUDDY_PORT, VIBEBUDDY_SUPPORT_DIR, CLAUDE_CONFIG_DIR,
  CODEX_HOME, GROK_HOME, CURSOR_HOME.
#>
[CmdletBinding()]
param(
    [ValidateSet("all", "claude", "codex", "grok", "cursor")]
    [string]$Agent = "all",
    [switch]$Approval,
    [switch]$Statusline,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

$supportDir = $env:VIBEBUDDY_SUPPORT_DIR
if (-not $supportDir) { $supportDir = Join-Path $env:APPDATA "vibebuddy" }
$binDir = Join-Path $supportDir "bin"
$scriptDir = Split-Path $MyInvocation.MyCommand.Path -Parent

# Recognises our entries: the hook script basenames we (and upstream) install,
# in .ps1 or legacy .sh form, plus the early inline `curl ...:9876/hook` form.
$script:OursPattern = '(vibebuddy-forward|approval-hook|vibebuddy-statusline|capture-terminal|cursor-followup)\.(sh|ps1)|127\.0\.0\.1:9876/hook\?agent='
function Test-OursCommand([string]$cmd) {
    return ($null -ne $cmd) -and ($cmd -match $script:OursPattern)
}

function Get-JsonFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    try {
        return Get-Content -LiteralPath $Path -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        throw "Refusing to modify ${Path}: not valid JSON ($($_.Exception.Message))"
    }
}

function Set-JsonFile([string]$Path, $Obj) {
    $dir = Split-Path $Path -Parent
    if ($dir -and -not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory $dir -Force | Out-Null }
    ($Obj | ConvertTo-Json -Depth 40) | Set-Content -LiteralPath $Path -Encoding UTF8
}

function Backup-Config([string]$Path, [string]$AgentName, [string]$Key) {
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $bdir = Join-Path $supportDir "backups\$AgentName-$Key"
    New-Item -ItemType Directory $bdir -Force | Out-Null
    $ts = (Get-Date).ToUniversalTime().ToString("yyyyMMddHHmmss")
    $base = Split-Path $Path -Leaf
    $dest = Join-Path $bdir "$base.$ts"
    Copy-Item -LiteralPath $Path $dest -Force
    Get-ChildItem -LiteralPath $bdir -Filter "$base.*" |
        Sort-Object Name -Descending | Select-Object -Skip 10 | Remove-Item -Force
    return $dest
}

function Write-Manifest([string]$AgentName, [string]$ConfigPath, [array]$Entries) {
    $manifestPath = Join-Path $supportDir "hooks-manifest.json"
    $manifest = Get-JsonFile $manifestPath
    if (-not $manifest) { $manifest = [PSCustomObject]@{} }
    $manifest | Add-Member -NotePropertyName $AgentName -NotePropertyValue (
        [PSCustomObject]@{
            config  = $ConfigPath
            entries = $Entries
            time    = (Get-Date).ToUniversalTime().ToString("o")
        }
    ) -Force
    Set-JsonFile $manifestPath $manifest
}

function Get-PathKey([string]$Path) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($Path.ToLowerInvariant())
    $hash = $sha.ComputeHash($bytes)
    return (($hash | ForEach-Object { $_.ToString("x2") }) -join "").Substring(0, 8)
}

# --- Claude/Codex group-format helpers ("hooks": { event: [{matcher, hooks:[{type,command,timeout?}]}] }) ---

function New-CommandHook([string]$Command, [int]$Timeout = 0) {
    $h = [PSCustomObject]@{ type = "command"; command = $Command }
    if ($Timeout -gt 0) { $h | Add-Member -NotePropertyName "timeout" -NotePropertyValue $Timeout }
    return $h
}

function Set-GroupHooks($Settings, [string]$Event, [array]$Groups) {
    if (-not $Settings.hooks) {
        $Settings | Add-Member -NotePropertyName "hooks" -NotePropertyValue ([PSCustomObject]@{})
    }
    $existing = @()
    if ($Settings.hooks.PSObject.Properties[$Event]) { $existing = @($Settings.hooks.$Event) }
    $kept = @($existing | Where-Object {
            $cmds = @($_.hooks | ForEach-Object { $_.command })
            -not ($cmds | Where-Object { Test-OursCommand $_ })
        })
    $Settings.hooks | Add-Member -NotePropertyName $Event -NotePropertyValue @($kept + $Groups) -Force
}

function Remove-OurGroups($Settings) {
    $removed = 0
    if (-not $Settings.hooks) { return 0 }
    foreach ($prop in @($Settings.hooks.PSObject.Properties)) {
        $groups = @($prop.Value | Where-Object {
                $cmds = @($_.hooks | ForEach-Object { $_.command })
                -not ($cmds | Where-Object { Test-OursCommand $_ })
            })
        $removed += (@($prop.Value).Count - $groups.Count)
        if ($groups.Count -eq 0) { $Settings.hooks.PSObject.Properties.Remove($prop.Name) }
        else { $Settings.hooks | Add-Member -NotePropertyName $prop.Name -NotePropertyValue $groups -Force }
    }
    return $removed
}

# --- Cursor flat-format helpers ("hooks": { event: [{command, timeout}] }) ---

function Set-CursorEvent($Doc, [string]$Event, [array]$Entries) {
    $existing = @()
    if ($Doc.hooks.PSObject.Properties[$Event]) { $existing = @($Doc.hooks.$Event) }
    $kept = @($existing | Where-Object { -not (Test-OursCommand $_.command) })
    $Doc.hooks | Add-Member -NotePropertyName $Event -NotePropertyValue @($Entries + $kept) -Force
}

function Remove-OurCursorEvents($Doc) {
    $removed = 0
    foreach ($prop in @($Doc.hooks.PSObject.Properties)) {
        $items = @($prop.Value | Where-Object { -not (Test-OursCommand $_.command) })
        $removed += (@($prop.Value).Count - $items.Count)
        if ($items.Count -eq 0) { $Doc.hooks.PSObject.Properties.Remove($prop.Name) }
        else { $Doc.hooks | Add-Member -NotePropertyName $prop.Name -NotePropertyValue $items -Force }
    }
    return $removed
}

function Install-Claude([bool]$WithApproval, [bool]$WithStatusline) {
    $lines = @()
    $cfgDir = $env:CLAUDE_CONFIG_DIR
    if (-not $cfgDir) { $cfgDir = Join-Path $HOME ".claude" }
    $settingsPath = Join-Path $cfgDir "settings.json"
    $ps = "powershell -NoProfile -ExecutionPolicy Bypass -File"
    $fwd = "$ps `"$binDir\vibebuddy-forward.ps1`" claude"

    if ($Uninstall) {
        if (-not (Test-Path -LiteralPath $settingsPath)) { return @("claude: nothing to remove") }
        $settings = Get-JsonFile $settingsPath
        $backup = Backup-Config $settingsPath "claude" "settings"
        $removed = Remove-OurGroups $settings
        # restore status line
        if ($settings.statusLine -and (Test-OursCommand $settings.statusLine.command)) {
            $key = ""
            $defaultDir = Join-Path $HOME ".claude"
            if ($cfgDir -ne $defaultDir) { $key = Get-PathKey $cfgDir }
            $origFile = if ($key) { Join-Path $supportDir "statusline-original.$key.cmd" } else { Join-Path $supportDir "statusline-original.cmd" }
            if (Test-Path -LiteralPath $origFile) {
                $orig = (Get-Content -LiteralPath $origFile -Raw).Trim()
                $settings | Add-Member -NotePropertyName "statusLine" -NotePropertyValue ([PSCustomObject]@{ type = "command"; command = $orig }) -Force
                $lines += "claude: status line restored"
            }
            else {
                $settings.PSObject.Properties.Remove("statusLine")
                $lines += "claude: status line removed (no original was saved)"
            }
        }
        Set-JsonFile $settingsPath $settings
        $lines = @("claude: removed $removed vibebuddy hook group(s) from $settingsPath") + $lines
        return $lines
    }

    $settings = Get-JsonFile $settingsPath
    if (-not $settings) { $settings = [PSCustomObject]@{} }
    $backup = Backup-Config $settingsPath "claude" "settings"
    if ($backup) { $lines += "claude: backup -> $backup" }

    $plan = @{}
    foreach ($e in @("SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd")) {
        $plan[$e] = @([PSCustomObject]@{ matcher = ""; hooks = @(New-CommandHook $fwd) })
    }
    if ($WithApproval) {
        $appr = "$ps `"$binDir\approval-hook.ps1`" claude 60"
        $plan["PermissionRequest"] = @([PSCustomObject]@{ matcher = "*"; hooks = @(New-CommandHook $appr 75) })
        $plan["PreToolUse"] = @(
            $plan["PreToolUse"][0],
            [PSCustomObject]@{ matcher = "AskUserQuestion"; hooks = @(New-CommandHook $appr 75) }
        )
    }
    foreach ($kv in $plan.GetEnumerator()) { Set-GroupHooks $settings $kv.Key $kv.Value }

    if ($WithStatusline) {
        $ours = "$ps `"$binDir\vibebuddy-statusline.ps1`""
        $current = $null
        if ($settings.statusLine) { $current = $settings.statusLine.command }
        if ($current -ne $ours) {
            $key = ""
            $defaultDir = Join-Path $HOME ".claude"
            if ($cfgDir -ne $defaultDir) { $key = Get-PathKey $cfgDir }
            $origFile = if ($key) { Join-Path $supportDir "statusline-original.$key.cmd" } else { Join-Path $supportDir "statusline-original.cmd" }
            if ($current -and -not (Test-OursCommand $current) -and -not (Test-Path -LiteralPath $origFile)) {
                $current | Set-Content -LiteralPath $origFile -Encoding UTF8
                $lines += "claude: saved original status line -> $origFile"
            }
            $settings | Add-Member -NotePropertyName "statusLine" -NotePropertyValue ([PSCustomObject]@{ type = "command"; command = $ours }) -Force
            $lines += "claude: status line wired"
        }
    }

    Set-JsonFile $settingsPath $settings
    $entries = @($plan.Keys | ForEach-Object { "hooks.$_" })
    Write-Manifest "claude" $settingsPath $entries
    $lines = @("claude: installed vibebuddy hooks$($WithApproval ? ' + approval gate' : ''): $settingsPath") + $lines
    return $lines
}

function Install-Codex([bool]$WithApproval) {
    $lines = @()
    $codexHome = $env:CODEX_HOME
    if (-not $codexHome) { $codexHome = Join-Path $HOME ".codex" }
    $hooksPath = Join-Path $codexHome "hooks.json"
    $ps = "powershell -NoProfile -ExecutionPolicy Bypass -File"
    $fwd = "$ps `"$binDir\vibebuddy-forward.ps1`" codex"

    if ($Uninstall) {
        if (-not (Test-Path -LiteralPath $hooksPath)) { return @("codex: nothing to remove") }
        $root = Get-JsonFile $hooksPath
        $backup = Backup-Config $hooksPath "codex" "hooks"
        $removed = Remove-OurGroups $root
        Set-JsonFile $hooksPath $root
        return @("codex: removed $removed vibebuddy hook group(s) from $hooksPath",
            "codex: re-trust hooks via /hooks in a fresh session if entries changed")
    }

    $root = Get-JsonFile $hooksPath
    if (-not $root) { $root = [PSCustomObject]@{} }
    $backup = Backup-Config $hooksPath "codex" "hooks"
    if ($backup) { $lines += "codex: backup -> $backup" }

    $events = @("SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
        "PreCompact", "PostCompact", "SubagentStart", "SubagentStop", "Stop", "Interrupt", "SessionEnd")
    foreach ($e in $events) {
        $cmd = $fwd; $timeout = 0
        if ($WithApproval -and $e -eq "PermissionRequest") {
            $cmd = "$ps `"$binDir\approval-hook.ps1`" codex"; $timeout = 30
        }
        Set-GroupHooks $root $e @([PSCustomObject]@{ matcher = ""; hooks = @(New-CommandHook $cmd $timeout) })
    }
    Set-JsonFile $hooksPath $root
    Write-Manifest "codex" $hooksPath @($events | ForEach-Object { "hooks.$_" })
    $lines = @("codex: installed vibebuddy hooks$($WithApproval ? ' + approval gate' : ''): $hooksPath",
        "codex: start a fresh session, run /hooks, and trust the VibeBuddy entries") + $lines
    return $lines
}

function Install-Grok([bool]$WithApproval, [bool]$WithStatusline) {
    $lines = @()
    $grokHome = $env:GROK_HOME
    if (-not $grokHome) { $grokHome = Join-Path $HOME ".grok" }
    $hooksDir = Join-Path $grokHome "hooks"
    $target = Join-Path $hooksDir "vibebuddy.json"
    $ps = "powershell -NoProfile -ExecutionPolicy Bypass -File"
    $fwd = "$ps `"$binDir\vibebuddy-forward.ps1`" grok"

    if ($Uninstall) {
        if ((Test-Path -LiteralPath $target) -and ((Get-Content -LiteralPath $target -Raw) -match "vibebuddy-forward")) {
            Remove-Item -LiteralPath $target -Force
            $lines += "grok: removed $target"
            $legacy = "$target.vibebuddy-backup"
            if (Test-Path -LiteralPath $legacy) {
                Move-Item -LiteralPath $legacy $target -Force
                $lines += "grok: restored backup"
            }
            if (-not (Get-ChildItem -LiteralPath $hooksDir -Force | Where-Object { -not $_.PSIsContainer })) {
                Remove-Item -LiteralPath $hooksDir -Force
            }
        }
        else { $lines += "grok: nothing to remove (not installed by vibebuddy)" }
        return $lines
    }

    New-Item -ItemType Directory $hooksDir -Force | Out-Null
    $hooks = [ordered]@{}
    foreach ($e in @("SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
            "Stop", "StopFailure", "StopCancelled", "Notification", "SubagentStart", "SubagentStop", "SessionEnd")) {
        $cmd = $fwd; $timeout = 5
        if ($WithApproval -and $e -eq "PreToolUse") {
            $cmd = "$ps `"$binDir\approval-hook.ps1`" grok"; $timeout = 30
        }
        $hooks[$e] = @([PSCustomObject]@{ type = "command"; command = $cmd; timeout = $timeout })
    }
    $doc = [PSCustomObject]@{ hooks = [PSCustomObject]$hooks }
    $newText = ($doc | ConvertTo-Json -Depth 20)
    $existing = $null
    if (Test-Path -LiteralPath $target) { $existing = Get-Content -LiteralPath $target -Raw }
    if ($existing -and $existing -notmatch "vibebuddy-forward" -and -not (Test-Path -LiteralPath "$target.vibebuddy-backup")) {
        Copy-Item -LiteralPath $target "$target.vibebuddy-backup" -Force
        $lines += "grok: foreign file set aside -> $target.vibebuddy-backup"
    }
    if ($existing -ne $newText) {
        $backup = Backup-Config $target "grok" "hooks"
        if ($backup) { $lines += "grok: backup -> $backup" }
        $newText | Set-Content -LiteralPath $target -Encoding UTF8
        $lines += "grok: installed vibebuddy hooks$($WithApproval ? ' + approval gate' : ''): $target"
    }
    else { $lines += "grok: already installed: $target" }
    if ($WithApproval) {
        $lines += "grok: phone answers are authoritative only with [ui] permission_mode = `"always-approve`" in config.toml"
    }
    $lines += "grok: reload hooks in grok (`/hooks` -> 'r') or start a new session"

    if ($WithStatusline) {
        $key = Get-PathKey $grokHome
        $slCmd = "$ps `"$binDir\vibebuddy-statusline.ps1`" grok $key"
        $configPath = Join-Path $grokHome "config.toml"
        $origFile = Join-Path $supportDir "grok-statusline-original.$key.cmd"
        $changed = Set-TomlStatusLine $configPath $slCmd $origFile
        $lines += "grok: status line $($changed ? 'wired' : 'already wired') in $configPath"
    }
    Write-Manifest "grok" $target @("hooks.*")
    return $lines
}

function Set-TomlStatusLine([string]$ConfigPath, [string]$Command, [string]$OrigFile) {
    # Narrow line-based edit: only touches `status_line` under `[ui]`.
    # The command is written as a TOML literal string (single quotes) so
    # Windows backslashes need no escaping.
    $lines = @()
    if (Test-Path -LiteralPath $ConfigPath) { $lines = @(Get-Content -LiteralPath $ConfigPath) }
    $out = @()
    $inUi = $false; $sawUi = $false; $done = $false
    foreach ($line in $lines) {
        if ($line -match '^\s*\[') {
            if ($inUi -and -not $done) { $out += "status_line = '$Command'"; $done = $true }
            $inUi = $line -match '^\s*\[ui\]\s*$'
            if ($inUi) { $sawUi = $true }
            $out += $line
            continue
        }
        if ($inUi -and $line -match '^\s*status_line\s*=') {
            if (-not $done) {
                $orig = ($line -replace '^\s*status_line\s*=\s*', '').Trim()
                if ($orig -and $orig -notmatch 'vibebuddy-statusline' -and -not (Test-Path -LiteralPath $OrigFile)) {
                    $orig | Set-Content -LiteralPath $OrigFile -Encoding UTF8
                }
                $done = $true
            }
            $out += "status_line = '$Command'"
            continue
        }
        $out += $line
    }
    if ($inUi -and -not $done) { $out += "status_line = '$Command'"; $done = $true }
    if (-not $sawUi) { $out += ""; $out += "[ui]"; $out += "status_line = '$Command'"; $done = $true }
    $before = ($lines -join "`n")
    $after = ($out -join "`n")
    if ($before -ne $after) {
        Backup-Config $ConfigPath "grok" "config" | Out-Null
        $dir = Split-Path $ConfigPath -Parent
        if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory $dir -Force | Out-Null }
        $after | Set-Content -LiteralPath $ConfigPath -Encoding UTF8
        return $true
    }
    return $false
}

function Install-Cursor([bool]$WithApproval) {
    $lines = @()
    $cursorHome = $env:CURSOR_HOME
    if (-not $cursorHome) { $cursorHome = Join-Path $HOME ".cursor" }
    $hooksPath = Join-Path $cursorHome "hooks.json"
    $ps = "powershell -NoProfile -ExecutionPolicy Bypass -File"
    $fwd = "$ps `"$binDir\vibebuddy-forward.ps1`" cursor"

    if ($Uninstall) {
        if (-not (Test-Path -LiteralPath $hooksPath)) { return @("cursor: nothing to remove") }
        $doc = Get-JsonFile $hooksPath
        $backup = Backup-Config $hooksPath "cursor" "hooks"
        $removed = Remove-OurCursorEvents $doc
        if ($removed -eq 0) { return @("cursor: nothing to remove (not installed by vibebuddy)") }
        Set-JsonFile $hooksPath $doc
        return @("cursor: removed $removed vibebuddy hook entries, kept your own: $hooksPath")
    }

    $doc = Get-JsonFile $hooksPath
    if (-not $doc) { $doc = [PSCustomObject]@{} }
    if (-not $doc.PSObject.Properties["version"]) { $doc | Add-Member -NotePropertyName "version" -NotePropertyValue 1 }
    if (-not $doc.hooks) { $doc | Add-Member -NotePropertyName "hooks" -NotePropertyValue ([PSCustomObject]@{}) }
    $backup = Backup-Config $hooksPath "cursor" "hooks"
    if ($backup) { $lines += "cursor: backup -> $backup" }

    foreach ($e in @("sessionStart", "sessionEnd", "beforeSubmitPrompt", "postToolUse",
            "postToolUseFailure", "afterFileEdit", "afterAgentResponse", "afterAgentThought",
            "preCompact", "subagentStart", "subagentStop")) {
        Set-CursorEvent $doc $e @([PSCustomObject]@{ command = $fwd; timeout = 5 })
    }
    $preCmd = $fwd; $preTimeout = 5
    if ($WithApproval) {
        $preCmd = "$ps `"$binDir\approval-hook.ps1`" cursor"; $preTimeout = 35
    }
    Set-CursorEvent $doc "preToolUse" @([PSCustomObject]@{ command = $preCmd; timeout = $preTimeout })

    Set-JsonFile $hooksPath $doc
    Write-Manifest "cursor" $hooksPath @("hooks.*")
    $lines = @("cursor: installed vibebuddy hooks$($WithApproval ? ' + approval gate' : ''): $hooksPath",
        "cursor: hooks.json is reloaded on save; restart Cursor if it does not pick it up") + $lines
    return $lines
}

# --- main ---

New-Item -ItemType Directory $binDir -Force | Out-Null
foreach ($script in @("vibebuddy-forward.ps1", "approval-hook.ps1", "vibebuddy-statusline.ps1")) {
    $src = Join-Path $scriptDir $script
    if (-not (Test-Path -LiteralPath $src)) { throw "missing hook script: $src" }
    Copy-Item -LiteralPath $src (Join-Path $binDir $script) -Force
}
Write-Host "hook scripts -> $binDir"

$agents = @("claude", "codex", "grok", "cursor")
if ($Agent -ne "all") { $agents = @($Agent) }

$allLines = @()
foreach ($a in $agents) {
    try {
        switch ($a) {
            "claude" { $allLines += Install-Claude $Approval $Statusline }
            "codex" { $allLines += Install-Codex $Approval }
            "grok" { $allLines += Install-Grok $Approval $Statusline }
            "cursor" { $allLines += Install-Cursor $Approval }
        }
    }
    catch {
        $allLines += "${a}: FAILED - $($_.Exception.Message)"
    }
    $allLines += ""
}
$allLines | Where-Object { $_ -ne "" } | ForEach-Object { Write-Host $_ }
