param(
    [string]$Version = "0.1.0"
)

# 任一镜像构建失败时立即停止，避免发布一套版本不一致的专项镜像。
$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot

function Invoke-DockerBuild {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name,
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    Write-Host "=== Building $Name ==="
    & docker build @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Name build failed with exit code $LASTEXITCODE"
    }
}

# 基础镜像包含 Pi、通用工具、公共规则和完整跨方向资料库。
Invoke-DockerBuild -Name "ctf-agent-pi-base:$Version" -Arguments @(
    "--file", (Join-Path $PSScriptRoot "base/Dockerfile"),
    "--tag", "ctf-agent-pi-base:$Version",
    $ProjectRoot
)

# 六个专项镜像共享同一版本标签，依次安装各方向工具与入口 Skill。
# BASE_VERSION 显式传入，避免 Dockerfile 使用旧的硬编码基础镜像版本。
$Profiles = @("web", "crypto", "pwn", "reverse", "forensics", "misc")
foreach ($Profile in $Profiles) {
    Invoke-DockerBuild -Name "ctf-agent-pi-${Profile}:$Version" -Arguments @(
        "--build-arg", "BASE_VERSION=$Version",
        "--file", (Join-Path $PSScriptRoot "$Profile/Dockerfile"),
        "--tag", "ctf-agent-pi-${Profile}:$Version",
        $ProjectRoot
    )
}

Write-Host "All CTF Agent Pi images built successfully for version $Version."
