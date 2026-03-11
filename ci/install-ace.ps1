# install-ace.ps1 -- Downloads and installs the Microsoft Access Database Engine
# 2016 (64-bit) by resolving the current URL from the Microsoft Download Center.
# Called from ci/Dockerfile.testenv and directly on CI runners.
#
# Why not hardcode the URL? It has already changed once (404 in CI).
# Scraping the details page keeps this resilient to CDN path changes.
#
# ASCII-only file: PowerShell inside Windows containers may misread UTF-8
# multi-byte characters (e.g. em-dash) as Windows-1252, breaking string parsing.

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

$detailsUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=54920'

Write-Host "Resolving ACE download URL from: $detailsUrl"
$page = (Invoke-WebRequest -Uri $detailsUrl -UseBasicParsing).Content

$url = [regex]::Match(
    $page,
    'https://download\.microsoft\.com/download/[^\s"]+accessdatabaseengine_X64\.exe',
    [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
).Value

if (-not $url) {
    throw "Could not find ACE 64-bit download URL on ${detailsUrl} - page layout may have changed."
}

Write-Host "Downloading: $url"
Invoke-WebRequest -Uri $url -OutFile ace64.exe

Write-Host "Installing (silent)..."
$proc = Start-Process -FilePath '.\ace64.exe' -ArgumentList '/quiet' -Wait -PassThru
if ($proc.ExitCode -ne 0) {
    throw "ACE installer exited with code $($proc.ExitCode)"
}

Remove-Item -Force ace64.exe
Write-Host "ACE 2016 installed."
