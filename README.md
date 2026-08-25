# ctftools
CTF-BTFly 将大模型 Agent、各种工具、Docker 环境和桌面管理界面整合到同一个工具，为每道 CTF 题目提供独立、可观察、可恢复、可人工接管的智能分析环境。


一、统一管理六大类 CTF 题目
CTF-BTFly 支持常见的六大 CTF 题型：

Web

Crypto

Pwn

Reverse

Forensics

Misc

可以直接创建题目，也可以将文件或整个文件夹拖入对应的题型区域。系统会自动保留附件目录结构，并根据题型选择相应的docker环境。

每道题目都有独立的状态、附件、终端、分析记录、模型用量、Flag 候选和解题报告，方便统一管理比赛中的多项任务。

二、一题一沙箱，隔离运行更安心
六个镜像：
每道题目都会运行在独立的 Docker 沙箱中，并拥有自己的工作目录、Agent 会话和分析产物。

系统根据题型加载不同的专项工具环境，例如：
Web：Nmap、SQLMap、Gobuster、WhatWeb

Crypto：PyCryptodome、SymPy、Z3、John

Pwn：GDB、Pwntools、Ropper、Checksec

Reverse：Apktool、angr、Strace、Ltrace

Forensics：Binwalk、Tshark、Yara、Volatility

Misc：FFmpeg、ImageMagick、Steghide、ZBar

沙箱只挂载当前题目的工作区，不直接挂载 Docker Socket，并对 CPU、内存、进程数和系统权限进行限制，降低不同题目之间相互影响的风险。


AI Agent 自主分析并执行工具

任务启动后，AI Agent 可以在授权范围内：

阅读题目描述和附件；

检查文件类型与目录结构；

执行命令和专项分析工具；

编写、运行并修正解题脚本；

保存响应数据、脚本和分析证据；

根据执行结果调整解题路线；

识别并提交可能的 Flag；

生成完整的中文解题报告。

所有重要操作都会显示在桌面端时间线中。用户可以实时查看 Agent 正在进行的分析、调用过的工具、产生的结果以及当前任务状态。


四、标准 Agent与黑板多模型协作
黑板多模型：
对于常规题目，可以使用“标准 Agent”模式，让一个主要模型负责完整解题过程。

面对复杂题目，还可以启用“黑板多模型”模式：由一个调度模型负责拆解问题、分配工作、检查冲突和汇总结果，多个 Worker 模型从不同方向并行分析。

调度模型与 Worker 共享同一块持久化“黑板”，在其中记录：

已确认的事实；

待验证的猜想；

已失败的分析路线；

关键证据；

当前工作项；

Flag 候选；

下一步行动建议。

这让不同模型不再是相互隔离的聊天窗口，而能够围绕同一道题共享进度、交叉验证并持续推进。

五、模型可以切换，解题上下文不会丢失
CTF-BTFly 支持配置多个在线模型和本地模型。

在线模型可以连接兼容接口的远程服务；本地模式则可以对接 LM Studio、Ollama 运行在用户电脑上的模型服务。

对当前模型效果不理想时，用户可以暂停任务并切换模型。新模型会接管原有的 Agent 会话，继续使用此前的消息、工具结果、分析脚本、附件和已有产物，无需重新创建容器或从头开始。

模型配置支持新增、编辑、连接检测和热更新，通常无需重启程序。


六、自动发现 Flag 候选，交由人工确认
系统会综合题目填写的 Flag 格式、Agent 实时输出、工具执行结果以及最终 Writeup，主动识别可能的 Flag。

每个候选都会保留来源、置信度和格式匹配情况。发现候选后，系统可以冻结当前解题流程，等待用户确认，避免 Agent 在已经获得正确答案后继续消耗模型资源。

最终结果始终由用户确认，兼顾自动化效率与人工判断。

七、自动沉淀附件、脚本与中文 Writeup
Agent 在解题过程中产生的脚本、响应文件和分析证据会统一保存在当前题目的工作区中。用户可以直接在桌面端浏览、复制或下载这些文件。
任务完成后，系统会生成中文 WRITEUP.md，记录：

题目基本信息；

分析思路；

使用过的工具与命令；

关键脚本；

重要证据；

Flag 获取过程；

最终结果。

相比只有一句答案的普通模型对话，CTF-BTFly 更强调解题过程的可复现性。

八、实时状态、历史恢复与资源监控
桌面端可以集中查看任务状态、Agent 活动、模型用量以及宿主机 CPU、内存和 Docker 资源情况。
重要任务事件会持久化保存。即使桌面界面暂时断开，也可以在重新连接后恢复稳定的历史记录。后台控制服务还能够独立管理正在运行的任务，减少因界面退出而导致任务中断的情况。

系统也会根据当前机器的 CPU 和内存情况，给出并行题目数量及黑板 Worker 数

量建议。


九、连接更多专业分析工具
MCP:
CTF-BTFly 支持通过 MCP Gateway 接入 IDA、JADX、Burp 等外部分析工具。

管理员可以为工具设置适用题型、启用状态和权限范围。任务启动时，系统会冻结该任务可以访问的工具列表，并记录相关调用，减少运行过程中权限被意外扩大的风险。
让 AI 真正进入 CTF 解题流程
CTF-BTFly 不只是一个模型聊天界面，也不只是一个 Docker 启动器。

它将题目管理、模型调度、沙箱执行、专业工具、Flag 确认、文件沉淀和 Writeup 生成连接成一套完整流程，让 AI 的每一步分析都能够被观察、验证、暂停、恢复和人工接管。

CTF-BTFly 将桌面工作台、Go 控制平面、Docker 沙箱、Pi Agent 与模型网关组合在一起，让每道题都可追踪、可复现、可人工接管。
一套界面，覆盖完整解题闭环
1.题目与沙箱
导入文件或目录，一题一工作区、一题一 Docker 沙箱；支持暂停、恢复、重试与清理。
2.Agent 协作
简单题使用标准 Agent，复杂题使用调度模型与多个 Worker 共享黑板证据。
3.实时过程
流式展示分析、工具调用、终端输出和状态事件，大量记录按节点增量渲染。
4.Flag 与 Writeup
自动收集候选、人工确认、复制最终 Flag，并生成可复现的中文解题报告。
5.模型与用量
在线/本地模型可混用，支持参数与强度配置，按任务统计请求和 Token。
6.MCP 与插件
通过同目录 JSON 管理 MCP 和宿主工具；前端可刷新配置并按题型限制权限。

从附件到报告，五步完成
控制权始终在本机。必要时暂停任务、补充提示或切换模型，再从原上下文继续
1.创建题目
选择题型、模型与解题架构，导入附件。
2.启动沙箱
daemon 建立独立工作区并启动 Pi Agent。
3.观察与接管
查看事件、终端、文件和黑板，随时暂停。
4.审核结果
检查证据并人工确认 Flag 候选。
5.归档复现
下载 Writeup 与 Artifact，关闭容器释放资源。

系统概况
查看 Docker、镜像、模型和隔离运行状态。
<img width="1616" height="973" alt="image" src="https://github.com/user-attachments/assets/4b247d49-fc86-4d26-b70c-75d80a8cf8ef" />
任务控制台
统一查看队列、Agent、Flag 审核与资源状态。
<img width="2582" height="1550" alt="image" src="https://github.com/user-attachments/assets/595c7bcd-c5b2-486e-8afd-f2811d50e943" />
创建题目
配置题型、附件、运行模式与模型。
<img width="2582" height="1550" alt="image" src="https://github.com/user-attachments/assets/35f3242d-5f82-4722-b3cb-959bd1af2fe3" />
题目工作区
浏览解题过程、事件节点、文件、模型状态与 Flag 候选。
<img width="1586" height="992" alt="image" src="https://github.com/user-attachments/assets/f3332657-c85d-404d-816a-93da10407604" />
黑板多模型
查看调度 Agent、Worker、工作项和共享知识。
<img width="1586" height="992" alt="image" src="https://github.com/user-attachments/assets/79113642-ba37-4b84-b5ba-4bcf28d8b9d1" />
Flag 审核
集中检查候选并由操作员确认最终结果。
<img width="2582" height="1550" alt="image" src="https://github.com/user-attachments/assets/99d025f3-1c16-45b1-8218-69d6081a3fab" />
模型用量
按日期和题目统计请求与 Token。
<img width="2582" height="1550" alt="image" src="https://github.com/user-attachments/assets/1ccb09b3-77c5-440d-9f85-c254f4565879" />
MCP 工具
配置 IDA、JADX、Burp 等服务并刷新 JSON。
<img width="1616" height="973" alt="image" src="https://github.com/user-attachments/assets/39ef5122-cd84-45e2-bbd8-9de1ac6ef272" />
本机工具插件
以受控权限向 Agent 提供 EXE、CMD 或脚本。
<img width="1616" height="973" alt="image" src="https://github.com/user-attachments/assets/99a65dcf-c060-4e04-930a-b00a71f688d7" />


本地优先的工具链:Wails + React,Go daemon,Docker,Pi RPC,SQLite,MCP Gateway





