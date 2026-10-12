[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Payload,

  [Parameter(Mandatory = $true)]
  [string]$Certificate,

  [string]$OutputDirectory = "release-output/windows-x64",

  [string]$TimestampUrl = "http://timestamp.digicert.com"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Resolve-RepositoryRoot {
  $root = Resolve-Path (Join-Path $PSScriptRoot "../..")
  return $root.Path
}

function Assert-ExitCode([string]$Description) {
  if ($LASTEXITCODE -ne 0) {
    throw "$Description failed with exit code $LASTEXITCODE"
  }
}

function Test-PayloadChecksums([string]$Root) {
  $checksumFile = Join-Path $Root "SHA256SUMS.txt"
  if (-not (Test-Path $checksumFile -PathType Leaf)) {
    throw "The payload does not contain SHA256SUMS.txt"
  }
  foreach ($line in Get-Content $checksumFile) {
    if ($line -notmatch '^([0-9a-fA-F]{64})\s{2}(.+)$') {
      throw "Invalid checksum line: $line"
    }
    $expected = $Matches[1].ToUpperInvariant()
    $relative = $Matches[2].Replace('/', [IO.Path]::DirectorySeparatorChar)
    $path = Join-Path $Root $relative
    if (-not (Test-Path $path -PathType Leaf)) {
      throw "Payload file is missing: $relative"
    }
    $actual = (Get-FileHash $path -Algorithm SHA256).Hash
    if ($actual -ne $expected) {
      throw "Payload SHA-256 mismatch: $relative"
    }
  }
}

$repository = Resolve-RepositoryRoot
$payloadPath = (Resolve-Path $Payload).Path
$certificatePath = (Resolve-Path $Certificate).Path
$outputPath = if ([IO.Path]::IsPathRooted($OutputDirectory)) {
  $OutputDirectory
} else {
  Join-Path $repository $OutputDirectory
}
$temporary = Join-Path ([IO.Path]::GetTempPath()) ("shotforward-windows-signing-" + [Guid]::NewGuid().ToString("N"))
$extract = Join-Path $temporary "payload"
$signingConfig = Join-Path $temporary "tauri.windows-signing.conf.json"
$importedThumbprints = @()
$existingThumbprints = @(Get-ChildItem Cert:\CurrentUser\My | ForEach-Object Thumbprint)

try {
  New-Item -ItemType Directory -Force -Path $extract | Out-Null
  Expand-Archive -Path $payloadPath -DestinationPath $extract
  Test-PayloadChecksums $extract

  $manifestPath = Join-Path $extract "payload-manifest.json"
  if (-not (Test-Path $manifestPath -PathType Leaf)) {
    throw "The payload does not contain payload-manifest.json"
  }
  $manifest = Get-Content $manifestPath -Raw | ConvertFrom-Json
  if ($manifest.schema_version -ne 1 -or $manifest.platform -ne "windows-x64" -or $manifest.target -ne "x86_64-pc-windows-msvc") {
    throw "The payload is not a supported ShotForward Windows x64 payload"
  }

  Push-Location $repository
  try {
    $currentCommit = (git rev-parse HEAD).Trim()
    Assert-ExitCode "git rev-parse"
  } finally {
    Pop-Location
  }
  if ($currentCommit -ne $manifest.commit) {
    throw "Payload commit $($manifest.commit) does not match the checked-out commit $currentCommit"
  }

  $target = [string]$manifest.target
  $releaseDirectory = Join-Path $repository "desktop/src-tauri/target/$target/release"
  $binaryDirectory = Join-Path $repository "desktop/src-tauri/binaries"
  New-Item -ItemType Directory -Force -Path $releaseDirectory, $binaryDirectory | Out-Null
  Copy-Item (Join-Path $extract "desktop/src-tauri/target/$target/release/shotforward-desktop.exe") $releaseDirectory -Force
  Get-ChildItem (Join-Path $extract "desktop/src-tauri/binaries/*-$target.exe") | Copy-Item -Destination $binaryDirectory -Force

  if (-not (Test-Path (Join-Path $repository "desktop/node_modules/.bin/tauri.cmd"))) {
    Push-Location (Join-Path $repository "desktop")
    try {
      npm ci
      Assert-ExitCode "npm ci"
    } finally {
      Pop-Location
    }
  }

  $securePassword = if ([string]::IsNullOrEmpty($env:WINDOWS_CERTIFICATE_PASSWORD)) {
    Read-Host "PFX password" -AsSecureString
  } else {
    ConvertTo-SecureString $env:WINDOWS_CERTIFICATE_PASSWORD -AsPlainText -Force
  }
  $imported = @(Import-PfxCertificate -FilePath $certificatePath -CertStoreLocation Cert:\CurrentUser\My -Password $securePassword)
  $leaf = $imported | Where-Object HasPrivateKey | Select-Object -First 1
  if ($null -eq $leaf) {
    throw "The PFX did not contain a code-signing certificate with a private key"
  }
  $importedThumbprints = @($imported | Where-Object { $_.Thumbprint -notin $existingThumbprints } | ForEach-Object Thumbprint)

  $signTool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" |
    Sort-Object FullName -Descending |
    Select-Object -First 1
  if ($null -eq $signTool) {
    throw "signtool.exe was not found; install the Windows SDK"
  }

  $sidecars = @(
    "shotforwardd-$target.exe",
    "shotforward-codex-runtime-$target.exe",
    "ffmpeg-$target.exe",
    "ffprobe-$target.exe"
  ) | ForEach-Object { Join-Path $binaryDirectory $_ }
  foreach ($file in $sidecars) {
    if (-not (Test-Path $file -PathType Leaf)) { throw "Missing sidecar: $file" }
    & $signTool.FullName sign /sha1 $leaf.Thumbprint /fd SHA256 /tr $TimestampUrl /td SHA256 $file
    Assert-ExitCode "Authenticode signing for $file"
    & $signTool.FullName verify /pa /all $file
    Assert-ExitCode "Authenticode verification for $file"
  }

  $windowsSigning = [ordered]@{
    version = [string]$manifest.version
    bundle = [ordered]@{
      windows = [ordered]@{
        certificateThumbprint = $leaf.Thumbprint
        digestAlgorithm = "sha256"
        timestampUrl = $TimestampUrl
      }
    }
  }
  $windowsSigning | ConvertTo-Json -Depth 5 | Set-Content -Path $signingConfig -Encoding utf8

  Push-Location (Join-Path $repository "desktop")
  try {
    npm run tauri -- bundle --ci --target $target --bundles nsis,msi --config $signingConfig
    Assert-ExitCode "Tauri Windows bundling"
  } finally {
    Pop-Location
  }

  $mainExecutable = Join-Path $releaseDirectory "shotforward-desktop.exe"
  & $signTool.FullName verify /pa /all $mainExecutable
  Assert-ExitCode "Authenticode verification for the desktop executable"

  $bundleDirectory = Join-Path $releaseDirectory "bundle"
  $installers = @(Get-ChildItem $bundleDirectory -Recurse -File | Where-Object { $_.Extension -in ".exe", ".msi" })
  if ($installers.Count -lt 2) {
    throw "Expected NSIS and MSI installers under $bundleDirectory"
  }
  foreach ($installer in $installers) {
    & $signTool.FullName verify /pa /all $installer.FullName
    Assert-ExitCode "Authenticode verification for $($installer.FullName)"
  }

  New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
  $published = @()
  foreach ($installer in $installers) {
    $destination = Join-Path $outputPath $installer.Name
    Copy-Item $installer.FullName $destination -Force
    $published += Get-Item $destination
  }
  $checksumPath = Join-Path $outputPath "SHA256SUMS-windows-x64.txt"
  $published | ForEach-Object {
    "$((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant())  $($_.Name)"
  } | Set-Content -Path $checksumPath -Encoding ascii

  Write-Host "Signed Windows release written to $outputPath"
} finally {
  foreach ($thumbprint in $importedThumbprints) {
    Get-Item "Cert:\CurrentUser\My\$thumbprint" -ErrorAction SilentlyContinue | Remove-Item -Force
  }
  Remove-Item $temporary -Recurse -Force -ErrorAction SilentlyContinue
}
