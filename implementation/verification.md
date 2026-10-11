# VulnHarvest v0.0.1 验证记录

本文件随实施更新。未完成的检查不会记为通过；交叉编译不能替代原生运行验收。

## 隔离范围

- 开发分支：`codex/portable-v0.0.1`，基线 `b183535`。
- 工作树：`C:\Users\cy\.codex\worktrees\portable-v0-0-1\Hack3rX_2026_011_ARTEX`。
- 专用 PostgreSQL 16 容器：`vulnharvest-portable-test-pg`，独立临时存储，无宿主数据挂载，动态环回端口。
- 不访问或修改旧实例目录 `G:\ARTEX-0.3.15`。

## 改造前检查（2026-10-11）

- `npm ci --no-audit --no-fund` 完成；原始前端 `NEXT_EXPORT=1 npm run build` 成功，31 个静态页面完成导出。
- 原始前端两个 Node 测试文件：7 通过；Node 提示 package 未声明模块类型。
- 在独立 PostgreSQL 库执行原始 `schema.sql` 成功。最终元数据包含 49 张表、520 列、163 条约束、164 个索引；主业务还包括运行时创建的 `llm_records`、`llm_usage`。
- 初轮独立 PG `go test -json -count=1 -timeout=180s ./...` 未通过：888 条测试事件 pass、14 fail、14 skip（包含子测试），11 个包通过。存在跨包清理/初始化竞争，不能视作数据库回归完成。
- 已确认 Windows 证据写入的目录 `Sync` 报 `Access is denied`，影响证据绑定及归档；纳入本次跨平台修复。
- 已在原始源码独立复现 `TestSkillRelPath`：Windows 返回反斜杠，API 期待 `/`；纳入便携路径修复。
- 后续串行测试仍存在背景任务恢复/未等待退出造成的共享测试状态干扰；需隔离测试数据和等待退出后再给出最终回归结果。

## SQLite 基础（分项已审查）

- 模式解析、51 张业务表、独立读写池、事务、外键、WAL、迁移备份与错误分类已实现，Task 1 分项审查通过。
- 未来 schema 拒绝覆盖两种真实 WAL 状态：外部连接保持打开，以及子进程异常退出后没有常驻连接；拒绝后主数据库与 WAL 哈希保持不变。
- Task 1 的配置/SQLite 聚焦测试与 `go vet ./config ./db` 通过；完整业务和运行时尚在后续分项实施。
- Windows race 测试程序在断言执行前退出 `0xc0000139`，未获得 race 检查结果，不计为通过。
- 接入补缺初始化后的主代理复核（2026-10-12）：`go test ./config ./db -run 'TestResolveMode|^TestSQLite|TestApplySchemaRetries' -count=1` 通过，config 0.501s、db 3.429s；PostgreSQL DSN 为空且配置路径显式指向本次隔离文件。

## 配置与基础 DAO（分项已审查）

- 提交 `207eb10`、`d72213e`、`302345f` 覆盖补缺初始化、JSON/UTC 参数绑定、配置、工具绑定、对话、旁路、模型记录与用量、审批、通知领取及搜索。尚未认定全部产品业务兼容。
- 主代理复核：`go test ./config ./db -run 'TestResolveMode|^TestSQLite|^TestPortable|TestApplySchemaRetries' -count=1` 通过，config 0.414s、db 11.735s；SQLite 临时文件与本次专用 `vh_task2` PostgreSQL 均实际运行。
- 未运行应用后台任务、通知发送、MCP 或模型调用；这些运行时验收仍在后续分项。
- 本轮跨包检查 `go test ./... -run '^$'` 通过；这是编译与测试入口检查，没有运行业务断言，不计作全量回归。
- 审查指出 SQLite 时间参数驱动包装遗漏连接复用接口；提交 `3f36cfd` 转发底层 `ResetSession` / `IsValid`。真实查询执行取消、原生事务回滚和后续读写回归通过，主代理聚焦复核 1.637s，通过复审。
- Linux amd64 实际 race 检查：从固定 `3f36cfd` 的源码归档在独立 `golang:1.27.2-alpine` 临时容器运行 `go test -race -mod=readonly ./config ./db -run 'TestResolveMode|^TestSQLite|^TestPortable(ConnectionReuseInterfaces|ExecutingCancellationAndReuse|DateBindingsAndJSON|PreparedTimeAndScanners)$' -count=1`，config 1.025s、db 14.384s，通过且无竞态报告。只覆盖该基础范围，未代表整个应用 race 验收。

## 资产与公司（分项已审查）

- 原源码在专用 `vh_assets` 初始空库执行资产/公司聚焦基线，30 个 pass 测试事件、0 fail、0 skip（包含包事件），7.479s。后续双库适配由分项测试与审查验证。
- 分项提交 `92f470c` 包含八类资产、公司范围、JSON 无损合并、DSL 与纯读池接缝。主代理执行完整聚焦双库 suite：145 个 pass 事件（包含包）、0 fail、0 skip，83.126s。初轮审查发现四处反例：UTC 字符串缺时区、指数数值及负零排序、params 文本身份、映射 IPv6 的地址族匹配；后续修复及复审结果见下一条。
- 审查确认的窄 PostgreSQL 兼容修正：auth 对象按值去重、重复 params 的显式 C 字节排序、数组反斜杠转义、A/AAAA 归属优先、副作用错误传播与删除原子性、DSL 的 OR 范围约束；未在启动时重写历史资产。
- 修复提交 `d01d7db` 保留 UTC 时区、十进制 scale、PG 文本分组身份及 IPv6 地址族。修复后统一 suite：179 个测试 pass 事件及 1 个包 pass，0 fail、0 skip，117.943s。主代理另独立运行新增反例组：36 个 pass 事件（含包）、0 fail、0 skip，6.712s；复审确认全部问题已修复，无新增重要问题。

## 最终验收

任务/探索图原源码聚焦基线：在专用 `vh_task3` 初始空库执行 `^Test(Task|Exploration|Intent|Constraint)`，21 个 pass 事件、0 fail、0 skip，7.076s；不是完整 DB 回归。

尚未完成。SQLite 全业务、Agent 闭环、文件持久化、运行时、UI、五平台构建与运行、PostgreSQL 回归逐项完成后填写准确证据。

用户已提供独立测试 LLM 配置；`/v1/models` 正常，`deepseek-v4-flash` 的单次 echo 工具调用成功（458 tokens）。尚未执行本系统真实 Agent 闭环，不将接口测试当作业务验收。测试密钥仅存于 Git 忽略的本次测试目录，不会从旧实例读取 Key。
