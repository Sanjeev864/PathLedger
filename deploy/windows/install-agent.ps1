<#
.SYNOPSIS
  Installs the PathLedger agent on a Windows PC as a background task that
  starts with the computer, runs as SYSTEM and restarts if it stops.

.EXAMPLE
  # In an elevated PowerShell, next to pathledger-agent-windows-amd64.exe:
  .\install-agent.ps1 -ControllerUrl https://pathledger.example.com -EnrollToken ple_...

.NOTES
  The enrolment token is used once and never written to disk. The agent's
  credential is stored in a folder only SYSTEM and Administrators can read.
  No administrator rights are needed for the probes themselves; they are
  needed only to install a computer-wide background task.
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$ControllerUrl,
  [Parameter(Mandatory = $true)][string]$EnrollToken,
  [string]$Binary = (Join-Path $PSScriptRoot "pathledger-agent-windows-$(if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }).exe"),
  [string]$TaskName = "PathLedger Agent"
)
$ErrorActionPreference = "Stop"

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  throw "Run this script in an elevated PowerShell (Run as administrator)."
}
if (-not (Test-Path $Binary)) { throw "Agent binary not found: $Binary" }

$installDir = Join-Path $env:ProgramFiles "PathLedger"
$stateDir   = Join-Path $env:ProgramData "PathLedger\agent"
$logFile    = Join-Path $env:ProgramData "PathLedger\agent.log"
$exe        = Join-Path $installDir "pathledger-agent.exe"

# Replace a running agent cleanly.
if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  Get-Process -Name "pathledger-agent" -ErrorAction SilentlyContinue | Stop-Process -Force
}

New-Item -ItemType Directory -Force -Path $installDir, $stateDir | Out-Null
Copy-Item -Force $Binary $exe

# The state folder holds the agent's credential: SYSTEM and Administrators only.
& icacls $stateDir /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Could not restrict permissions on $stateDir" }

# Enrol once, now, with the token only in this process's environment.
$env:PATHLEDGER_ENROLL_TOKEN = $EnrollToken
try {
  & $exe -controller $ControllerUrl -state-dir $stateDir -enroll-only -log-format text
  if ($LASTEXITCODE -ne 0) { throw "Enrolment failed (exit code $LASTEXITCODE). Check the URL and that the token is unused and not expired." }
} finally {
  Remove-Item Env:\PATHLEDGER_ENROLL_TOKEN -ErrorAction SilentlyContinue
}

$action   = New-ScheduledTaskAction -Execute $exe -Argument "-state-dir `"$stateDir`" -log-file `"$logFile`""
$trigger  = New-ScheduledTaskTrigger -AtStartup
$runAs    = New-ScheduledTaskPrincipal -UserId "SYSTEM" -LogonType ServiceAccount -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable `
              -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) `
              -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $runAs -Settings $settings `
  -Description "PathLedger network monitoring agent" -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

Write-Host "PathLedger agent installed and running."
Write-Host "  Program: $exe"
Write-Host "  Log:     $logFile"
Write-Host "  Remove:  .\uninstall-agent.ps1"
