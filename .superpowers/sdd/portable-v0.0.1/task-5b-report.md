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
