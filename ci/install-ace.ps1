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
        # Microsoft 365 Access Runtime -- Click-to-Run, installed via Office Deployment Tool.
        # This is what most production machines have (Office 365, Access 2019/2021 Runtime).
        # Download: ~200-500 MB. Registers the same ODBC driver as ACE 2016.

        $odtDir = Join-Path $env:TEMP 'odt'
        New-Item -ItemType Directory -Path $odtDir -Force | Out-Null

        # 1. Download Office Deployment Tool
        $odtUrl = 'https://www.microsoft.com/en-us/download/details.aspx?id=49117'
        Write-Host "Resolving ODT URL from: $odtUrl"
        $page = (Invoke-WebRequest -Uri $odtUrl -UseBasicParsing).Content
        $odtExeUrl = [regex]::Match(
            $page,
            'https://download\.microsoft\.com/download/[^\s"]+officedeploymenttool[^\s"]*\.exe',
            [System.Text.RegularExpressions.RegexOptions]::IgnoreCase
        ).Value

        if (-not $odtExeUrl) { throw "Could not find ODT download URL" }
        Write-Host "Downloading ODT: $odtExeUrl"
        $odtExe = Join-Path $odtDir 'odt.exe'
        Invoke-WebRequest -Uri $odtExeUrl -OutFile $odtExe

        # 2. Extract ODT (self-extracting archive)
        Write-Host "Extracting ODT..."
        $proc = Start-Process -FilePath $odtExe -ArgumentList "/quiet /extract:`"$odtDir`"" -Wait -PassThru
        if ($proc.ExitCode -ne 0) { throw "ODT extraction failed ($($proc.ExitCode))" }

        # 3. Write configuration for AccessRuntime 64-bit
        $configXml = Join-Path $odtDir 'config.xml'
        @(
            '<Configuration>'
            "  <Add OfficeClientEdition=`"$Arch`" Channel=`"Current`">"
            '    <Product ID="AccessRuntimeRetail">'
            '      <Language ID="en-us" />'
            '    </Product>'
            '  </Add>'
            '  <Display Level="None" AcceptEULA="TRUE" />'
            '</Configuration>'
        ) -join "`r`n" | Set-Content -Path $configXml -Encoding ASCII

        # 4. Download and install
        $setup = Join-Path $odtDir 'setup.exe'
        Write-Host "Downloading and installing Microsoft 365 Access Runtime (this may take a few minutes)..."
        $proc = Start-Process -FilePath $setup -ArgumentList "/configure `"$configXml`"" -Wait -PassThru
        if ($proc.ExitCode -ne 0) { throw "Access Runtime install failed ($($proc.ExitCode))" }

        Remove-Item -Path $odtDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host "Microsoft 365 Access Runtime installed."
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
