# ARTEX / VulnHarvest 项目维护上下文

记录日期：2026-10-11（Asia/Shanghai）。本文件是当前源码与本机状态的快照，供后续维护、升级前快速定位；不是完整代码审计或部署验收。版本、运行路径和验证结果发生变化后应重新核对。

维护范围约束（用户于 2026-10-11 明确要求）：旧目录 `G:\ARTEX-0.3.15` 的实例正在运行用户任务，禁止对其执行停止、重启、更新、替换产物或修改配置、持久化数据。后续工作默认仅操作当前源码工作区；不得影响旧实例。

## 1. 当前工作区与版本基线

- 工作目录：`G:\SoftwareDevelopment\Hack3rX_2026_011_ARTEX`。
- Git 分支：`main`；基线提交：`84a9d25`，提交说明 `Init Project Based From ARTEX v0.3.15`。
- 唯一远程：`origin = https://github.com/Blue-Seventeen/VulnHarvest.git`；尚未配置 `upstream`，本地没有 tag。
- 当前 `main` 比本地缓存的 `origin/main` 领先 1 个提交；该远程跟踪分支仍在 `6c92dda`，只有 README。此次未 fetch，不代表 GitHub 当前实时状态。
- 导入基线含 599 个受 Git 跟踪的文件；153 个 Go 测试文件、2 个前端 Node 测试文件、27 个前端页面文件。
- `CHANGELOG.md` 最新正式记录是 `0.3.15 / 2026-10-07`。二进制版本来自 `cmd/artex/main.go` 的 linker 注入，默认 `dev`；`web/package.json` 的 `2.2.0` 是前端包版本，不能代替系统发布版本。
- Go module 仍是 `github.com/Autumn-27/artex`。没有基于上游完整 Git 历史的分叉，后续升级不能仅凭这份提交历史识别全部上游变更。
- 本次开始时工作树干净；未发现工作区或已检查父目录中的 `AGENTS.md`。

## 2. 项目定位与技术栈

这是 LLM 多 Agent 驱动的自主渗透测试平台。核心能力包括任务编排、目标拆解、资产管理、探索过程与漏洞记录、人在环路对话、流量留痕、工具审批、漏洞复测和报告导出。

| 层 | 当前技术 / 关键入口 |
| --- | --- |
| 后端 | Go `1.26.3`，标准库 `net/http`；`cmd/artex/main.go` |
| Agent SDK | `github.com/Autumn-27/norma v0.4.3`，包含 LLM、harness、tool、hook、transcript 等能力 |
| 前端 | Next.js `^16.2.9`、React `^19.2.7`、TypeScript、Tailwind、shadcn/Base UI；锁定依赖见 `web/package-lock.json` |
| 业务数据库 | PostgreSQL，`pgx/v5`；Compose 默认 PostgreSQL 16 |
| 流量存储 | SQLite 索引与磁盘正文，`modernc.org/sqlite`；并非所有数据都在 PostgreSQL |
| 通信 / 鉴权 | REST `/api/*`、SSE、JWT |
| 生产打包 | Next 静态导出 + Go `embedui`，单个二进制可提供前端与 API |

根目录 `LICENSE` 为 AGPL-3.0；`web/LICENSE` 是前端模板的 MIT 许可，不能仅据此前端文件推断整个派生项目的许可。

## 3. 目录职责与修改入口

| 目录 / 文件 | 维护职责 |
| --- | --- |
| `cmd/artex/` | CLI 参数、版本、启动、信号处理、优雅关闭、更新重启协议 |
| `server/server.go`、`server/server_mgmt.go` | HTTP 路由、API、管理操作；前者是路由总入口 |
| `server/manager.go`、`server/engine.go` | 任务与共享资源生命周期，planner / worker 循环、暂停、取消、删除屏障 |
| `server/goals.go`、`server/scheduler.go`、`server/orchestration.go` | 目标拆解、任务并发准入/FIFO、Agent 触发、跨任务编排 |
| `agent/` | goals、planner、worker、mainagent、chat、retester，提示词、工具装配、上下文压缩、执行收尾 |
| `agent/provider.go`、`llmpool/`、`llmrec/` | Anthropic / OpenAI / Responses 接入、故障切换、限流和模型调用/用量记录 |
| `db/` | 资产、探索、任务、漏洞、LLM 配置、审批、归档等持久化；`schema.sql` 为集中 schema |
| `config/` | 配置路径、数据与 skill 路径解析、数据库 DSN；`ARTEX_PG_DSN` 优先于文件配置 |
| `guard/`、`intercept/` | 工具调用 hooks、审批规则、挂起/批准/拒绝流程与记录 |
| `traffic/`、`evidence/` | MITM 录制代理、流量索引/正文/搜索/归档、漏洞证据绑定与回收 |
| `enrich/` | DNS / HTTP 资产异步补全 |
| `mcphttp/`、`skills/` | 远程 MCP 协议与内置能力资源；Agent 可见性在服务端与数据库中装配 |
| `sidequestion/`、`notify/`、`report/` | 旁路提问、通知投递、报告生成 |
| `selfupdate/`、`start.sh`、`start.bat` | 发布包下载/校验/换装/回滚、守护进程重启 |
| `web/src/app/` | 页面与布局；主要业务在 `(main)/function`，系统配置在 `(main)/system` |
| `web/src/components/` | 图、会话、审批、漏洞证据和通用 UI 组件 |
| `web/src/lib/api.ts`、`types.ts` | 前端 API 客户端与数据契约；调整接口时需和后端 DTO 一起检查 |
| `web/src/lib/mock/` | 无后端演示模式；模拟页面可用不等于真实链路可用 |
| `.github/workflows/release.yml`、`build.sh`、`Dockerfile` | 多平台二进制、校验文件、GitHub Release、Docker 镜像发布 |

## 4. 核心数据模型与执行链

- **资产图**跨任务共享：公司、根域、子域、IP、service、app、endpoint 等资产；结构与去重主要在 `db/assets.go`、`db/company_scope.go`、`db/task_scope.go`。
- **探索图**按任务隔离：goal、intent、fact、finding、hint；关系与锚点位于 `exploration_nodes`、`exploration_edges`、`exploration_anchors`。资产图与探索图通过 asset ID 锚点联系。
- **漏洞记录**不能一律当作探索节点：`findings`、复测表、流量证据绑定有独立 ID 与流程，接口变更要核对 `finding_id` / node ID 的区别。

已从当前代码追踪的主要链路：

1. 页面经 `web/src/lib/api.ts` 调用 `POST /api/tasks`，由 JWT 中间件进入 `server.createTask`。
2. 请求验证 LLM 配置、关联任务、企业、范围与拦截规则；`Manager.CreateTaskWithOptions` 创建任务与探索上下文。
3. `server/goals.go:launchTask` 播种资产/可选种子意图，经并发准入启动或进入持久化 FIFO；`startTaskEngine` 拆分目标后启动 `Engine.Run`。
4. `plannerLoop` 根据通知和心跳唤醒，合并变化并检查暂停、删除、终态与超时；`Planner.Plan` 生成待执行意图。
5. `workerLoop → runWorkerStep → claimNext → runIntent → Worker.Execute` 领取一条意图并执行，工具经 guard/intercept，写回资产、事实、漏洞与 activity。
6. 写回再触发规划；前端通过 SSE / 分页历史刷新活动、图与结果。目标证明或超时/人工停止后进入对应终态。

这条链路已核对源码调用关系，本次未创建真实任务或发起模型调用。

## 5. 启动、构建与持久化边界

- 默认 API/UI 端口 `8787`；Go CLI 默认录制代理 `127.0.0.1:8788`。Compose 的 UI 映射是 `8787:8787`，代理映射是 `127.0.0.1:8788:8788`。
- `config.json` 或 `ARTEX_PG_DSN` 必须提供数据库连接，没有可隐式使用的默认数据库。首次前端访问 `/setup` 设置管理员密码；运行 Agent 还需要有效 LLM 配置。
- 服务启动时 `db.Open` 执行内嵌 `schema.sql`，并有 PostgreSQL advisory lock 与死锁重试；不是独立版本化 migration 工具。二进制回滚不会自动回滚数据库结构。
- 需要保全 PostgreSQL、`data/`、JWT 签名密钥和定制 `skills/`。JWT 当前优先位于可执行文件旁 `jwt.key`，兼容旧 `data/jwt.key`；`data/traffic/` 含 SQLite 索引/正文/代理 CA，`data/evidence/` 存漏洞流量正文副本。
- 开发后端可用 `go run ./cmd/artex`，不带 `embedui` 时后端不提供前端静态页面。
- 前端开发是 `cd web; npm run dev`，API 通过 Next rewrites 转发。`README.md` / `dev.sh` 写的前端 `5173` 没有在实际 npm 脚本或 Next 配置里设置；`next dev` 通常默认 `3000`，需要 `npm run dev -- --port 5173` 才能与文字说明一致。
- 前端生产静态构建需 `NEXT_EXPORT=1`；POSIX 环境可直接 `npm run build:static`。PowerShell 应先 `$env:NEXT_EXPORT='1'`，再 `npm run build`，不能直接依赖脚本里的 POSIX 环境赋值语法。
- 将 `web/out` 内容复制到 `server/webui/dist` 后，以 `go build -tags embedui ... ./cmd/artex` 构建内嵌二进制；系统版本用 `-ldflags '-X main.version=<version>'` 注入。
- 本地 Dockerfile 不编译 Go/Next，要求先准备 `dist/<架构>/artex`，然后构建运行镜像。
- `start.sh` / `start.bat` 按退出码 `75` 重启以完成更新；正常退出码为 `0`。直接运行二进制不能依赖这套守护重启流程。
- 生产 SSE 默认同源，开发模式默认直连后端 `8787`；`NEXT_PUBLIC_SSE_BASE` 是构建期变量，反向代理需关闭 SSE 缓冲。

## 6. 本机与当前目录的区别

本次读取的当前状态：Go `1.26.3 windows/amd64`、Node `24.14.0`、npm `11.9.0`，Docker daemon `29.7.2` 可访问。

当前工作区没有 `.env`、`config.json`、`artex.exe`、`data/`、`start-local.ps1`、`web/node_modules`、`web/out`、`server/webui/dist`；当前进程环境未设置数据库 DSN 或 Anthropic/OpenAI Key。它尚不是这份源码的已验证部署目录。

本机存在另一个 ARTEX 实例：进程路径为 `G:\ARTEX-0.3.15\artex.exe`，本次 `/api/health` 返回 `ok=true`、`service=artex`、`version=0.3.15`，监听 `127.0.0.1:8787` 和 `127.0.0.1:8788`。这些检查只证明旧实例正在提供健康接口，不证明它使用本仓库源码，也不证明 Agent 端到端功能。

先前部署记录也指向旧目录。后续调整前必须明确修改的是源码仓库，还是旧实例的部署产物与持久数据；不应混用两套配置或直接覆盖运行数据。

## 7. 验证基线

实际执行的基线检查：

| 检查 | 当前结果 |
| --- | --- |
| `go test -json -count=1 -timeout=120s ./...`（禁用已有数据库/实测模型配置） | 退出码 `1`；14 个包通过、1 个包失败、2 个包无测试；JSON 测试事件含子测试：575 pass、23 fail、249 skip |
| 两个前端 Node 测试文件 | 7 个用例通过，0 失败、0 跳过；有 Node 模块类型提示，不影响本次退出码 `0` |
| Windows 单独复现 `TestSkillRelPath` | 退出码 `1`，可独立稳定复现斜杠格式差异 |
| 旧目录实例 `/api/health` 与监听端口 | 健康接口成功，版本 `0.3.15`；8787 / 8788 均为环回监听 |
| 工作树检查 | 原有受跟踪文件没有改动；仅新增本维护文档，未提交或推送 |

23 个失败中，22 个因没有数据库配置而在 `NewManager` 阶段失败，集中于漏洞流量/工作流及通知测试。它们复用了 `server/finding_traffic_test.go:23` 的 `trafficEvidenceServer`，该 helper 在无法连接数据库时调用 `t.Fatal`，而其他许多测试采用 `t.Skip`，所以无数据库环境下并非统一跳过。这些失败不能据此断言对应业务功能有缺陷；需独立 PostgreSQL 测试库再验证。

另 1 个为 `server/skill_upload_test.go:39` 的 `TestSkillRelPath`：`server/server_mgmt.go:1509` 中使用 `filepath.Clean`，Windows 返回 `scripts\run.py`，测试期待 `scripts/run.py`。已用 `go test -count=1 -run '^TestSkillRelPath$' -v ./server` 单独复现。本次只记录现有的跨平台返回值/测试契约差异，不修改实现或断言，也不扩大成已证明的上传功能故障。

完整 Go JSON 日志：`C:\Users\cy\AppData\Local\Temp\artex-context-go-test-a90a1452f9154acd8ae69b8c5e2002b4.jsonl`（临时文件可能日后被清理）。复现无数据库基线时，可将 `ARTEX_PG_DSN` 清空，`ARTEX_CONFIG` 指向不存在的测试配置路径，并清空 `ARTEX_REVIEW_LIVE_CONFIG`；本次未使用真实数据库或真实 LLM。

未安装前端依赖，未执行前端完整构建、类型检查或 Biome 检查，未运行真实 LLM/Agent 任务，未启动当前源码服务。

可复用的验证入口：

- 后端：`go test -count=1 -timeout=120s ./...`。大量测试依赖 PostgreSQL；无 DSN 时多数跳过、部分直接失败，不能把非数据库测试结果解释为数据库完整回归通过。
- 需数据库的回归：先准备**独立测试数据库**，再提供 `ARTEX_PG_DSN`；不要把应用真实数据直接交给会清理/修改数据的测试。
- 前端现有逻辑测试：`node --test web/src/lib/activity-merge.test.mjs web/src/lib/chat-mentions.test.mjs`，本机 Node 24 可直接处理相邻 TypeScript 模块。
- 前端类型/静态检查：安装锁定依赖后检查 `npx tsc --noEmit`、`npm run check` 与静态构建；当前 npm scripts 没有 `test` 入口。

## 8. 后续升级优先核对的事项

以下是本次已从配置确认的维护边界，不代表已经修复：

1. **发布来源仍沿用上游**：`selfupdate/github.go` 的 `Repo` 固定为 `Autumn-27/artex`；Compose 与 release workflow 镜像仍为 `autumn27/artex`。正式定制版本若开启上游一键更新或拉取该镜像，可能替换掉定制产物。独立发布前需统一更新来源、镜像命名与发布凭据；开发版本已有禁止自更新的判断。
2. **Git 基线没有上游历史与 tag**：只导入了文件快照。做持续同步前需确认可信的上游 `v0.3.15` 基线及需要保留的定制，再确定增量吸收策略。
3. **CI 主要覆盖发布**：只有 tag `v*` 触发的 release workflow，含前端构建、五平台 Go 构建、Linux amd64 `-h` 冒烟、校验文件和镜像推送；未设置 PR/分支测试，也未执行 `go test`、前端逻辑测试或 Biome。
4. **Windows 细节需独立验证**：源码构建脚本以 Bash 为主，且 `.gitignore` 忽略 `/artex` 但没有显式忽略根目录 `/artex.exe`。建议将构建产物放在已忽略的 `dist/`；本次不改脚本。
5. **文档/测试资产不完整**：`docs/` 被整体忽略，多个源码注释提及的设计稿未随 Git 提交；`docker-compose.bench.yml` 引用的 `Dockerfile.bench` 与 `bench/` 在当前工作区不存在，不能默认 Benchmark 部署可用。
6. **版本与数据兼容必须分开验证**：norma SDK、LLM Responses/流式接口、任务状态机、工具装配、证据绑定与 DB schema 都是升级回归重点。schema 更新后保留旧二进制不等于数据可安全回滚。

后续每次维护应先重查工作树、目标版本和实际运行来源；明确保留的定制及数据兼容要求，再做有边界的修改、针对性回归、构建验证与必要的运行验证。此文件不授权自动推送、发版、替换旧实例或创建定时任务。
