<#
.SYNOPSIS
  Stops and removes the PathLedger agent. Add -Purge to also delete its
  credential and buffered results (revoke the agent in the dashboard too).
#>
[CmdletBinding()]
param([switch]$Purge, [string]$TaskName = "PathLedger Agent")
$ErrorActionPreference = "Stop"

if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
Get-Process -Name "pathledger-agent" -ErrorAction SilentlyContinue | Stop-Process -Force
Remove-Item -Recurse -Force (Join-Path $env:ProgramFiles "PathLedger") -ErrorAction SilentlyContinue
if ($Purge) { Remove-Item -Recurse -Force (Join-Path $env:ProgramData "PathLedger") -ErrorAction SilentlyContinue }
Write-Host "PathLedger agent removed."
