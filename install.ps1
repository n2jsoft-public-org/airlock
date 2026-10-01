<#
.SYNOPSIS
    Installs the latest airlock release on Windows, without Go.

.DESCRIPTION
    Downloads the airlock release for this computer, checks it against the release's
    checksums.txt, and installs airlock.exe to %LOCALAPPDATA%\Programs\airlock. That folder is
    yours, so `airlock update` works later without an administrator terminal. Then adds the folder
    to the user PATH when it is missing, so `airlock` works in every new terminal.

    The repository is private: the release is read with GH_TOKEN, GITHUB_TOKEN or the token of the
    GitHub CLI (gh auth login).

.PARAMETER InstallDir
    Install folder. Defaults to AIRLOCK_INSTALL_DIR, else %LOCALAPPDATA%\Programs\airlock.

.PARAMETER Version
    Release to install, such as 20261001-758f7ea. Defaults to AIRLOCK_VERSION, else the latest.

.PARAMETER SkipPath
    Installs without adding the install folder to the user PATH.

.PARAMETER Uninstall
    Removes airlock.exe and the PATH entry added for it, through `airlock uninstall`. When that
    empties the folder, the folder goes too.

.PARAMETER Purge
    With -Uninstall, also removes the settings, history, logs and saved passwords.

.EXAMPLE
    .\install.ps1

.EXAMPLE
    & ([scriptblock]::Create((gh api repos/n2jsoft/airlock/contents/install.ps1 -H 'Accept: application/vnd.github.raw' | Out-String)))

.EXAMPLE
    .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string]$InstallDir,
    [string]$Version,
    [switch]$SkipPath,
    [switch]$Uninstall,
    [switch]$Purge
)

# This script also runs as a script block, inside the caller's session: it never calls exit.
$ErrorActionPreference = 'Stop'
# Windows PowerShell draws a progress bar that slows downloads down tenfold.
$ProgressPreference = 'SilentlyContinue'

$repo = 'n2jsoft/airlock'
$api = "https://api.github.com/repos/$repo"
$binary = 'airlock.exe'
$pathSeparator = [System.IO.Path]::PathSeparator

function Get-GitHubToken {
    foreach ($name in 'GH_TOKEN', 'GITHUB_TOKEN') {
        $value = [Environment]::GetEnvironmentVariable($name)
        if ($value) {
            return $value.Trim()
        }
    }
    if (Get-Command gh -ErrorAction SilentlyContinue) {
        # Windows PowerShell turns any stderr line of a native command into an error under 'Stop'.
        $ErrorActionPreference = 'Continue'
        $value = gh auth token --hostname github.com 2>$null
        if ($LASTEXITCODE -eq 0 -and $value) {
            return ($value | Out-String).Trim()
        }
    }
    return ''
}

function Get-Architecture {
    # A 32-bit PowerShell on a 64-bit Windows reports the real processor in PROCESSOR_ARCHITEW6432.
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) {
        $arch = $env:PROCESSOR_ARCHITECTURE
    }
    switch ($arch) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw "Unsupported processor $arch." }
    }
}

function Get-AccessHint {
    if ($token) {
        return "check that your GitHub account or token can read $repo"
    }
    return 'no GitHub token found: run gh auth login, or set GH_TOKEN'
}

function Get-StatusCode($errorRecord) {
    $response = $errorRecord.Exception.Response
    if ($response) {
        return [int]$response.StatusCode
    }
    return 0
}

# Invoke-GitHub downloads uri to outFile. Both PowerShell editions drop the Authorization header
# when GitHub redirects a download to its storage host.
function Invoke-GitHub([string]$uri, [string]$accept, [string]$outFile) {
    $headers = @{ 'Accept' = $accept; 'X-GitHub-Api-Version' = '2022-11-28' }
    if ($token) {
        $headers['Authorization'] = "Bearer $token"
    }
    Invoke-WebRequest -Uri $uri -Headers $headers -OutFile $outFile -UseBasicParsing
}

function Get-Release {
    if ($Version) {
        $uri = "$api/releases/tags/$Version"
    } else {
        $uri = "$api/releases/latest"
    }
    $file = Join-Path $tmp 'release.json'
    try {
        Invoke-GitHub $uri 'application/vnd.github+json' $file
    } catch {
        $status = Get-StatusCode $_
        if ($status -eq 401 -or $status -eq 403) {
            throw "GitHub refused access to ${repo}: $(Get-AccessHint)."
        }
        if ($status -eq 404 -and $Version) {
            throw "No release $Version in $repo, or no access: $(Get-AccessHint)."
        }
        if ($status -eq 404) {
            throw "No release found in $repo, or no access: $(Get-AccessHint)."
        }
        throw "Cannot read the release from GitHub: $($_.Exception.Message)"
    }
    return Get-Content -Raw $file | ConvertFrom-Json
}

function Save-Asset($release, [string]$name) {
    $asset = $release.assets | Where-Object { $_.name -eq $name } | Select-Object -First 1
    if (-not $asset) {
        throw "Release $($release.tag_name) has no $name."
    }
    $file = Join-Path $tmp $name
    try {
        Invoke-GitHub $asset.url 'application/octet-stream' $file
    } catch {
        throw "Cannot download ${name}: $($_.Exception.Message)"
    }
    return $file
}

function Assert-Checksum([string]$archive, [string]$checksums, [string]$name) {
    $expected = Get-Content $checksums | ForEach-Object {
        $fields = -split $_
        if ($fields.Count -eq 2 -and $fields[1] -eq $name) {
            $fields[0]
        }
    } | Select-Object -First 1
    if (-not $expected) {
        throw "checksums.txt does not list $name."
    }
    $actual = (Get-FileHash -Algorithm SHA256 $archive).Hash
    if ($actual -ine $expected) {
        throw "The download of $name does not match checksums.txt: nothing was changed."
    }
}

# Install-Binary puts newExe in place of target. Windows allows renaming, not overwriting, a running
# executable: the current one moves to airlock.exe.old, which airlock removes at its next launch.
function Install-Binary([string]$newExe, [string]$target) {
    $staged = "$target.new"
    Copy-Item -Force $newExe $staged
    if (-not (Test-Path $target)) {
        Move-Item $staged $target
        return
    }
    $old = "$target.old"
    Remove-Item -Force $old -ErrorAction SilentlyContinue
    if (Test-Path $old) {
        # An earlier .old still runs.
        $old = "$target.old-" + [guid]::NewGuid().ToString('N').Substring(0, 8)
    }
    Move-Item $target $old
    try {
        Move-Item $staged $target
    } catch {
        Move-Item $old $target
        throw
    }
    Remove-Item -Force $old -ErrorAction SilentlyContinue
}

function Test-PathContains([string]$pathList, [string]$dir) {
    $wanted = $dir.TrimEnd('\', '/')
    foreach ($entry in ($pathList -split [regex]::Escape($pathSeparator))) {
        if ($entry -and $entry.TrimEnd('\', '/') -ieq $wanted) {
            return $true
        }
    }
    return $false
}

function Add-ToUserPath([string]$dir) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Test-PathContains $userPath $dir)) {
        $newPath = if ($userPath) { "$($userPath.TrimEnd($pathSeparator))$pathSeparator$dir" } else { $dir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "Added $dir to your user PATH. Open a new terminal to use airlock everywhere."
    }
    if (-not (Test-PathContains $env:Path $dir)) {
        $env:Path = "$env:Path$pathSeparator$dir"
    }
}

function Remove-FromUserPath([string]$dir) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Test-PathContains $userPath $dir)) {
        return
    }
    $wanted = $dir.TrimEnd('\', '/')
    $kept = $userPath -split [regex]::Escape($pathSeparator) |
        Where-Object { $_ -and $_.TrimEnd('\', '/') -ine $wanted }
    [Environment]::SetEnvironmentVariable('Path', ($kept -join $pathSeparator), 'User')
    Write-Host "Removed $dir from your user PATH."
}

# Write-OtherCopies points at airlock.exe binaries left elsewhere on the PATH, such as an older
# install.ps1 or go install into GOPATH\bin, which may run instead of this one.
function Write-OtherCopies([string]$dir) {
    $wanted = $dir.TrimEnd('\', '/')
    foreach ($entry in ($env:Path -split [regex]::Escape($pathSeparator))) {
        if (-not $entry -or $entry.TrimEnd('\', '/') -ieq $wanted) {
            continue
        }
        $other = Join-Path $entry $binary
        if (Test-Path -PathType Leaf $other) {
            Write-Warning "Another airlock is in $other and may run instead of this one: remove it with Remove-Item '$other'"
        }
    }
}

function Test-Prerequisite([string]$command, [string]$hint) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        Write-Warning "$command was not found. $hint"
    }
}

# Test-AirlockUninstall tells whether the airlock at target has the uninstall command.
function Test-AirlockUninstall([string]$target) {
    if (-not (Test-Path $target)) {
        return $false
    }
    # Windows PowerShell turns any stderr line of a native command into an error under 'Stop'.
    $ErrorActionPreference = 'Continue'
    $help = & $target uninstall --help 2>$null | Out-String
    return $help -match '--purge'
}

# Invoke-AirlockUninstall runs airlock uninstall, then waits for the binary to go, and its folder
# when nothing else is in it: Windows deletes them once airlock has exited.
function Invoke-AirlockUninstall([string]$target) {
    $arguments = @('uninstall', '--yes')
    if ($Purge) {
        $arguments += '--purge'
    }
    & $target @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "airlock uninstall failed with exit code $LASTEXITCODE"
    }
    $dir = Split-Path $target
    for ($i = 0; $i -lt 40; $i++) {
        $emptyDir = (Test-Path $dir) -and -not (Get-ChildItem -Force $dir)
        if (-not (Test-Path $target) -and -not $emptyDir) {
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if (Test-Path $target) {
        Write-Warning "$target is still there: delete it once no airlock runs."
    }
}

if ($env:OS -ne 'Windows_NT') {
    throw 'install.ps1 installs airlock on Windows: on macOS and Linux, use install.sh.'
}

if (-not $InstallDir) {
    $InstallDir = $env:AIRLOCK_INSTALL_DIR
}
if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\airlock'
}
if (-not $Version) {
    $Version = $env:AIRLOCK_VERSION
}
$InstallDir = $InstallDir.TrimEnd('\', '/')
$target = Join-Path $InstallDir $binary

if ($Uninstall) {
    if (Test-AirlockUninstall $target) {
        Invoke-AirlockUninstall $target
        return
    }
    # An airlock too old to uninstall itself, or none at all.
    if (Test-Path $target) {
        Remove-Item -Force $target
        Write-Host "Removed $target"
    } else {
        Write-Host "Nothing to remove: $target does not exist"
    }
    Remove-Item -Force "$target.old*" -ErrorAction SilentlyContinue
    if ((Test-Path $InstallDir) -and -not (Get-ChildItem -Force $InstallDir)) {
        Remove-Item $InstallDir
        Remove-FromUserPath $InstallDir
    }
    if ($Purge) {
        Write-Warning "This airlock cannot purge its data: remove $env:APPDATA\airlock and the airlock entries of the Credential Manager by hand."
    }
    return
}

# Windows PowerShell 5.1 may not offer TLS 1.2 by default, which GitHub requires.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$token = Get-GitHubToken
$arch = Get-Architecture
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("airlock-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    $release = Get-Release
    $assetName = "airlock_$($release.tag_name)_windows_$arch.zip"
    $archive = Save-Asset $release $assetName
    $checksums = Save-Asset $release 'checksums.txt'
    Assert-Checksum $archive $checksums $assetName

    $extracted = Join-Path $tmp 'extracted'
    Expand-Archive -Path $archive -DestinationPath $extracted
    $newExe = Join-Path $extracted $binary
    if (-not (Test-Path $newExe)) {
        throw "$assetName holds no $binary."
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Install-Binary $newExe $target
    Write-Host "Installed airlock $($release.tag_name) to $target"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

if ($SkipPath) {
    if (-not (Test-PathContains $env:Path $InstallDir)) {
        Write-Warning "$InstallDir is not on your PATH: add it to run airlock from any folder."
    }
} else {
    Add-ToUserPath $InstallDir
}

Write-OtherCopies $InstallDir
Test-Prerequisite 'aws' 'Install AWS CLI v2: winget install Amazon.AWSCLI'
Test-Prerequisite 'session-manager-plugin' 'Install the Session Manager plugin: https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-windows.html'
& $target version
