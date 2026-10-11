# Task 2 — 配置与基础 DAO

状态：实施完成，双库契约、现有 PG 模块回归与原 Task1 回归均已通过。仅使用本次独立工作树、Go t.TempDir 与一次性开发容器的 vh_task2 库。没有访问 G:\ARTEX-0.3.15、live-llm.secret.json，没有启动应用、通知客户端、MCP、下载或 LLM 请求。

## 变更与存储契约

- OpenSQLite 在成功迁移、reader 建立后补缺 seed。既有文件仍先 mode=ro 读取未来版本，未来版本拒绝路径没有新增 RW 连接或 seed。六个 builtin agents 的 name/description/role/budget/interactive_shell 全保留；新 agent 插入时给正确交互默认。取消启动时删除旧变量与覆盖变量目录的逻辑；删除 marker 也不会许可修改用户开关。
- seed agent 改为 INSERT ON CONFLICT DO NOTHING 后 SELECT id；变量、browser MCP、ScopeSentry MCP、skill visibility 均补缺。原 PG schema 已有 ScopeSentry seed，额外插入仍 DO NOTHING。既有 approval 默认 seed marker 与规则补缺保留；不启动任何调度。
- artex-sqlite 是真实 driver wrapper：NamedValueChecker 将 time.Time / *time.Time / sql.NullTime 等最终绑定值转换为 UTC 固定九位小数格式 `2006-01-02 15:04:05.000000000`。DB 方法、嵌入 DB、prepared statement、原生 *sql.Tx 同样受覆盖。转发现代 sqlite 已实现的 ConnBeginTx / ConnPrepareContext / ExecerContext / QueryerContext / Pinger；底层 stmt/rows 原样返回，其 context/列类型接口保留。完全不改 SQL 文本。
- now()、schema defaults 与 updated_at/retest trigger 使用同一 UTC 纳秒格式；generator 同步更新，所有 sqlite schema 文件保持 LF。portable_time_shift 做真实时间加减并返回同一格式，避免 datetime() 丢失小数后参与文本范围比较。
- JSONScanner 只在实际 Scan 目的地址使用：TEXT/string、PG JSONB/[]byte、SQL NULL；复制输入切片。nullableTimeScanner 只适配 MAX(ts) 这类 SQLite 缺失 DATETIME 类型 metadata 的 aggregate 返回；普通 DATETIME 保持 driver 返回 time.Time。jsonValue 在本项 JSON DAO bind site 明确将 SQLite JSON 写成 TEXT，PG 保留 bytes。
- 显式 DB.SQL / SQLite 独立分支：配置与引用锁、工具 JSON 数组增删/成员计数、对话 patch/批删/usage、旁路锁/ordinal/alias、records/usage Ensure 与查询、skill stats、审批审计与执行定位、通知 fanout/claim/lease/cooldown/stats、commands 与 chat_mentions。SQL 参数保持 $N。
- SQLite 写事务本来就是 BEGIN IMMEDIATE；通知选择、置 sending、attempt+1、deadline、batch_id 与完整记录读取均同一原生事务。不用伪造 FOR UPDATE、SKIP LOCKED 或 advisory-lock 函数。
- portable_json_overlay 仅用于审计对象的浅层右覆盖：null 保留为 JSON null，嵌套对象整体替换，不采用 json_patch 的递归合并/删除语义。
- portable_lower 使用 Go Unicode lowercase，再明确 LIKE ... ESCAPE '\'；保留 %, _ 与反斜线语义，Écho/écho 在两库实测。chat_mentions SQLite union 使用每分支子查询，保留 exact-ID 优先、稳定 keyset cursor、20+1 页面语义。
- SQLite AFTER INSERT ordinal 返回值修正：StartSideRequest 在同一事务按 id 重读，首次 DTO 的 ordinal 非零。对话 patch 与审批规则 UPDATE RETURNING 显式设置 updated_at=now()，不依赖 AFTER trigger 执行时机。
- EnsureLLMRecordsTable/EnsureLLMUsageTable 在 SQLite 只验证当前版本及表/关键列；不执行 PG DDL/advisory。PG 原有 DDL 仍使用 migration lock。
- 只读 ArchivedAggregateStats 的 JSON Scan 本地适配；实际冷归档记录参与 profile、skill、tool 历史统计。没有实现归档写入/恢复。
- IsBusyError 增加 wrapped PG 55P03，SQLite BUSY/LOCKED 原有 extended code 保留；新增 IsCheckViolation（PG 23514 / SQLite 275），不匹配错误消息。

## TDD 与确切证据

1. `go test ./db -run '^TestPortable(Seed|Date)' -count=1 -v` 首次 FAIL：builtin missing；+08:00 已过期 open_until 被 LoadLLMHealth 返回。完成 seed、driver 参数规范化、scanner 后 PASS。
2. `go test ./db -run '^TestPortableDAOContracts$' -count=1 -v` 首次业务失败：profiles/conversation/side 近 FOR 语法错误；tools 近 ? 语法错误；records pg_advisory_xact_lock 不存在。逐模块显式 SQL 后只剩 skill STRING_AGG DISTINCT 两参数问题；改 SQLite GROUP_CONCAT(DISTINCT agent_key) 和 aggregate time scanner 后全 PASS。
3. `go test ./db -run '^TestPortable(Notification|Intercept)' -count=1 -v` 首次 FAIL：NotificationChannel.config TEXT 无法 Scan 到 RawMessage；ResolveIntercept ::jsonb 不支持，8并发没有赢家。适配后 PASS，双库验证8并发各只有一个 claim/decision 成功。
4. `go test ./db -run '^TestPortableMentions' -count=1 -v` 首次 FAIL：:: token。显式 union SQL 后 PASS；双库 Écho/écho、literal %_Back\、exact-ID 与分页 cursor 验证通过。
5. `go test ./db -run '^TestPortable(Error|ProfileTask|Unicode|Prepared)' -count=1 -v` 首次编译 FAIL：IsCheckViolation undefined。分类实现后真实业务 FAIL：profile chain 的 FOR UPDATE；Unicode commands 空结果。限定 ReplaceTaskLLMProfiles 锁分支、portable_lower 后同命令在 SQLite+PG PASS。native prepared Tx、纳秒 since 排除边界、fractional time shift 与 JSON scanner NULL/copy 通过。
6. `go test ./db -run '^TestPortableIntercept' -count=1 -v` added RETURNING test FAIL：updated_at 与旧值相同。显式 SET 后 PASS；测试有2ms时钟推进，避免本机时钟分辨率使连续 now() 同值。
7. `go test ./db -run '^TestPortable(CustomTool|Cold)' -count=1 -v` nil tool Agents 首次 FAIL：添加 worker 后仍空数组；两处 create/update 将 nil/empty 明确保存 [] 后 PASS。Cold SQLite 真行已通过；PG 初次 fixture 因未 Ensure runtime llm_usage 表失败，补 fixture 初始化后验证。
8. `go test ./db -run '^TestPortableJSONStorage' -count=1 -v` 首次 FAIL：typeof(agents) 是 blob；局部 bind jsonValue 后 PASS，tool JSON 与 intercept input/audit 均 TEXT。

测试构造中曾误用 explorations.title（应为必填 goal）、Activity 的 *int token 字段；这些是 fixture 修正，不报告为生产缺陷。PG JSONB 会格式化空白，所以跨库 JSON input-output 用结构相等；重启前后同库 snapshot 使用完整字段比较。单独筛 conversation 子测试会缺失 profiles 子测试的 fixture；最终命令运行整个契约组。

9. `go test ./db -run '^TestPortableDAOContracts$' -count=1 -v` 新 UTC summary timestamp 断言先 FAIL：原 SQLite CAST 字符串缺 T/Z，前端可能误读为本地时间。SQLite SELECT 显式转换 RFC3339 UTC 之后最终全组 PASS；PG 时间 DTO 不变。

## 复核命令（已执行）

- 环境 `TASK2_PG_DSN=postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_task2?sslmode=disable`。test-only task2Stores 在重建 public 前检查 current_database() 必须恰为 vh_task2，每组 PG 契约新空 schema；SQLite 每组独立 t.TempDir，无失败跳过。
- `go test ./config ./db -run 'TestResolveMode|^TestSQLite|^TestPortable|TestApplySchemaRetries' -count=1`：SQLite + PG 全契约 PASS，最终 config 0.528s / db 11.468s。包括未来 WAL（常驻连接与崩溃子进程）字节保护、原 Task1 模式/完整schema/备份/transaction/路由全部原有测试。
- 清空本容器 vh_task2 public 后设置 `ARTEX_PG_DSN` 为同一专用 endpoint：`go test ./db -run 'Test(OpenSeed|PoolProfiles|DeleteProfile|Conversation|Intercept|LLMRecords|LLMRetry|Notification|FanOut|Claim|Digest|Delivery|SaveNotification|DeleteChannel|RecordNotification|SetFindingStatus|SkillUsage|ToolUsage)' -count=1 -v`：PASS 9.193s，无 skip；含原 PG 锁顺序/引用重试、raw records migration、通知 savepoint 错误隔离与 lease/state/digest、approval archive compatibility、skill/tool usage 既有回归。保存点测试的日志中 CHECK 失败是故意注入并成功回滚的测试证据。
- `go vet ./config ./db`：PASS（退出码0，无诊断）。
- `git diff --check`：PASS。

## 接缝与范围警告

- 新接缝：JSONScanner{&raw}、DB.jsonValue（包内bind helper）、nullableTimeScanner（包内aggregate helper）、portable_lower / portable_json_overlay / portable_time_shift SQLite函数、IsCheckViolation。
- lockLLMProfileForReference 与 lockSideParent 改为 DB receiver；只有本项调用者，已更新。lockProfileReferenceRows 仍只执行调用者已显式选择的 query，不隐藏任何 dialect 推断。
- ReplaceTaskLLMProfiles 仅适配删除 profile 测试所需的 task-first lock；task_context 的其他 ANY/bulk hydration/MarkTaskLLMProfileQuotaExhausted 和 tasks 的 setter/创建/生命周期属于 Task3，未宣称任务DAO完成。
- WithEvidenceTx 仅为通知 enable/status 所需改 SQLite immediate / PG advisory 分支。SetFindingStatusTx 新增可选 dialect ...*DB，DB.SetFindingStatusWithNotify 传入 d；Task4 的 finding_retests 等调用者必须同样传入 d，否则默认保留原 PG FOR UPDATE。本项没有改其他 finding/evidence写入链。
- Task4 的归档写入/manifest/恢复与数组绑定未适配；这里只读 aggregate scanner。Task3/4 尚有 PG-only SQL，不能据本报告宣称 SQLite 全产品已经可运行。
- Task5 仍需 portable 重启禁自动恢复/通知/调度/下载 gating；本项只是配置与DAO，不启动运行时。
- 没有运行真实应用、网络客户端、LLM、前端或五平台 build。race 本机 Task1 已记录 Windows 0xc0000139 环境问题，未把普通并发测试称为 race 检查。

## 分模块提交

- 207eb10 `feat(db): preserve portable configuration and adapt core history DAOs`
- d72213e `feat(db): adapt portable approvals notifications and Unicode search`
- 第三项 `test(db): cover portable persistence and DAO edge contracts` 提交包含本报告；确切 commit 在交接回复给出。
