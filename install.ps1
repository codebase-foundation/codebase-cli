# Codebase CLI installer (Windows)
# Usage: irm https://raw.githubusercontent.com/codebase-foundation/codebase-cli/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$Repo = "codebase-foundation/codebase-cli"
$Binary = "codebase"

# Default install directory: ~/.codebase/bin (no admin needed)
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:USERPROFILE ".codebase\bin" }

# Detect architecture
$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }

# Get latest release tag
Write-Host "Finding latest release..."
$Release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "codebase-installer" }
$Tag = $Release.tag_name
if (-not $Tag) {
    Write-Error "Could not find latest release. Check https://github.com/$Repo/releases"
    exit 1
}
Write-Host "Latest version: $Tag"

# Download
$Archive = "${Binary}_windows_${Arch}.zip"
$Url = "https://github.com/$Repo/releases/download/$Tag/$Archive"

Write-Host "Downloading $Url..."
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "codebase-install-$(Get-Random)"
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {
    $ZipPath = Join-Path $TmpDir $Archive
    Invoke-WebRequest -Uri $Url -OutFile $ZipPath -UseBasicParsing
    Expand-Archive -Path $ZipPath -DestinationPath $TmpDir -Force

    # Create install directory if needed
    if (-not (Test-Path $InstallDir)) {
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    }

    # Copy binary
    $ExeName = "${Binary}.exe"
    $Src = Join-Path $TmpDir $ExeName
    if (-not (Test-Path $Src)) {
        # goreleaser might nest it differently
        $Src = Get-ChildItem -Path $TmpDir -Recurse -Filter $ExeName | Select-Object -First 1 -ExpandProperty FullName
    }
    Copy-Item -Path $Src -Destination (Join-Path $InstallDir $ExeName) -Force

    Write-Host ""
    Write-Host "Installed $Binary $Tag to $InstallDir\$ExeName"

    # Add to user PATH if not already there
    $UserPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
    if ($UserPath -notlike "*$InstallDir*") {
        Write-Host ""
        Write-Host "Adding $InstallDir to your PATH..."
        $NewPath = "$InstallDir;$UserPath"
        [System.Environment]::SetEnvironmentVariable("PATH", $NewPath, "User")
        $env:PATH = "$InstallDir;$env:PATH"
        Write-Host "Done. New terminal windows will have 'codebase' available."
        Write-Host "For this terminal, it's already available."
    }

    Write-Host ""
    Write-Host "Run 'codebase' in any project directory to get started."
}
finally {
    Remove-Item -Path $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
