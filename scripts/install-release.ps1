[CmdletBinding()]
param(
    [string]$InstallDir = $env:TELOS_INSTALL_DIR,
    [string]$AgentSkillsDir = $env:TELOS_AGENT_SKILLS_DIR,
    [switch]$Help
)
$ErrorActionPreference = 'Stop'
if ($Help) {
    Write-Output 'Usage: install.ps1 [-InstallDir PATH] [-AgentSkillsDir PATH] [-Help]'
    Write-Output 'Install the Telos CLI and agent skill on Windows amd64. Local execution is unavailable.'
    return
}
if ($env:OS -ne 'Windows_NT' -or $env:PROCESSOR_ARCHITECTURE -ne 'AMD64') {
    throw 'Telos requires Windows amd64. Run this installer from a 64-bit PowerShell session.'
}
$version = '@TELOS_VERSION@'
$releaseBase = $env:TELOS_RELEASE_BASE_URL
if ([string]::IsNullOrWhiteSpace($releaseBase)) { $releaseBase = 'https://usetelos.ai/releases' }
$releaseBase = $releaseBase.TrimEnd('/')
if ([string]::IsNullOrWhiteSpace($InstallDir)) { $InstallDir = Join-Path $env:USERPROFILE '.local\bin' }
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$stage = Join-Path ([IO.Path]::GetTempPath()) ('telos-install-' + [guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($stage) | Out-Null
try {
    $artifact = 'telos-windows-amd64.exe'
    $checksums = Join-Path $stage 'SHA256SUMS'
    $binary = Join-Path $stage 'telos.exe'
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/$version/SHA256SUMS" -OutFile $checksums
    $entries = @([IO.File]::ReadAllLines($checksums) | Where-Object { $_ -match ('^[a-fA-F0-9]{64}\s+' + [regex]::Escape($artifact) + '$') })
    if ($entries.Count -ne 1) { throw "Missing or duplicate checksum for $artifact" }
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/$version/$artifact" -OutFile $binary
    $stream = [IO.File]::OpenRead($binary)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try {
        $actual = [BitConverter]::ToString($hasher.ComputeHash($stream)).Replace('-', '')
    } finally {
        $stream.Dispose()
        $hasher.Dispose()
    }
    if ($actual -ne ($entries[0] -split '\s+')[0]) { throw "Checksum verification failed for $artifact" }
    # Append '.' to avoid PowerShell 5's quoting bug for trailing backslashes.
    $arguments = @('__install', (Join-Path $InstallDir '.'))
    if (-not [string]::IsNullOrWhiteSpace($AgentSkillsDir)) { $arguments += Join-Path $AgentSkillsDir '.' }
    & $binary @arguments
    if ($LASTEXITCODE -ne 0) { throw 'Telos installation failed; run the installer again to finish.' }
    if (($env:PATH -split ';') -notcontains $InstallDir) {
        Write-Output "Add $InstallDir to your user PATH, then open a new terminal."
    }
} finally {
    Remove-Item -LiteralPath $stage -Recurse -Force
}
