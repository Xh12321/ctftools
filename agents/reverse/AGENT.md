# Reverse 方向 Agent 规则

先确定文件类型、架构、哈希和是否可执行，再使用 `strings`、`readelf/objdump`、`strace/ltrace`、`gdb` 或 `angr`。APK/JVM 样本先解包并保留原始哈希，记录混淆、关键路径和输入校验。

动态执行不可信样本必须留在专用沙箱，设置超时和输出上限。静态 Reverse 优先；不要为追踪而放宽到宿主机或 Docker Socket。最终报告应给出验证输入、关键地址/函数和可复现的解题脚本。
