# install-ace.ps1 -- Downloads and installs a Microsoft Access ODBC driver.
# Called from CI runners.
#
# Supported versions:
#   2010  - ACE 2010 via Wayback Machine (supports Access 97+)
#   2016  - ACE 2016 via Microsoft Download Center (Access 2000+)
#   365   - Microsoft 365 Access Runtime via Office Deployment Tool (Access 2000+)
#
# ASCII-only file: PowerShell inside Windows containers may misread UTF-8
# multi-byte characters as Windows-1252, breaking string parsing.

param(
    [ValidateSet('2010', '2016', '365')]
    [string]$Version = '2016',

    [ValidateSet('32', '64')]
    [string]$Arch = '64'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

switch ($Version) {
    '2010' {
        # Microsoft retired the ACE 2010 download page. Use Wayback Machine archive.
        $base = 'https://web.archive.org/web/20240214170634if_/https://download.microsoft.com/download/2/4/3/24375141-E08D-4803-AB0E-10F2E3A07AAA'
        $exe = if ($Arch -eq '64') { 'AccessDatabaseEngine_X64.exe' } else { 'AccessDatabaseEngine.exe' }
        $url = "$base/$exe"
        Write-Host "Downloading ACE 2010 ${Arch}-bit from Wayback Machine archive..."
        Invoke-WebRequest -Uri $url -OutFile ace.exe

        Write-Host "Installing ACE 2010 ${Arch}-bit (silent)..."
        $proc = Start-Process -FilePath '.\ace.exe' -ArgumentList '/quiet' -Wait -PassThru
        if ($proc.ExitCode -ne 0) { throw "ACE 2010 installer exited with code $($proc.ExitCode)" }
        Remove-Item -Force ace.exe
        Write-Host "ACE 2010 ${Arch}-bit installed."
    }

    '2016' {
        $detailsUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=54920'
        Write-Host "Resolving ACE 2016 URL from: $detailsUrl"
        $page = (Invoke-WebRequest -Uri $detailsUrl -UseBasicParsing).Content

        $pattern = if ($Arch -eq '64') {
            'https://download\.microsoft\.com/download/[^\s"]+accessdatabaseengine_X64\.exe'
        } else {
            'https://download\.microsoft\.com/download/[^\s"]+accessdatabaseengine\.exe'
        }
        $url = [regex]::Match($page, $pattern, [System.Text.RegularExpressions.RegexOptions]::IgnoreCase).Value
        if (-not $url) { throw "Could not find ACE 2016 ${Arch}-bit download URL" }

        Write-Host "Downloading: $url"
        Invoke-WebRequest -Uri $url -OutFile ace.exe

        Write-Host "Installing ACE 2016 ${Arch}-bit (silent)..."
        $proc = Start-Process -FilePath '.\ace.exe' -ArgumentList '/quiet' -Wait -PassThru
        if ($proc.ExitCode -ne 0) { throw "ACE 2016 installer exited with code $($proc.ExitCode)" }
        Remove-Item -Force ace.exe
        Write-Host "ACE 2016 ${Arch}-bit installed."
    }

    '365' {
        # Microsoft 365 Access Runtime -- direct C2R installer from Microsoft.
        # Source: https://support.microsoft.com/en-us/office/download-and-install-microsoft-365-access-runtime-185c5a32-8ba9-491e-ac76-91cbe3ea09c9
        # This downloads only the Access Runtime (~150 MB), not the full Office suite.
        $platform = if ($Arch -eq '64') { 'x64' } else { 'x86' }
        $url = "https://c2rsetup.officeapps.live.com/c2r/download.aspx?ProductreleaseID=AccessRuntimeRetail&language=en-us&platform=$platform"

        $installer = Join-Path $env:TEMP 'OfficeSetup.exe'
        Write-Host "Downloading Microsoft 365 Access Runtime ${Arch}-bit..."
        Invoke-WebRequest -Uri $url -OutFile $installer

        Write-Host "Installing Access Runtime (this may take a minute)..."
        $proc = Start-Process -FilePath $installer -ArgumentList '/quiet /norestart' -Wait -PassThru
        if ($proc.ExitCode -ne 0 -and $proc.ExitCode -ne 3010) {
            throw "Access Runtime install failed ($($proc.ExitCode))"
        }

        Remove-Item -Force $installer -ErrorAction SilentlyContinue
        Write-Host "Microsoft 365 Access Runtime ${Arch}-bit installed."
    }
}

# Verify the ODBC driver is registered
$drivers = Get-ItemProperty 'HKLM:\SOFTWARE\ODBC\ODBCINST.INI\ODBC Drivers' -ErrorAction SilentlyContinue
$accessDriver = $drivers.PSObject.Properties | Where-Object { $_.Name -like '*Access*' -and $_.Name -like '*.mdb*' }
if ($accessDriver) {
    Write-Host "ODBC driver registered: $($accessDriver.Name)" -ForegroundColor Green
} else {
    Write-Host "WARNING: No Access ODBC driver found in registry after install" -ForegroundColor Yellow
}
