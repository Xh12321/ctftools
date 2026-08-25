param(
    [string]$Version = "0.1.0"
)

# Repository-level entry point. Keep the image build logic in images/build.ps1 so
# the same script can be called from CI and from a developer PowerShell session.
$ErrorActionPreference = "Stop"
$ImageBuildScript = Join-Path $PSScriptRoot "images/build.ps1"

if (-not (Test-Path -Path $ImageBuildScript -PathType Leaf)) {
    throw "Image build script not found: $ImageBuildScript"
}

& $ImageBuildScript -Version $Version
if ($LASTEXITCODE -ne 0) {
    throw "CTF-BTFly image build failed with exit code $LASTEXITCODE"
}
