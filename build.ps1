param(
    [string]$Version = "0.1.0"
)

# 全局错误捕获
$ErrorActionPreference = "Stop"

# 路径定义
$PSScriptRoot = $PSScriptRoot.TrimEnd('\')
$ProjectRoot = Split-Path -Parent $PSScriptRoot
$BaseDockerfile = Join-Path $PSScriptRoot "base\Dockerfile"

# 调试输出路径
Write-Host "=== 路径调试信息 ==="
Write-Host "脚本目录: $PSScriptRoot"
Write-Host "构建上下文根目录: $ProjectRoot"
Write-Host "基础镜像Dockerfile: $BaseDockerfile"
Write-Host "镜像版本标签: $Version`n"

# 前置校验基础Dockerfile
if (-not (Test-Path -Path $BaseDockerfile -PathType Leaf)) {
    Write-Error "缺失 base/Dockerfile，请核对目录结构！"
    exit 1
}

# 构建基础镜像（此处标签也建议统一加${}规范写法）
Write-Host "开始构建基础镜像 ctf-agent-pi-base:${Version}"
docker build --file "$BaseDockerfile" --tag "ctf-agent-pi-base:${Version}" "$ProjectRoot"
if ($LASTEXITCODE -ne 0) {
    Write-Error "基础镜像构建失败，终止脚本"
    exit 1
}
Write-Host "基础镜像构建完成`n"

# 批量构建各分类镜像
$Profiles = @("web", "crypto", "pwn", "reverse", "forensics", "misc")
foreach ($Profile in $Profiles) {
    $dfPath = Join-Path $PSScriptRoot "$Profile\Dockerfile"
    if (-not (Test-Path $dfPath -PathType Leaf)) {
        Write-Warning "跳过 $Profile ：未找到Dockerfile $dfPath"
        continue
    }

    Write-Host "----------------------------------------"
    Write-Host "正在构建 ctf-agent-pi-${Profile}:${Version}"
    docker build --file "$dfPath" --tag "ctf-agent-pi-${Profile}:${Version}" "$ProjectRoot"
    
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "$Profile 镜像构建出错，继续构建其他镜像"
    }
    else {
        Write-Host "$Profile 镜像构建成功"
    }
    Write-Host "----------------------------------------`n"
}

Write-Host "所有镜像构建流程执行完毕"