param([switch]$Uninstall)

$ErrorActionPreference = 'Stop'
$temporaryDirectory = $null
$pendingBinary = $null

try {
    $defaultDirectory = [string]::IsNullOrWhiteSpace($env:IG_MCP_BIN_DIR)
    $binDirectory = if ($defaultDirectory) { Join-Path $HOME '.local\bin' } else { $env:IG_MCP_BIN_DIR.Trim() }
    $binDirectory = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($binDirectory))
    $binary = Join-Path $binDirectory 'ig-mcp.exe'

    if ($Uninstall) {
        if (Test-Path -LiteralPath $binary -PathType Leaf) {
            Remove-Item -LiteralPath $binary -Force
            Write-Host "ig-mcp: removed $binary"
        } else {
            Write-Host "ig-mcp: not installed at $binary"
        }
        return
    }

    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    if ([string]::IsNullOrWhiteSpace($env:IG_MCP_VERSION)) {
        $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/mewisme/ig-mcp/releases/latest' -Headers @{ 'User-Agent' = 'ig-mcp-installer' } -UseBasicParsing
        $tag = [string]$release.tag_name
    } else {
        $tag = $env:IG_MCP_VERSION.Trim()
        if ($tag -notmatch '^v') { $tag = "v$tag" }
    }
    if ($tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$') {
        throw "Invalid release version: $tag"
    }
    $version = $tag -replace '^v', ''

    $architecture = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
        'X64' { 'amd64'; break }
        'Arm64' { 'arm64'; break }
        default { throw 'Unsupported Windows architecture (expected x64 or arm64)' }
    }
    $asset = "ig-mcp_${version}_windows_${architecture}.zip"
    $releaseUrl = "https://github.com/mewisme/ig-mcp/releases/download/$tag"
    $temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ("ig-mcp-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $temporaryDirectory -Force | Out-Null
    $archivePath = Join-Path $temporaryDirectory $asset
    $checksumPath = Join-Path $temporaryDirectory 'checksums.txt'

    Write-Host "ig-mcp: downloading $tag ($architecture)"
    Invoke-WebRequest -Uri "$releaseUrl/$asset" -OutFile $archivePath -UseBasicParsing
    Invoke-WebRequest -Uri "$releaseUrl/checksums.txt" -OutFile $checksumPath -UseBasicParsing

    $checksumPattern = '(?m)^([a-fA-F0-9]{64})[ \t]+\*?' + [regex]::Escape($asset) + '\r?$'
    $checksumMatch = [regex]::Match([IO.File]::ReadAllText($checksumPath), $checksumPattern)
    if (-not $checksumMatch.Success) { throw "Checksum not found for $asset" }
    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
    if ($actualHash -ne $checksumMatch.Groups[1].Value) { throw "SHA256 mismatch for $asset" }

    New-Item -ItemType Directory -Path $binDirectory -Force | Out-Null
    $pendingBinary = Join-Path $binDirectory ("ig-mcp.exe." + [Guid]::NewGuid().ToString('N') + '.tmp')
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        $entry = $zip.GetEntry('ig-mcp.exe')
        if ($null -eq $entry -or $entry.Length -eq 0) { throw 'Archive has no ig-mcp.exe entry' }
        $inputStream = $entry.Open()
        try {
            $outputStream = [IO.File]::Create($pendingBinary)
            try { $inputStream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
        } finally { $inputStream.Dispose() }
    } finally { $zip.Dispose() }

    if ([IO.File]::Exists($binary)) {
        [IO.File]::Replace($pendingBinary, $binary, $null)
    } else {
        [IO.File]::Move($pendingBinary, $binary)
    }
    Write-Host "ig-mcp: installed $binary"

    if ($defaultDirectory) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $present = $false
        foreach ($item in (([string]$userPath) -split ';')) {
            $expanded = [Environment]::ExpandEnvironmentVariables($item.Trim().Trim('"'))
            if ($expanded.TrimEnd([char[]]@('\', '/')) -ieq $binDirectory.TrimEnd([char[]]@('\', '/'))) {
                $present = $true
                break
            }
        }
        if (-not $present) {
            $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $binDirectory } else { $userPath.TrimEnd(';') + ';' + $binDirectory }
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            Write-Host 'ig-mcp: added install directory to User PATH (open a new terminal)'
        }
    }
} catch {
    [Console]::Error.WriteLine("ig-mcp: $($_.Exception.Message)")
    exit 1
} finally {
    if ($pendingBinary -and (Test-Path -LiteralPath $pendingBinary)) {
        Remove-Item -LiteralPath $pendingBinary -Force -ErrorAction SilentlyContinue
    }
    if ($temporaryDirectory -and (Test-Path -LiteralPath $temporaryDirectory)) {
        Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force -ErrorAction SilentlyContinue
    }
}