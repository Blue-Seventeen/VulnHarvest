# VulnHarvest v0.0.1

VulnHarvest 是基于 LLM 的自主渗透测试控制台。界面、API 和 SQLite 存储内嵌在单个 `artex` 可执行文件中。内部 Go module、`artex` 命令、原有 `ARTEX_*` 环境变量和登录 principal `ARTEX` 保持兼容。项目源码位于 [Blue-Seventeen/VulnHarvest](https://github.com/Blue-Seventeen/VulnHarvest)。

## 下载和首次启动

使用经审核的 `VulnHarvest-v0.0.1-<系统>-<架构>.zip`：Windows amd64、Linux amd64/arm64、macOS amd64/arm64。解压整个目录，Windows 运行 `start.bat`；Linux/macOS 运行 `./start.sh`（如解压工具未保留权限，先 `chmod +x start.sh artex`）。启动脚本按自身位置定位程序并显式传入 `-mode portable`，支持中文、空格路径和从其它工作目录启动。

打开控制台输出的实际 HTTP 地址；便携模式默认请求 `127.0.0.1:8787` 和流量代理 `127.0.0.1:8788`，端口占用时自动选择新的 loopback 端口。系统配置页的「运行环境」卡片显示实际地址。首次进入 `/setup` 设置管理员密码，然后在「LLM 配置」中添加模型。打开 UI 不要求安装 Go、Node.js 或 PostgreSQL；执行依赖外部工具的 Agent 工作需要相应工具。

启动失败时 stderr 与原退出码会保留。脚本不无限重启。建议在终端启动以保留错误输出。可附加 `-no-browser`、`-addr 127.0.0.1:0`、`-proxy 127.0.0.1:0`；`-proxy ""` 关闭流量代理。

## 数据与搬迁

便携目录包含程序、`config.example.json`、`skills/`、`runtime/bin/`、README 和许可。首次运行按程序位置创建 `state/` 和 `data/`：state 保存主 SQLite、登录签名材料和运行信息，data 保存任务工作区、对话、归档及附件。state 位于网页可下载工作区之外。

**先关闭应用并等待进程退出，再复制整个目录**，包括 `state/`、`data/`、`skills/`、`runtime/` 和自定义 `config.json`。不要只复制数据库或在运行时搬迁 WAL 文件。单实例锁会拒绝同一 state 上的第二个进程。

便携重启后，未完成任务和历史对话保持暂停；在对应页面手动恢复。调度、通知、MCP 发现和归档后台处理也先暂停。「运行环境」卡片显示本会话状态并提供显式恢复。恢复通知会处理保留的待投递事件；恢复仅影响当前进程，不改写已保存的启用配置。请先确认需要恢复的后台功能。历史 PostgreSQL 数据导入、同步页面或知识库不属于此版本。

## 可选工具

运行环境卡片提供重新检测，并显示各工具状态、来源、版本、路径、功能影响与失败候选的回退原因。检测优先级是 `runtime/bin` > 有效配置路径 > 进程启动时 PATH。不自动安装系统工具、npm 包或浏览器。

按需求自行提供对应平台/架构的 Python、Node/npm/npx、ripgrep、nmap、curl、shell、Chromium/Playwright 或 MCP 可执行文件；它们不随默认包发行。缺失只影响依赖的脚本、搜索、网络探测、浏览器或 MCP 能力，界面和不依赖这些工具的操作仍可用。Windows 不支持当前 SDK 的 PTY，普通命令与可用工具的非 PTY 执行可以使用。移动目录后重新检测，失效的旧绝对路径会回退到其它有效候选。

Windows 的工具隔离 relay 使用可执行文件副本；启动和每次重检测会占用额外磁盘空间。本会话副本在正常关闭时清理，强退留下的旧 session 目前不会在下次启动时自动删除。请保留充足空间，并在搬迁前优先正常退出；具体开销随内嵌程序大小和重检测次数增长。

## 配置模式和 PostgreSQL

选择模式的优先级：CLI `-mode` > `VULNHARVEST_MODE` > `config.json` 的 `mode` > `postgres`。便携脚本显式选择 portable；直接运行 `artex` 且不设模式时仍使用兼容的 postgres 默认。portable 不读取或连接 PostgreSQL DSN。

需要 PostgreSQL 时先安装并**创建数据库**（应用仅自动建表及执行幂等表结构迁移，不创建数据库），复制 `config.example.json` 为 `config.json` 并将 `mode` 设为 `postgres`、填写 `database`。然后直接运行：

```sh
./artex -mode postgres -addr 127.0.0.1:8787
```

连接优先级仍为 `ARTEX_PG_DSN` > config 的 `database.dsn` > database 分字段。可用 `ARTEX_CONFIG` 显式指定配置文件。示例 `skill_dir` 为空时使用程序旁的 skills；`ARTEX_SKILL_DIR` 或自定义 `skill_dir` 可覆盖。自定义相对 skill_dir 按工作目录解析；可选 tools 相对路径按程序根解析。LLM Key 通过现有环境变量或应用内配置提供，不写入示例、发行包或 CI。

源码中的 Compose 只引用本地 `vulnharvest:0.0.1` 并禁止 pull；需自行构建此镜像。便携使用不要求 Docker。历史 install/update 脚本的上游安装行为已停止。

## 构建与验证

构建机器需要 Go 1.26.3+、Node.js 24 和 `web/node_modules` 开发依赖（先在 web 中执行 `npm ci`）。运行包不需要这些开发依赖。

```sh
./build.sh --release
# Windows PowerShell:
go run ./cmd/portablebuild --release
# 单平台:
go run ./cmd/portablebuild --target windows/amd64
```

构建器每次重建最新生产 UI，强制 `NEXT_PUBLIC_MOCK=0`、同源 SSE，内嵌到二进制。五个 zip 和 SHA256SUMS 在 dist。文件白名单为目标可执行文件、portable 启动脚本、config.example、README、LICENSE、web/proxy 许可、第三方 notices、`portable/skills-manifest.txt` 列出的技能文件，以及空 runtime/bin。不会复制本工作区 state/data、配置 Key、.superpowers 或开发依赖。

跨平台编译只证明生成产物，不等于各平台已运行。GitHub Actions 在 Windows x64、Linux x64/arm64、macOS Intel/Apple Silicon 原生 runner 上打印真实架构，执行包内嵌 UI、启动/存储/锁/中文空格搬迁检查；Task 7 普通 SQLite business harness 接入后执行全套普通业务测试。未运行的环境不得声称通过。工作流只上传验证产物，不创建 tag/Release，也不推送 Docker 镜像。

## 手动升级和许可

上游 ARTEX 自动检查、下载、换装、回滚、SSE 和启动 bootstrap 全部停用。升级前关闭应用并完整备份目录，只手动安装经审核的 VulnHarvest 版本，保留数据及定制技能。

根 LICENSE 保持 GNU AGPL v3，前端和 go-mitmproxy 的 MIT 许可随包保留。Norma SDK 原始归档没有声明 LICENSE，发行目录 THIRD-PARTY-NOTICES.md 如实记录这一未解决事项；不得把它描述为已获明确再发行许可。源码 scoped provenance 保持不变。
