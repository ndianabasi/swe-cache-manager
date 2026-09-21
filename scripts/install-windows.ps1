$ErrorActionPreference = "Stop"

$installDir = $env:SWE_CACHE_INSTALL_DIR
if ([string]::IsNullOrWhiteSpace($installDir)) {
    $installDir = Join-Path $env:LOCALAPPDATA "Programs\swe-cache"
}

New-Item -ItemType Directory -Force $installDir | Out-Null
Copy-Item -Force (Join-Path $PSScriptRoot "..\bin\swe-cache.exe") (Join-Path $installDir "swe-cache.exe")

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathEntries = @($userPath -split ";" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
if ($pathEntries -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable("Path", (($pathEntries + $installDir) -join ";"), "User")
}

Write-Host "Installed swe-cache in $installDir. Open a new terminal before invoking swe-cache by name."
