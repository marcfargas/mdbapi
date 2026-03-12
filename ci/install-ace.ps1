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
    '2010' {
        # Microsoft retired the ACE 2010 download page. Use Wayback Machine archive.
        $url = 'https://web.archive.org/web/20240214170634if_/https://download.microsoft.com/download/2/4/3/24375141-E08D-4803-AB0E-10F2E3A07AAA/AccessDatabaseEngine_X64.exe'
        Write-Host "Downloading ACE 2010 from Wayback Machine archive..."
        Invoke-WebRequest -Uri $url -OutFile ace64.exe
    }
    '2016' {
        $detailsUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=54920'
        Write-Host "Resolving ACE 2016 URL from: $detailsUrl"
        $page = (Invoke-WebRequest -Uri $detailsUrl -UseBasicParsing).Content

        $url = [regex]::Match(
            $page,
            'https://download\.microsoft\.com/download/[^\s"]+accessdatabaseengine_X64\.exe',
            [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
        ).Value

        if (-not $url) {
            throw "Could not find ACE 2016 URL"
        }

        Write-Host "Downloading: $url"
        Invoke-WebRequest -Uri $url -OutFile ace64.exe
    }
}

Write-Host "Installing ACE $Version (silent)..."
$proc = Start-Process -FilePath '.\ace64.exe' -ArgumentList '/quiet' -Wait -PassThru
if ($proc.ExitCode -ne 0) {
    throw "ACE installer exited with code $($proc.ExitCode)"
}

Remove-Item -Force ace64.exe
Write-Host "ACE $Version installed."
