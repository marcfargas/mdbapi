#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Install mdbapi — Access database REST API service for Windows.

.DESCRIPTION
    Downloads and installs mdbapi to C:\Program Files\mdbapi (admin) or
    $HOME\.mdbapi (user). Creates a sample config if none exists.

    Usage (stable release):
        irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1 | iex

    Usage (develop build from CI):
        $env:MDBAPI_CHANNEL='develop'; irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1 | iex

    Usage (with parameters):
        & ([scriptblock]::Create((irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1))) -Develop

.PARAMETER Develop
    Install latest build from the develop branch CI artifacts instead of
    the latest stable release.

.PARAMETER Version
    Install a specific release version (e.g. "0.1.0"). Ignored with -Develop.

.PARAMETER InstallDir
    Override the installation directory. Default: C:\Program Files\mdbapi
    (admin) or $HOME\.mdbapi (user).

.PARAMETER Token
    GitHub token for private repo access. Also reads $env:GH_TOKEN or
    $env:GITHUB_TOKEN. Required for -Develop on private repos.
#>
[CmdletBinding()]
param(
    [switch]$Develop,
    [switch]$Tsnet,
    [string]$Version,
    [string]$InstallDir,
    [string]$Token
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # faster downloads

$Owner = 'marcfargas'
$Repo  = 'mdbapi'

# ---------------------------------------------------------------------------
# Environment variable overrides (for irm | iex which can't pass params)
# ---------------------------------------------------------------------------
if ($env:MDBAPI_CHANNEL -eq 'develop') { $Develop = $true }
if ($env:MDBAPI_TSNET -eq '1') { $Tsnet = $true }
if ($env:MDBAPI_VERSION -and -not $Version) { $Version = $env:MDBAPI_VERSION }
if ($env:MDBAPI_DIR -and -not $InstallDir) { $InstallDir = $env:MDBAPI_DIR }

# ---------------------------------------------------------------------------
# Architecture — amd64 only (ACE ODBC driver is not available for arm64)
# ---------------------------------------------------------------------------
$arch = 'amd64'
Write-Host "mdbapi installer — Windows amd64" -ForegroundColor Cyan

# ---------------------------------------------------------------------------
# Resolve GitHub auth token
# ---------------------------------------------------------------------------
if (-not $Token) {
    $Token = if ($env:GH_TOKEN) { $env:GH_TOKEN }
             elseif ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN }
             else { $null }
}

function Get-GitHubHeaders {
    $h = @{ 'Accept' = 'application/vnd.github+json'; 'User-Agent' = 'mdbapi-installer' }
    if ($Token) { $h['Authorization'] = "Bearer $Token" }
    return $h
}

# ---------------------------------------------------------------------------
# Determine install directory
# ---------------------------------------------------------------------------
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
           ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $InstallDir) {
    $InstallDir = if ($isAdmin) { Join-Path $env:ProgramFiles 'mdbapi' }
                  else          { Join-Path $HOME '.mdbapi' }
}
New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null

# ---------------------------------------------------------------------------
# Download
# ---------------------------------------------------------------------------
$tmpDir = Join-Path $env:TEMP "mdbapi-install-$(Get-Random)"
New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

try {
    if ($Develop) {
        $variant = if ($Tsnet) { 'tsnet' } else { 'standard' }
        Write-Host "Channel: develop ($variant)" -ForegroundColor Yellow
        $artifactName = if ($Tsnet) { "windows-$arch-tsnet" } else { "windows-$arch" }
        $zipPath = Join-Path $tmpDir 'artifact.zip'
        $downloaded = $false

        # --- Method 1: nightly.link (no auth required, public repos) ---
        $nightlyUrl = "https://nightly.link/$Owner/$Repo/workflows/ci.yml/develop/$artifactName.zip"
        Write-Host "Trying nightly.link..."
        try {
            Invoke-WebRequest -Uri $nightlyUrl -OutFile $zipPath -UseBasicParsing -TimeoutSec 30
            $size = [math]::Round((Get-Item $zipPath).Length / 1MB, 1)
            Write-Host "  Downloaded via nightly.link ($size MB)" -ForegroundColor Green
            $downloaded = $true
        } catch {
            Write-Host "  nightly.link unavailable, trying GitHub API..." -ForegroundColor DarkGray
        }

        # --- Method 2: GitHub API (needs token for artifact download) ---
        if (-not $downloaded) {
            $apiBase = "https://api.github.com/repos/$Owner/$Repo"
            $headers = Get-GitHubHeaders

            try {
                $artifacts = Invoke-RestMethod -Uri "$apiBase/actions/artifacts?name=$artifactName&per_page=1" `
                                               -Headers $headers
            } catch {
                if ($_.Exception.Response.StatusCode -eq 404 -or $_.Exception.Response.StatusCode -eq 401) {
                    throw "Cannot access CI artifacts. Set `$env:GH_TOKEN or pass -Token."
                }
                throw
            }
            if (-not $artifacts.artifacts -or $artifacts.total_count -eq 0) {
                throw "No CI artifacts found for '$artifactName'. Has CI run on develop recently?"
            }
            $artifact = $artifacts.artifacts[0]
            $created = [datetime]::Parse($artifact.created_at).ToString('yyyy-MM-dd HH:mm')
            Write-Host "  Found: $($artifact.name) ($created, $([math]::Round($artifact.size_in_bytes/1MB, 1)) MB)"

            if (-not $Token) {
                throw @"
GitHub artifact download requires authentication.
Options:
  1. Set `$env:GH_TOKEN before running the installer
  2. Pass -Token parameter
  3. Wait for nightly.link to index the latest CI run
"@
            }

            Write-Host "Downloading via GitHub API..."
            Invoke-WebRequest -Uri $artifact.archive_download_url -Headers $headers -OutFile $zipPath
        }

        # Extract
        Expand-Archive -Path $zipPath -DestinationPath $tmpDir -Force

    } else {
        # Stable release
        $headers = Get-GitHubHeaders
        $apiBase = "https://api.github.com/repos/$Owner/$Repo"

        if ($Version) {
            $tag = "v$Version"
            Write-Host "Channel: release $tag" -ForegroundColor Green
            $release = Invoke-RestMethod -Uri "$apiBase/releases/tags/$tag" -Headers $headers
        } else {
            Write-Host "Channel: latest release" -ForegroundColor Green
            $release = Invoke-RestMethod -Uri "$apiBase/releases/latest" -Headers $headers
        }

        Write-Host "  Version: $($release.tag_name)"

        # Find the right asset
        $suffix = if ($Tsnet) { "${arch}_tsnet" } else { $arch }
        $assetName = "mdbapi_$($release.tag_name.TrimStart('v'))_windows_$suffix.zip"
        $asset = $release.assets | Where-Object { $_.name -eq $assetName }
        if (-not $asset) {
            $available = ($release.assets | ForEach-Object { $_.name }) -join ', '
            throw "Asset '$assetName' not found in release. Available: $available"
        }

        # Download
        $zipPath = Join-Path $tmpDir 'release.zip'
        Write-Host "Downloading $assetName ($([math]::Round($asset.size/1MB, 1)) MB)..."
        $dlHeaders = Get-GitHubHeaders
        $dlHeaders['Accept'] = 'application/octet-stream'
        Invoke-WebRequest -Uri $asset.url -Headers $dlHeaders -OutFile $zipPath

        # Extract
        Expand-Archive -Path $zipPath -DestinationPath $tmpDir -Force
    }

    # ---------------------------------------------------------------------------
    # Install binaries
    # ---------------------------------------------------------------------------
    $installed = @()
    $binaries = if ($Tsnet) { @('mdbapi_tsnet.exe', 'mdbapi.exe') } else { @('mdbapi.exe', 'probe.exe') }
    foreach ($exe in $binaries) {
        $found = Get-ChildItem -Path $tmpDir -Filter $exe -Recurse | Select-Object -First 1
        if ($found) {
            # tsnet binary installs as mdbapi.exe
            $destName = if ($exe -eq 'mdbapi_tsnet.exe') { 'mdbapi.exe' } else { $exe }
            $dest = Join-Path $InstallDir $destName
            Copy-Item -Path $found.FullName -Destination $dest -Force
            $installed += $destName
        }
    }

    if ($installed.Count -eq 0) {
        throw "No executables found in download. Contents: $(Get-ChildItem $tmpDir -Recurse | Select-Object -ExpandProperty Name)"
    }

    # ---------------------------------------------------------------------------
    # Sample config
    # ---------------------------------------------------------------------------
    $cfgDir = 'C:\ProgramData\MDBService'
    $cfgPath = Join-Path $cfgDir 'config.yaml'
    if (-not (Test-Path $cfgPath)) {
        $configsDir = Join-Path $tmpDir 'configs'
        $exampleCfg = Join-Path $configsDir 'config.example.yaml'
        if (-not (Test-Path $exampleCfg)) { $exampleCfg = $null }

        if ($isAdmin) {
            New-Item -ItemType Directory -Path $cfgDir -Force | Out-Null
            if ($exampleCfg) {
                Copy-Item -Path $exampleCfg -Destination $cfgPath
                Write-Host "  Sample config: $cfgPath" -ForegroundColor DarkGray
            } else {
                $minCfg = @(
                    '# mdbapi configuration'
                    '# See https://github.com/marcfargas/mdbapi for documentation.'
                    ''
                    'server:'
                    '  listen: "127.0.0.1:8080"'
                    ''
                    'auth:'
                    '  keys: []   # Run: mdbapi keygen -add-to-config'
                    ''
                    'databases: []'
                    '  # - alias: mydb'
                    '  #   path: C:\Data\MyDatabase.mdb'
                ) -join "`r`n"
                [System.IO.File]::WriteAllText($cfgPath, $minCfg)
                Write-Host "  Minimal config: $cfgPath" -ForegroundColor DarkGray
            }
        }
    } else {
        Write-Host "  Config: run as admin to create $cfgPath" -ForegroundColor DarkGray
    }

    # ---------------------------------------------------------------------------
    # PATH
    # ---------------------------------------------------------------------------
    $currentPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($currentPath -notlike "*$InstallDir*") {
        [Environment]::SetEnvironmentVariable('Path', "$currentPath;$InstallDir", 'User')
        $env:Path = "$env:Path;$InstallDir"
        Write-Host "  Added to user PATH: $InstallDir" -ForegroundColor DarkGray
    }

    # ---------------------------------------------------------------------------
    # Summary
    # ---------------------------------------------------------------------------
    Write-Host ""
    Write-Host "Installed: $($installed -join ', ')" -ForegroundColor Green
    Write-Host "Location:  $InstallDir" -ForegroundColor Green

    $mdbapi = Join-Path $InstallDir 'mdbapi.exe'
    $ver = & $mdbapi version 2>$null
    if ($ver) { Write-Host "Version:   $ver" -ForegroundColor Green }

    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  1. Edit config:    notepad $cfgPath"
    Write-Host "  2. Generate key:   mdbapi keygen -add-to-config"
    Write-Host "  3. Install service: mdbapi install  (run as admin)"
    Write-Host "  4. Start service:  Start-Service MDBRestService"
    Write-Host ""

} finally {
    # Cleanup
    Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
