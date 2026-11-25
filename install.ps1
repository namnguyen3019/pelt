# Install pelt on Windows from a GitHub release.
#
#   irm https://raw.githubusercontent.com/namnguyen3019/pelt/main/install.ps1 | iex
#
# A specific version, from a saved copy of this script:
#
#   .\install.ps1 -Version v0.1.0
#
# The script picks pelt_windows_<arch>.zip for this CPU (amd64 or arm64),
# checks it against the release's checksums.txt (SHA-256), puts pelt.exe in
# a per-user folder and adds that folder to your user PATH. No admin needed.
#
# Settings (environment variables, used when the matching parameter is not
# given, which is always the case with "irm ... | iex"):
#   PELT_VERSION         release to install, e.g. v0.1.0 (default: latest)
#   PELT_INSTALL_DIR     folder for pelt.exe
#                        (default: %LOCALAPPDATA%\Programs\pelt)
#   PELT_NO_MODIFY_PATH  set to 1 to leave your PATH alone
#   PELT_BASE_URL        download assets from here instead of GitHub, for
#                        mirrors and testing; it must hold the release's files

param(
  [string]$Version = $env:PELT_VERSION,
  [string]$InstallDir = $env:PELT_INSTALL_DIR,
  [switch]$NoModifyPath = ($env:PELT_NO_MODIFY_PATH -eq '1')
)

function Install-Pelt {
  param(
    [string]$Version,
    [string]$InstallDir,
    [bool]$NoModifyPath
  )

  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is very slow with the progress bar on

  $repo = 'namnguyen3019/pelt'

  if (-not $Version) { $Version = 'latest' }
  if ($Version -ne 'latest' -and -not $Version.StartsWith('v')) { $Version = "v$Version" }
  if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\pelt' }

  # Windows PowerShell 5.1 may not offer TLS 1.2 by default.
  if ($PSVersionTable.PSVersion.Major -lt 6) {
    [Net.ServicePointManager]::SecurityProtocol =
      [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  }

  # CPU: prefer the OS architecture, which stays correct when this PowerShell
  # runs as a 32-bit or emulated x64 process.
  $cpu = $null
  try {
    $cpu = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
  } catch {
    $cpu = $null
  }
  if (-not $cpu) {
    $cpu = $env:PROCESSOR_ARCHITEW6432
    if (-not $cpu) { $cpu = $env:PROCESSOR_ARCHITECTURE }
  }
  switch -Regex ($cpu) {
    '^(X64|AMD64)$' { $arch = 'amd64' }
    '^(Arm64|ARM64)$' { $arch = 'arm64' }
    default { throw "unsupported CPU architecture: $cpu (supported: amd64, arm64)" }
  }

  $asset = "pelt_windows_$arch.zip"
  if ($env:PELT_BASE_URL) {
    $base = $env:PELT_BASE_URL.TrimEnd('/')
  } elseif ($Version -eq 'latest') {
    $base = "https://github.com/$repo/releases/latest/download"
  } else {
    $base = "https://github.com/$repo/releases/download/$Version"
  }

  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("pelt-install-" + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    Write-Host "pelt-install: downloading $asset ($Version)"
    $zip = Join-Path $tmp $asset
    $sums = Join-Path $tmp 'checksums.txt'
    try {
      Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile $zip
      Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $sums
    } catch {
      $what = if ($Version -eq 'latest') { 'the latest release' } else { "release $Version" }
      throw "could not download from $base (check that $what exists and has $asset): $($_.Exception.Message)"
    }

    $expected = $null
    foreach ($line in Get-Content $sums) {
      $parts = $line.Trim() -split '\s+'
      if ($parts.Count -eq 2 -and $parts[1].TrimStart('*') -eq $asset) { $expected = $parts[0].ToLower() }
    }
    if (-not $expected) { throw "checksums.txt has no entry for $asset" }
    $actual = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
    if ($expected -ne $actual) {
      throw "checksum mismatch for $asset (expected $expected, got $actual)"
    }
    Write-Host 'pelt-install: checksum OK'

    $unpacked = Join-Path $tmp 'unpacked'
    Expand-Archive -Path $zip -DestinationPath $unpacked
    $exe = Join-Path $unpacked 'pelt.exe'
    if (-not (Test-Path $exe)) { throw "$asset does not contain pelt.exe" }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir 'pelt.exe'
    try {
      Copy-Item -Force -Path $exe -Destination $target
    } catch {
      throw "could not write $target (is pelt running?): $($_.Exception.Message)"
    }
    Write-Host "pelt-install: installed $target"
  } finally {
    Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
  }

  if ($NoModifyPath) { return }

  $sep = [IO.Path]::PathSeparator
  $inSession = ($env:Path -split $sep) -contains $InstallDir
  if (-not $inSession) { $env:Path = "$InstallDir$sep$env:Path" }

  if ($IsLinux -or $IsMacOS) { return } # testing on a non-Windows PowerShell

  # Read the user PATH without expanding %VARIABLES%, so they survive the write.
  $key = Get-Item -Path 'HKCU:\Environment'
  $userPath = $key.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
  $entries = @($userPath -split ';' | Where-Object { $_ })
  if ($entries -notcontains $InstallDir) {
    $newPath = (@($InstallDir) + $entries) -join ';'
    Set-ItemProperty -Path 'HKCU:\Environment' -Name 'Path' -Value $newPath -Type ExpandString
    # Let Explorer and new terminals see the change.
    $dummy = 'PELT_INSTALL_' + [Guid]::NewGuid().ToString('N')
    [Environment]::SetEnvironmentVariable($dummy, '1', 'User')
    [Environment]::SetEnvironmentVariable($dummy, $null, 'User')
    Write-Host "pelt-install: added $InstallDir to your user PATH; open a new terminal to use pelt everywhere"
  }
}

try {
  Install-Pelt -Version $Version -InstallDir $InstallDir -NoModifyPath $NoModifyPath.IsPresent
} catch {
  # Write-Host and no "exit", so a failure inside "irm ... | iex" does not
  # close the user's PowerShell window.
  Write-Host "pelt-install: error: $($_.Exception.Message)" -ForegroundColor Red
}
