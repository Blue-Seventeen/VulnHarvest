# Task 5b 便携运行时实施报告

## 结论

本轮完成 portable/postgres 模式接入、可执行文件相对路径、独立 SQLite state、跨平台实例锁、保留 listener 端口回退、运行时工具 API、历史任务暂停、显式 session 恢复、关闭 owner join、文件根约束以及可迁移任务归档路径。未读取旧 ARTEX 安装、旧密钥或旧进程；所有行为测试使用本 worktree、TempDir、loopback 与专用测试容器。

## 关键接口

- CLI：`-mode portable|postgres`、`-no-browser`、`-addr`、`-proxy`；portable 优先 CLI > `VULNHARVEST_MODE` > config > postgres。
- `GET /api/runtime`、鉴权 `POST /api/runtime/check`、鉴权 `POST /api/runtime/resume`，只返回工具名称/版本/路径/状态/影响范围，不返回 MCP env、Key 或密码。health 保留原字段并增加 `product`、`mode`、`storage_mode`。
- `Server.Shutdown` 取消并 join owned goroutine、HTTP request body、engine、side/archive、日志 writer；Manager 再关闭 enrich、traffic、DB。
- portable archive/reference 使用受管相对命名空间，关闭目录整体复制到新根后可 restore/delete；journal 恢复拒绝越界绝对路径并保留原始用户文本。

## TDD 证据

- `go test ./config -count=1` PASS：Redact URL/query/keyword/malformed secret；Temp executable + unrelated CWD 实际子进程 PASS。
- `go test ./runtimeenv -run 'TestInstance|TestPortableListener' -count=1` PASS：锁、loopback fallback、postgres fixed-port 行为。
- `go test ./server -run 'TestRuntime|TestPortableArchiveServerContract/SQLite' -count=1` PASS（含慢 body、shutdown join、文件/junction boundary、manual archive、cold relocation、journal recovery、runtime auth/rescan/resume）。
- `go test ./server -run '^TestRuntimePostgresOwnedDatabaseStartup$' -count=1` PASS，使用独立 `vh_runtime`（容器 `127.0.0.1:27347`；密码仅由 controller 环境变量注入）。
- `go test ./third_party/norma/tool -run 'TestRuntimeCleanup|TestCleanupRemovesDir' -count=1` PASS：background process/output watcher/PTY owner cleanup join。
- `go test ./cmd/artex -run '^TestPortableActualBinary$' -count=1 -timeout=120s` PASS 40.980s：真实 Temp binary、无关 CWD、occupied HTTP/proxy ports、portable over invalid PG env、auth 401、第二实例 lock reject、进程死亡后可重新取得 OS lock。

## 明确限制

- 未运行 Linux native/race gate；Windows host race/`0xc0000139` 仍交 controller。
- 未执行 full SQLite/PostgreSQL business suite；Task7 负责 test-factory 与完整 DB/LLM 业务闭环。PG portable 只做窄启动/健康边界。
- 真实授权 LLM、Playwright/MCP 业务调用未执行；startup/rescan 不执行 MCP business argv，也不下载包。
- 上游 norma/go-mitmproxy provenance/patch manifest 沿用 Task5a；本轮只对 norma background manager 做最小 owner join patch，controller 需运行最终 provenance verifier。

## Fix1 — I1–I5 与 M1/M2 集中修复

Fix1 固定源码提交：`aad0e5d16405837c02bbe4f2c422d349164e13c3`。此后报告提交只补本行；生产源码、测试和 provenance 与固定提交一致，controller 应以此 SHA 执行 native/race 与 re-review。

本节替代上方首轮 provenance 沿用/待验证的结论。Fix1 基线为 `8af8a93b3825133d36d62de56da40107bf4b4ced`，全部实验只用本工作树 TempDir、loopback、专用 PG 容器的 `vh_runtime`；未读取旧安装或真实 LLM secret。密码只经 `VULNHARVEST_PORTABLE_TEST_PG_PASSWORD` 注入，不写入报告或提交。上方首轮 norma 测试命令写成 `go test ./third_party/norma/tool` 不准确：nested module 的真实运行必须切换到 `third_party/norma` 后执行，下方给出准确命令。

### 最终实现与交接

- I1：只新增严格 UUID 的 `transcripts/exp0-btw-<UUID>[.jsonl]` 受管命名，归属由任务快照 conversations + side_question_requests 的 `session_key` 交集取得；拒绝其它 conversation、无记录、非 UUID。真实 completed conversation-side 行由现有 side runner 完成；因为 SideQuestionService 无 transcript writer，测试写入受管历史 artifact fixture，不把它宣称为模型自动生成 transcript。实际 archive → 半完成 restore 文件安装 → cold copy → journal rollback → restore → rearchive → 再搬迁 → delete 都校验同一行、Answer、artifact 与原始用户文本。
- I3：relative 与 legacy absolute journal 都经过相同的 canonical task/archive ID、精确 namespace、两端 `files/<relative>` 关系、task identity 与 snapshot/live ownership 检查。absolute 只允许当前 owned root 内的合法路径，绝不跟随旧根地址。portable stage/install/recover 的实际 read/write/mkdir/rename/remove 使用 `os.Root`，而非校验后 unrestricted syscall；测试还在校验后替换 junction，实际 rename 拒绝且外部内容不变。PG raw absolute 路径语义保留。
- I4：新增无 mkdir 的 `config.SkillPath` 与 `server.ValidatePortableRoots`，解析存在 ancestor/junction 和未存在 suffix；CLI 与 Manager 在创建 stores 前拒绝 state 与 data/skills 的相等、任一包含与 canonical alias 重叠，Server constructor 再校验。独立外置 data/skills 仍可启动；真实 binary argv 覆盖 base/state/data junction/skill junction。
- I5：portable 与 postgres 都创建同一个 ToolManager owner，捕获原 PATH，并在 stores 打开后读取数据库工具设置完成第二次 Check；GET environment、鉴权 POST/check 与真实 SDK 执行使用同一发布 generation。PG 固定监听与原恢复策略保留。实际 PG CLI fixture 先确认专用库无 enabled MCP，再验证 ready URL、401、启动 generation ≥2、重检测 generation+1、bundled node 路径和用户显式 custom command 的真实选择；启动/重检测 audit 只允许 node 与本地已知 npm/npx CLI 的固定 `--version`。Server/Manager 消费者 stop+join 后才执行 ToolManager.Close（defer 逆序）。
- M1：只用开库后第二次 Check 的最终 snapshot 记录日志。健康 ready 且无 candidate failure 时静默；missing/unverified、fallback 的 candidate source/failure reason 与影响均记录并经 config.Redact。日志不接受 MCP args/env/Key 输入。
- M2：runtime.json 同目录 CreateTemp(0600) → write → Sync → Close → rename；先写 ready:false，保留服务 ready 后写 ready:true 和相同实际 listener 地址。Windows sharing/access-denied 的 rename 有 2 秒有界重试，不退回 truncate 写；并发成功读取必须是完整 JSON。Windows replacement 期间 reader 的临时 sharing error 需要重试，测试只容忍这两个明确 OS 错误、不容忍 partial JSON；无残留 temp。
- I2：从只读原 norma archive 重新生成 raw exact patch/changed/add hashes 和文档；210 baseline / 6 changed / 7 added / 0 deleted（204 untouched）。proxy entry、upstreamFiles、原 docs/copyright/version 与缺失 LICENSE 的事实保留，未修改 module cache。统一 manifest 的原 LF，避免全文件 CRLF diff。

### 红测与修复验证记录

日志均位于 `.superpowers/sdd/portable-v0.0.1/`，保持 ignored 本地证据，不 force-add。普通 root gates 设 `ARTEX_CONFIG=<worktree>/.superpowers/sdd/portable-v0.0.1/test-isolated-config.json`、`ARTEX_PG_DSN=''`；需要 PG 的窄测试额外注入上述专用密码环境变量。下列只报告实际执行范围，不等同 full business suite。

```text
# I1/I3/I4 RED（task-5b-fix1-red.log；exit 1）
go test ./server -run '^TestRuntimeFix1|^TestRuntimeClosedFolderRelocation$' -count=1 -v
# accepted malicious journal / roots overlap / exp0-btw namespace rejection；旧根 legacy 已原先拒绝。

# I5/M1/M2 RED（task-5b-fix1-cli-red.log；exit 1）
go test ./cmd/artex -run '^TestRuntimeFix1|^TestPostgresActualBinaryRuntimeCheckAndSelection$' -count=1 -timeout=150s
# PG generation 0、ready 误报、并发读取 0-byte JSON 三项都复现。

# I2 RED（task-5b-fix1-provenance-red.log；exit 1）
python third_party/verify_upstream.py --module norma --archive 'D:/software/gopath/pkg/mod/cache/download/github.com/!autumn-27/norma/@v/v0.4.3.zip'
# AssertionError: file set mismatch。

# archive/roots/ownership/late-junction/restore-journal GREEN
# task-5b-fix1-archive-final.log；exit 0；server 6.654s
go test ./server -run '^TestRuntimeFix1|^TestRuntimeClosedFolderRelocation$|^TestRuntimeInterruptedJournalRelocation$|^TestPortableArchiveServerContract/SQLite$' -count=1

# server runtime + SQLite archive contract + archive package GREEN
# task-5b-fix1-server-final.log；exit 0；server 14.897s
go test ./server -run '^TestRuntime|^TestPortableArchiveServerContract/SQLite$|^TestTaskArchivePackage' -count=1

# config GREEN（task-5b-fix1-config-final.log；exit 0；1.095s）
go test ./config -count=1
# instance/listener GREEN（task-5b-fix1-instance-final.log；exit 0；0.710s）
go test ./runtimeenv -run '^TestInstance|^TestPortableListener' -count=1
# norma cleanup GREEN（task-5b-fix1-norma-final.log；exit 0；3.079s）
cd third_party/norma
go test ./tool -run '^TestRuntimeCleanup|^TestCleanupRemovesDir$' -count=1

# 最终 actual CLI/PG/log/atomic metadata GREEN
# task-5b-fix1-cli-stable.log；exit 0；cmd/artex 118.821s
# portable 39.44s；PG check+selection 74.48s；log 0.07s；metadata 3.50s
go test ./cmd/artex -run '^TestRuntimeFix1|^TestPostgresActualBinaryRuntimeCheckAndSelection$|^TestPortableActualBinary$' -count=1 -v -timeout=240s

# 全 root compile-only（task-5b-fix1-compile-verified.log；exit 0）
# 各包 [no tests to run]，不是 full business test PASS。
go test ./... -run '^$'

# root vet（task-5b-fix1-vet-verified.log；exit 0）
go vet ./config ./runtimeenv ./server ./cmd/artex
# nested norma vet（task-5b-fix1-norma-vet-verified.log；exit 0）
cd third_party/norma
go vet ./tool

# 两个独立 original-archive verifier，不以最后一个成功掩盖前一个失败
# task-5b-fix1-provenance-norma-verified.log；exit 0；210/6/7/0
python third_party/verify_upstream.py --module norma --archive 'D:/software/gopath/pkg/mod/cache/download/github.com/!autumn-27/norma/@v/v0.4.3.zip'
# task-5b-fix1-provenance-proxy-verified.log；exit 0；104/6/2/0
python third_party/verify_upstream.py --module go-mitmproxy --archive 'D:/software/gopath/pkg/mod/cache/download/github.com/lqqyt2423/go-mitmproxy/@v/v1.9.2.zip'
```

中间 CLI 回归失败也保留：`task-5b-fix1-cli-green.log` 为 test HTTP 5 秒小于串行 probe 总耗时、Windows reader 阻止 rename；`task-5b-fix1-cli-final.log` 为 sentinel 未允许合法本地 npm/npx Node wrapper `--version`、Windows sharing read；`task-5b-fix1-cli-verified.log` 为全包并行 compile 负载下大型 test binary sentinel 超过真实 2 秒 probe ceiling。最终使用小型独立原生 sentinel，未放宽生产 probe 上限；已相应修正 HTTP 总等待与明确 OS sharing retry，并未放开 package/install/business 自动执行。

### Fix1 范围限制

Windows host 普通 gate；Linux native/race 与 macOS native 仍需 controller 基于本 Fix1 固定 SHA 验证，首轮 `3f052006` 的 Linux/race PASS 不能代表本 Fix1。普通完整 SQLite/PG/真实 LLM/Playwright/MCP 业务闭环仍归 Task7；Task6 环境卡片直接消费 runtime/check，不再需要另补 postgres 后端检测。未修改或暂存 controller 的 `implementation/verification.md`，未提交日志、测试库状态、Key 或 secret。
