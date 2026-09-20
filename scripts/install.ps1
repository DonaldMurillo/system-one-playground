param(
    [string]$Version = $env:SYSONESCRIPT_VERSION,
    [string]$InstallDir = $env:SYSONESCRIPT_INSTALL_DIR,
    [switch]$NoPathUpdate,
    [int]$WaitForProcessId = 0,
    [switch]$CleanupScript
)

$ErrorActionPreference = "Stop"
$repo = "DonaldMurillo/system-one-playground"
if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\SysOneScript\bin"
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($architecture) {
    "x64" { $arch = "x64" }
    "arm64" { $arch = "arm64" }
    default { throw "Unsupported architecture: $architecture" }
}

if ($Version) {
    $Version = $Version.TrimStart("v")
    $tag = "vscode-v$Version"
} else {
    $release = Invoke-RestMethod "https://api.github.com/repos/$repo/releases?per_page=30" |
        Where-Object { $_.tag_name.StartsWith("vscode-v") } |
        Select-Object -First 1
    if (-not $release) { throw "No SysOneScript CLI release found" }
    $tag = $release.tag_name
    $Version = $tag.Substring("vscode-v".Length)
}

$archive = "sysonescript-cli-v$Version-win32-$arch.zip"
$baseUrl = "https://github.com/$repo/releases/download/$tag"
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("sysonescript-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tempDir | Out-Null

try {
    if ($WaitForProcessId -gt 0) {
        Wait-Process -Id $WaitForProcessId -ErrorAction SilentlyContinue
    }
    Write-Host "Downloading SysOneScript $Version for win32-$arch..."
    $archivePath = Join-Path $tempDir $archive
    $checksumsPath = Join-Path $tempDir "checksums.txt"
    Invoke-WebRequest "$baseUrl/$archive" -OutFile $archivePath
    Invoke-WebRequest "$baseUrl/checksums.txt" -OutFile $checksumsPath

    $checksumLine = Get-Content $checksumsPath | Where-Object { $_ -match "^[0-9a-fA-F]{64}\s+$([regex]::Escape($archive))$" } | Select-Object -First 1
    if (-not $checksumLine) { throw "Release checksum is missing for $archive" }
    $expected = ($checksumLine -split "\s+")[0].ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 $archivePath).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "Checksum verification failed" }

    Expand-Archive $archivePath -DestinationPath $tempDir -Force
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item (Join-Path $tempDir "sos.exe") (Join-Path $InstallDir "sos.exe") -Force
    Copy-Item (Join-Path $tempDir "sysone.exe") (Join-Path $InstallDir "sysone.exe") -Force

    if (-not $NoPathUpdate) {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $entries = @($userPath -split ";" | Where-Object { $_ })
        if ($entries -notcontains $InstallDir) {
            $newPath = (@($entries) + $InstallDir) -join ";"
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            Write-Host "Added $InstallDir to your user PATH. Open a new terminal to use it."
        }
    }

    Write-Host "Installed sos.exe and sysone.exe to $InstallDir"
    Write-Host "Run: sysone version"
} finally {
    Remove-Item -Recurse -Force $tempDir -ErrorAction SilentlyContinue
    if ($CleanupScript) {
        Remove-Item -Force $PSCommandPath -ErrorAction SilentlyContinue
    }
}
