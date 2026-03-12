# install-ace.ps1 -- Downloads and installs the Microsoft Access Database Engine
# 64-bit installer by resolving the current URL from the Microsoft Download Center.
# Called from CI runners.
#
# Why not hardcode the URL? It has already changed once (404 in CI).
# Scraping the details page keeps this resilient to CDN path changes.
#
# ASCII-only file: PowerShell inside Windows containers may misread UTF-8
# multi-byte characters as Windows-1252, breaking string parsing.

param(
    [ValidateSet('2010', '2016')]
    [string]$Version = '2016'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

switch ($Version) {
    '2010' { $detailsUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=13255' }
    '2016' { $detailsUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=54920' }
    default { throw "Unsupported ACE version: $Version" }
}

Write-Host "Resolving ACE $Version download URL from: $detailsUrl"
$page = (Invoke-WebRequest -Uri $detailsUrl -UseBasicParsing).Content

$url = [regex]::Match(
    $page,
    'https://download\.microsoft\.com/download/[^\s"]+accessdatabaseengine_X64\.exe',
    [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
).Value

if (-not $url) {
    throw "Could not find ACE 64-bit download URL on ${detailsUrl} - page layout may have changed."
}

$installerPath = "ace64-$Version.exe"

Write-Host "Downloading: $url"
Invoke-WebRequest -Uri $url -OutFile $installerPath

Write-Host "Installing ACE $Version (silent)..."
$proc = Start-Process -FilePath $installerPath -ArgumentList '/quiet' -Wait -PassThru
if ($proc.ExitCode -ne 0) {
    throw "ACE installer exited with code $($proc.ExitCode)"
}

Remove-Item -Force $installerPath
Write-Host "ACE $Version installed."
