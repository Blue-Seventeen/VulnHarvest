# Task 3a 实施报告

范围依据 `task-3a-brief.md`：任务配置与上下文、公司创建快照、LLM 链、分类、模板和任务拦截规则。工作树 `C:\Users\cy\.codex\worktrees\portable-v0-0-1\Hack3rX_2026_011_ARTEX`，分支 `codex/portable-v0.0.1`，业务审查 base `d01d7db`。本报告不宣称整个 Task3 完成。

## 实现

- 公司关联 SQLite 分支先批量核对全部 IDs，再按输入去重后的公司顺序插入 task_scope；同一立即事务内追加资产 task_ids，使真实 membership trigger 创建关联，再写 company provenance。继续复用 Task4a 公司 scope 写锁。无效公司、来源、profile、拦截规则导致 exploration/task/关联/资产快照整笔 rollback；公司列表不存在资产时也保持公司关联。
- `hydrateTasksContext` 仍为三次批量 SELECT，分别读取链/来源/公司。SQLite 使用 JSON IDs 参数和 json_each，PG 保留 bigint[]；不使用每任务查询循环。大于 2^53 的相邻任务 IDs 有真实双库断言。
- LLM quota 行锁显式选择方言；SQLite 使用原生立即事务，PG 保留 FOR UPDATE。链替换、revision、cursor、ready/quota_exhausted、terminal 编辑、profile 删除和 late duplicate 行为保持，未改 Task2 DeleteProfile。双库原链测试包含并发末项只推进一次、旧 revision、不在新链的 profile、不从 earlier ready entry 绕回，以及 active profile 删除后正确推进/回退。
- Task name/pin 使用显式 SQLite CASE，保留 nil/false/true 和首次 pinned_at；parent、paused、queued、queue_mode/FIFO、terminal completed_at 和 heartbeat/coverage false 保持。SQLite StampFirstRun 使用同一个 Go UTC 瞬间的原生绑定生成 first_run_at/deadline_at，条件 UPDATE 幂等，非正 timeout 无 deadline；没有日期字符串 SQL 重写。
- 分类创建 DML CTE 为显式 SQLite INSERT RETURNING；rename 在立即事务内 UPDATE 后读实际 count 与最终时间；批量赋值用 JSON IDs，missing IDs 不使其余 live tasks 失败。unique 错误使用统一 classifier。
- `SetTaskCategory` 两库均改为同一原生事务 UPDATE 后读 count：原 PG CTE 在同一个 statement snapshot 中返回更新前计数。最小反例为清掉任务分类再重新赋回，原返回 0、实际 1；现在返回 1。保留缺失 task/category、nil category、UPDATE 与 FK 原生锁和事务回滚合同。该窄 PG 修复已由主代理明确授权。
- 模板使用 Task2 JSONScanner/jsonValue；保持 typed JSON、category nullable patch、规则 nil 显式清成 []、未供应字段和 Enabled=false。PATCH 仍在数据库中按独立字段 CASE 合并，双库并发 disjoint PATCH 不丢更新。
- SQLite AFTER UPDATE 触发器在 RETURNING 之后可能再次推进 updated_at。rename/template PATCH/intercept UPDATE 都显式写时间，再在同一立即事务内读触发器完成后的最终时间；不会以外部 reader 替代该 tx。PG 模板原 UPDATE 路径保持，拦截 UPDATE 显式 now 与原 PG BEFORE trigger 保持同一事务时间语义。
- 任务拦截规则所有读写优先复用 AssetStore 的已有 tx；自己创建的 SQLite UPDATE tx 才 commit。真实 scoped read/write/rollback 测试证明不会读旧 reader 状态、嵌套 begin 自锁或提交调用者事务。
- 普通 DeleteTask 所需的 `SELECT exploration_id ... FOR UPDATE` 增加显式 SQLite 无行锁分支；它已经在立即事务内。完整 deleteAssets/prepare/共享资产保护的其他 SQL 与业务验收留 3c，未删除 PG 表锁、未改 callback 顺序。

## 测试隔离与覆盖

`tasks_portable_test.go` 工厂只接受显式 `ARTEX_TASK_TEST_PG_DSN`，reset 前查询 current_database()，不等于 vh_task3 则 Fatal。实际运行 endpoint/account 为 `postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_task3?sslmode=disable`；从未 reset 其它 DB。SQLite 每 case 使用 t.TempDir() 文件，打开/迁移/断言失败 Fatal，无 SQLite skip。PG lock observation 的 `ARTEX_PG_DSN` 同样显式指定这一专用数据库，不使用 app 默认 config。

共同 case 覆盖 23 项：peer 公司 snapshot、peer 旧 revision、分类错误/CRUD/批量/无效输入、首次运行与状态、nullable 模板/最终时间、拦截 CRUD/scoped tx、重开持久化与仅直接来源、无效关联整体 rollback、2^53 以上 ID hydration、模板 CRUD/并发 patch、pin/name 排序、FIFO queue、全 LLM chain/terminal/duplicates、公司 snapshot、bulk context 和任务排序。对来源上限 8、无效关系回滚、非递归来源、公司来源覆盖不污染其他任务 provenance 也有断言。

peer 测试在两个独立 DB handle 间持有真实事务：公司 scope 归属变更未提交时任务创建必须等待，提交后快照读最新公司；revision UPDATE 未提交时 quota mutation 必须等待，提交后原 revision 标记 stale 而 cursor 保持 ready。使用启动 channel、100ms 的不得完成窗口和 10s 完成 deadline，不模拟 SQLite PG 锁函数。PG 原 TaskLLMProfileMutationsLockTaskBeforeProfile 的真实 PID/行锁观测另纳入最终 suite。

重开测试只打开本 fixture DB，覆盖 source/LLM exhaustion/paused/queued 的保留；非 UTC created_at 绑定后 Task JSON 使用 time.Time 的 RFC3339 明确时区，并解析回相同瞬间。未新增裸 SQLite clock string 的 API 输出。没有启动实际 Agent、模型、MCP、通知、server 后台进程，没有提取凭据，没有访问 `G:\ARTEX-0.3.15`。

## TDD RED / GREEN

以下命令 cwd 均为本独立工作树，使用 `-count=1`，双库命令设置上述显式 PG env。

1. RED `go test ./db -run '^TestPortableTaskContract/SQLite/(CategoryCRUD|TemplateCRUD|Metadata|LLMChain|BulkContext)$' -count=1 -timeout=60s`：分类 `near "INSERT": syntax error`，模板 `missing named argument "3::text"`，pin `missing named argument "2::boolean"`。旧 metadata 测试 defer 解引用被错误覆盖为 nil 的 DTO；改为提前捕获 ID，使清理不掩盖产品失败。修复上述显式方言后相应组转 GREEN。
2. RED `go test ./db -run '^TestPortableTaskContract/SQLite/(LLMChain|BulkContext|CompanySnapshot)$' -count=1 -timeout=60s`：quota `near "FOR"`，公司路径裸 []int64 bind `unsupported type []int64`；旧公司 fixture 的 ARRAY[$3] 也暴露 PG-only SQL，改为两库 fixture 字面量。之后 GREEN `.../SQLite/(LLMChain|BulkContext|CompanySnapshot|CategoryBatch|CategoryBatchReject|TemplateConcurrentPatch|TerminalAndDuplicateChain|Queue|TaskOrder)$`，2.676s。
3. RED 新首次运行/拦截组：StampFirstRun `near ">": syntax error`；拦截时间 DTO 与最终落库时间不一致。测试初始 kind="domain" 是 fixture 写错，不作产品 RED 证据，改为既有 CHECK 的 exact_domain/exact_ip 后真实时间反例成立。首次 SQLite 修复后相应 6 组 GREEN 1.653s；PG 原样断言 8 组 GREEN 7.491s。
4. timestamp 自审 RED `go test ./db -run '^TestPortableTaskContract/SQLite/(NullableTemplateAndTimestamps|Intercept)$' -count=20 -timeout=60s`：最初显式 now 的 patch 仍两次失败；template 返回 18:23:35.4495034，落库 18:23:35.4500236；category 返回 18:23:39.9448282，落库 18:23:39.9454116。此证据推翻单次初轮 suite 的时间正确性结论。改为同 tx 读取最终 timestamp 后 `.../(NullableTemplateAndTimestamps|Intercept|TemplateConcurrentPatch)$ -count=20` GREEN 6.117s。
5. scoped intercept RED `go test ./db -run '^TestPortableTaskContract/SQLite/ScopedIntercept$' -count=1 -timeout=20s`：在外层 tx 写 note='uncommitted'，scoped List 返回 'original'。改为全部拦截操作 tx precedence，并只提交自建 tx；双库 `.../(ScopedIntercept|Intercept|NullableTemplateAndTimestamps|TemplateConcurrentPatch)$` GREEN 4.141s。
6. PG 分类 count RED `go test ./db -run '^TestPortableTaskContract/PostgreSQL/CategoryCRUD$' -count=1 -timeout=30s`：assignment 返回 TaskCount:0 而期待 1，3.161s。改为两库同 tx UPDATE 后查询；双库 `.../(CategoryCRUD|CategoryErrors|CategoryBatch|CategoryBatchReject)$` GREEN 5.729s。

首次工厂 bootstrap 脚本使用本机 Python 3.9 不支持的 write_text(newline=...) 导致旧测试未接入工厂，此时的 skip/pass 不是 TDD 或 gate 证据；已立即修正为显式 open(newline=...)，上述 RED 均在实际 SQLite fixture 执行。中间 suite 及必要复测仅为定位/修复；最终 gate 以最后代码版本为准。

## 文件、自审与后续接缝

生产白名单：db/tasks.go、task_context.go、task_categories.go、task_templates.go、task_intercept.go。

测试白名单：db/tasks_portable_test.go、tasks_test.go、task_categories_test.go、task_templates_test.go、task_metadata_test.go、task_queue_test.go、task_list_performance_test.go。现有 PG tests 仅增加 explicit factory 接缝；公司 fixture 做明确方言选择，metadata nil 清理和分类返回 count 断言窄修正。不改其它人的 implementation/verification.md。

自审：所有新增 SQL 都是局部显式分支/共享兼容语句；无全局 SQL 替换、伪锁、float64 JSON round-trip。bulk hydration source 中只有三次查询；本项没有大规模 query-count instrumentation/performance benchmark。schema/UTC binder/UDF 未改。两个真实自审发现（AFTER timestamp 与 PG statement snapshot count）已修正并有覆盖证据；并发测试错误使用 goroutine 局部变量和 channel 同步，避免共享 err 竞态。

3b 待接缝：完整 exploration/activity/search、node/edge/anchor、token daily/metrics/session、intent 控制/terminal-only inherited traces。3c 待接缝：Attach/Detach/Register scopes、两套不同 contextCoverage/scopeTarget、完整删除/prepare/shared ownership/orphan 与 16 worker claim 并发整合；原来的 assets/anchors 表锁与删除 SQL仍在对应 PG 路径，不能称 SQLite 全部可运行。server stale GraphOverview 的 list_companies 验证也仍在后续整合范围。Task4b/4c/5/6 runtime/UI 未修改。

## 最终 gate

最终代码全部完成后执行：

```powershell
$env:ARTEX_TASK_TEST_PG_DSN='postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_task3?sslmode=disable'
$env:ARTEX_PG_DSN=$env:ARTEX_TASK_TEST_PG_DSN
go test ./db -run '^(TestPortableTaskContract|TestTaskLLMProfileMutationsLockTaskBeforeProfile|TestNormalizeTaskCompanyIDs|TestCreateTaskRejectsTooManySourcesBeforeOpeningTransaction|TestCreateTaskRejectsTooManyCompaniesBeforeOpeningTransaction)$' -count=1 -timeout=180s -json > .superpowers/sdd/portable-v0.0.1/task-3a-final-suite.jsonl
go test ./... -run '^$' -count=1 > .superpowers/sdd/portable-v0.0.1/task-3a-compile.log
go vet ./db
git diff --check
```

最终双库 suite exit 0，`ok github.com/Autumn-27/artex/db 20.540s`。SQLite **23 case pass**，PostgreSQL **23 case pass**；加上父组、源数/公司数边界和 PG lock observation，共 **53 test pass 事件、0 fail、0 skip**。SQLite 组 2.16s，PG 组 17.03s，PG 原真实锁观测 PASS 0.18s。JSONL 最后 package pass 已核对，不统计不完整输出。

全包编译入口 `go test ./... -run '^$' -count=1` exit 0（各包 no tests to run），这是编译 gate，不宣称全项目行为 suite 通过。`go vet ./db` 和最终 diffcheck 均 exit 0、无输出。没有执行本项业务 race 检测或任何五平台原生运行验收，不借用其它项的 race 结果作 3a 证据。

两轮中间 suite 日志另存 `task-3a-suite-before-timestamp-fix.jsonl`、`task-3a-suite-before-category-count-fix.jsonl`，保留自审前的历史，不能替代最后 gate。所有生成日志只留工作树供复核，不 stage。最终只 stage 上述自改 12 个 Go 文件和此报告，明确排除主代理的 verification.md；提交 SHA 由交接回复给出，避免报告自引用。提交标题 `feat(db): port task configuration and context contracts to sqlite`。
