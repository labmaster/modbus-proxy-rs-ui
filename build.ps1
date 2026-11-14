# Builds the release archives for all supported architectures (Windows PowerShell).
#
#   powershell -ExecutionPolicy Bypass -File build.ps1          version from main.go
#   powershell -ExecutionPolicy Bypass -File build.ps1 1.2.0    explicit version
#
# Result in dist\ – same as build.sh. Needs Go 1.24+ (https://go.dev/dl/) only.
# The work is done by tools/release (pure Go), so the archives are identical
# to the ones built on Linux (owner root, correct file permissions).
param([string]$Version = "")
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw "Go not found (https://go.dev/dl/)" }
$env:GOPROXY = "off"
if ($Version) { go run ./tools/release $Version } else { go run ./tools/release }
exit $LASTEXITCODE
