# CTF Agent 资料库

本目录中的资料会复制到镜像 `/opt/cpi/ctf-skills`，作为 Agent 的只读参考。它不等于授权：命令是否可以运行由 daemon 创建沙箱时的策略决定。

- `common/`：跨方向的文件、网络和证据处理参考
- `web/`、`crypto/`、`pwn/`、`reverse/`、`forensics/`、`misc/`：方向专用流程

资料应优先给出可复现命令、输入限制、失败判据和安全注意事项；不要把未经授权的公网目标或真实凭据写进示例。
