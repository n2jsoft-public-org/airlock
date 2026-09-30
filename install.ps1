<#
.SYNOPSIS
    Builds airlock from this checkout and installs it, without make.

.DESCRIPTION
    Windows counterpart of `make install` and `make uninstall`. Builds ./cmd/airlock into GOBIN
    (or the bin folder of the first GOPATH entry), then adds that folder to the user PATH when it
    is missing, so `airlock` works in every new terminal.

.PARAMETER Uninstall
    Removes the installed binary. The PATH is left untouched, since other Go tools live there.

.PARAMETER SkipPath
    Installs without adding the install folder to the user PATH.

.EXAMPLE
    .\install.ps1

.EXAMPLE
    .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [switch]$Uninstall,
    [switch]$SkipPath
)

$ErrorActionPreference = 'Stop'

$onWindows = $env:OS -eq 'Windows_NT'
$binary = if ($onWindows) { 'airlock.exe' } else { 'airlock' }
$pathSeparator = [System.IO.Path]::PathSeparator

function Get-InstallDir {
    $gobin = (go env GOBIN).Trim()
    if ($gobin) {
        return $gobin
    }
    $gopath = ((go env GOPATH).Trim() -split [regex]::Escape($pathSeparator))[0]
    return Join-Path $gopath 'bin'
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
    if (Test-PathContains $env:Path $dir) {
        return
    }
    if (-not $onWindows) {
        Write-Warning "$dir is not on your PATH. Add it to your shell profile to run airlock."
        return
    }
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not (Test-PathContains $userPath $dir)) {
        $newPath = if ($userPath) { "$($userPath.TrimEnd($pathSeparator))$pathSeparator$dir" } else { $dir }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Host "Added $dir to your user PATH. Open a new terminal to use airlock everywhere."
    }
    $env:Path = "$env:Path$pathSeparator$dir"
}

function Test-Prerequisite([string]$command, [string]$hint) {
    if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
        Write-Warning "$command was not found. $hint"
    }
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Go was not found. Install it with `winget install GoLang.Go` or from https://go.dev/dl/, then open a new terminal.'
}

$installDir = Get-InstallDir
$target = Join-Path $installDir $binary

if ($Uninstall) {
    if (Test-Path $target) {
        Remove-Item $target
        Write-Host "Removed $target"
    } else {
        Write-Host "Nothing to remove: $target does not exist"
    }
    return
}

Push-Location $PSScriptRoot
try {
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    go build -o $target ./cmd/airlock
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}
Write-Host "Installed $binary to $target"

if (-not $SkipPath) {
    Add-ToUserPath $installDir
}

Test-Prerequisite 'aws' 'Install AWS CLI v2: winget install Amazon.AWSCLI'
Test-Prerequisite 'session-manager-plugin' 'Install the Session Manager plugin: https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-windows.html'
