# Task 3b 实施报告

范围依据 task-3b-brief.md、原 task-3-brief.md 和 storage-mapping.md exploration 部分。工作树 `C:\Users\cy\.codex\worktrees\portable-v0-0-1\Hack3rX_2026_011_ARTEX`，分支 `codex/portable-v0.0.1`，业务前置 base `d6a3774`；主代理文档提交 `430364e` 在本项之前，不归本项业务变化。完整 Task3、runtime/UI/findings/archive 未在此宣称完成。

## 实现和行为

- 图 node/edge/anchor/payload、fact/hint、constraints、round/stamp/content_version、digest/covers 均走真实双库 DAO。原本可共享的 SQL 保持；digest.go、constraints.go 和 intent_admission.go 不需要制造方言分支。新增回归覆盖各模块，而不是仅编译这些文件。Node payload/Activity metadata 扫描使用 Task2 JSONScanner，SQLite bind 使用 jsonValue；嵌套对象/数组/true/null 和 `9007199254740993` 保留真实 JSON 类型与精度，不经过 float64 重编码。
- FindingLineage 的递归种子改为两库可用的 `CAST($2 AS BIGINT)`，两条纯 read CTE 显式走 DB.readPool，第一条 cursor 在第二条查询前 Close。没有把 CancelIntent/SoftDeleteIntent 或已有 tx 的读操作移到 reader。
- Unicode 搜索复用 whereBuilder.like：PG ILIKE，SQLite portable_lower + LIKE + 原生 ESCAPE。node count/page、payload summary/full text、origin、全部五种 trace search 使用同方言 predicate；双库验证 Ä/ä、中文、通配 `%`、转义 `_`，保留数字及 `#id` 定位与 SQL 分页/排序。终态 trace 始终在同一查询中检查 source intent state，planner/main、thinking/usage 和 live source 活动隔离。
- Source 仍只查询当前 live task 的直接 live relations；Task3a 的 public 创建路径最多 8 个来源，不递归。8-source 回归覆盖 grandparent 不可见、source 被标记非 live 后立即退出读取、全局 count/page；原 DirectSources 回归验证 source intent 不进入本地 frontier/claim 且不可跨 task 写入。
- TokenDailyAll 参数化 Go UTC cutoff，两库聚合只计算 result。SQLite 使用现有 portable_time_shift(...,0) 规范成精确 UTC 时间字符串再比较 cutoff，日桶用规范结果前 10 字符；不以 Julian day 浮点进行时间比较，不把默认三位 fractional text 与九位 binder text直接比较。PG 保持 DATE_TRUNC/AT TIME ZONE 的日桶。日期标签仍 date-only，普通 DTO 时间仍 time.Time（JSON 自带显式时区）。非 UTC 输入和 UTC 午夜前一微秒均有双库断言。
- LastActivityAll、SQLite TaskListMetricsAll 扫描原生/aggregate timestamp 后用整数 Unix 秒和纳秒按 PG EXTRACT(EPOCH)::bigint 的语义四舍五入。SQLite metrics 使用同一 SELECT 的 indexed correlated aggregates，PG 保留原 LATERAL SQL；没有多查询 Go 合并导致不同 snapshot。验证空任务、全维度 token、result-only、goal、running intent、finding severity、latest activity、legacy totals/goal counts 和非 UTC输入。单行 PG token schema 是 int4，测试使用两个 int32 上限聚合成 `4294967294`，没有改列结构或人为超范围输入。
- TokenStatsBySession 的 CAST 使用共享 SQL，仍以真实 intent ID/main segment/planner 为身份，205 次跨 reusable work#N 的完整历史不受 activity分页限制。取消 token ledger 维持原 authoritative/fallback/trailing exactly-once rollup 与原 UTC 日桶；SQLite ledger metadata 为 JSON TEXT。
- ClaimIntent 的原单条条件 UPDATE 完全保留。16 workers 使用两个独立 DB handle/SQLite peer，只一人 true；假删除和物理删除后 peer 不能再领。原 CAS 12 contenders 只一人成功。SoftDeleteIntent/CancelIntent 仍在原 tx 中执行；SQLite immediate tx 后去 FOR UPDATE，PG 保留真实行锁。peer 持有未提交 paused 状态时 soft-delete 等待，提交后正确继续；原 pause/resume/cancel/共享子孙/goal/孤儿 finding/edge/anchor 断言都在双库运行。
- 重开连接后 graph、blocked reason、worker token/session/result 保留；ReopenIntent 清理 blocked reason并重新领取，旧 result detail仍在。没有调用真实 Manager/Agent/MCP/通知/模型。

## 两处原 PG 问题的窄修复

1. `ListByKindPageWithSources` 原 source intent page/count 不过滤 terminal：local 1 个 open + source 1 个 done/3 个更新的 open，返回 total=5，并且 newer source live nodes可占据后续页；非分页 ListByKindWithSources/GetNodeWithSources 已过滤。主代理明确授权按原继承合同修复。现在仅 source KindIntent 在 SQL count/page 中复用同 terminal predicate；local open/running 不过滤，先过滤后 LIMIT，保持 id DESC、before 和 hasMore。双库断言 total=2、第一页 local、第二页 source done、搜索 live只返回local。
2. Task2 shared `lockSideParent` 原条件仅 `state<>'stopped'`；SoftDeleteIntent 删除 side session后，迟到 SaveSideSnapshot 仍能重建（两库 err=nil）。主代理明确授权必要共享 helper修复；两方言仅把 intent state条件改为 NOT IN ('stopped','deleted')，仍保留同 tx父锁、PG FOR SHARE 和 ErrSideParentGone。现在 late checkpoint被拒绝且不重建 side session。不扩展 side模块其它功能。

## 工厂与 TDD 证据

测试工厂 `openExplorationTestDB` 仅在 `TestPortableExplorationContract/<engine>/<case>` 使用 Task3a taskContractDB：SQLite TempDir文件，Open失败Fatal；PG必须显式 ARTEX_TASK_TEST_PG_DSN，current_database()严格等于 vh_task3才 reset public，不能推断或使用其它 app默认数据库。既有独立测试调用保持原 PG testDSN 行为。本 suite禁止 parallel跨PG reset。Close由契约factory的Cleanup负责，保证旧source tests的Cleanup不在已关闭DB上运行；旧单独调用保留原 defer ordering。

所有测试 shell均设置：

```powershell
$env:ARTEX_TASK_TEST_PG_DSN='postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_task3?sslmode=disable'
$env:ARTEX_PG_DSN=$env:ARTEX_TASK_TEST_PG_DSN
```

从未访问 `G:\ARTEX-0.3.15`；双库suite的TestMain只指向上述独立开发PG。SQLite业务factory从不读取app config或将DSN伪装成SQLite path。

1. RED `go test ./db -run '^TestPortableExplorationContract/SQLite/(Flow|IntentControls|NodeSearch|SessionTokens|DirectSources|CancelTokens|SoftDelete)$' -count=1 -timeout=60s`：Flow `missing named argument "2::bigint"`；IntentControls/SoftDelete `near "FOR": syntax error`；NodeSearch/SessionTokens `unrecognized token: ":"`；DirectSources `near "ILIKE": syntax error`；CancelTokens `near "AT": syntax error`。实际 SQLite fixtures 全部执行，无 skip。GREEN 分两次focused修复：`.../SQLite/(Flow|IntentControls|NodeSearch|SessionTokens|DirectSources|SoftDelete)$` 初次暴露新增search拼接错误 `a.portable_lower(...)`（非产品RED）；修正为完整column参数后，`.../SQLite/(DirectSources|CancelTokens|HardDelete|DeleteShared|CancelShared|TerminalReads|StateCAS)$` PASS 1.987s。其它相关组由最终suite覆盖。
2. RED `go test ./db -run '^TestPortableExplorationContract/SQLite/(LastActivity|Metrics|GraphJSONSearchDigest|Claim16Workers|MainSegments)$' -count=1 -timeout=60s`：LastActivity EXTRACT `near "FROM"`、Metrics LATERAL `near "SELECT"`；GraphJSONSearchDigest初期hint state=open不符合现有CHECK，改成active，这个fixture错误不算产品RED。实现后focused双库还发现两个测试假设错误：PG单行tokenint4不能bind >2^53，PG time.Time可能携带CST而仍是相同instant；改为两个int32聚合和检查非零时间，不改产品schema或强制DTO Location。`.../(SQLite|PostgreSQL)/(InheritedIntentPages|Metrics|MainSegments|GraphJSONSearchDigest)$` 中后三组随后均通过。
3. RED `go test ./db -run '^TestPortableExplorationContract/(SQLite|PostgreSQL)/(InheritedIntentPages|Metrics|MainSegments|GraphJSONSearchDigest)$' -count=1 -timeout=90s`：InheritedIntentPages两库 `first page ... total=5`。共享terminal predicate修复后 GREEN `go test ./db -run '^TestPortableExplorationContract/(SQLite|PostgreSQL)/InheritedIntentPages$' -count=1 -timeout=30s` PASS 1.838s。
4. 新增深层回归 `.../(SQLite|PostgreSQL)/(DailyBoundary|PeerControlBarrier|Restart)$` PASS 3.825s；`.../(SQLite|PostgreSQL)/(TraceUnicodeSearch|EightDirectSources|DailyBoundary|CancelTokens)$` PASS 4.014s。
5. RED `go test ./db -run '^TestPortableExplorationContract/(SQLite|PostgreSQL)/SideDeletionBarrier$' -count=1 -timeout=30s`：两库 `late side snapshot recreated deleted intent session: <nil>`。窄helper修复后完全相同命令 GREEN PASS 4.296s。
6. 最终自审增加 metrics 空任务、非result row、和 legacy totals一致性，并将 helper terminalOnly参数改成明确bool。focused `go test ./db -run '^TestPortableExplorationContract/(SQLite|PostgreSQL)/(Metrics|InheritedIntentPages)$' -count=1 -timeout=30s` PASS 3.217s。

中间命令用于每段定位，不把测试fixture错误或SQL拼接错误充作缺失功能RED。附加业务回归本身不强求在既有正确PG实现上失败。

## 最终 gate

全部生产/测试修改完成后，本项完整双库suite只执行一次：

```powershell
go test ./db -run '^TestPortableExplorationContract$' -count=1 -timeout=180s -json > .superpowers/sdd/portable-v0.0.1/task-3b-final-suite.jsonl
go test ./... -run '^$' -count=1 > .superpowers/sdd/portable-v0.0.1/task-3b-compile.log
go vet ./db
git diff --check
```

- suite exit 0；`ok github.com/Autumn-27/artex/db 20.939s`。SQLite 24 case pass、PG 24 case pass；加父组共 51 test pass事件、0 fail、0 skip，JSONL最终package pass核对。
- 全包编译入口 exit 0，各包 `[no tests to run]` 或 `[no test files]`，包括 server/agent 等。仅编译gate，不宣称全项目行为suite已跑。
- go vet ./db独立命令 exit 0，无输出；diffcheck无输出（提交前再核对）。未跑 race、性能benchmark、真实runtime或五平台原生运行。generated JSONL/log留工作树作审查证据，不stage。

## 文件白名单、提交与自审

生产：db/exploration.go、db/exploration_sources.go、db/side_questions.go（仅上述共享父状态条件）。

测试：db/exploration_portable_test.go、db/exploration_test.go、db/exploration_sources_test.go、db/exploration_tokens_test.go、db/intent_control_test.go、db/intent_delete_test.go。以及本报告；不stage主代理implementation/verification.md，未改Task3a工厂或其它生产模块。提交标题 `feat(db): port exploration activity and intent contracts to sqlite`，SHA在交接回复给出，避免报告自引用。

自审确认：无全局SQL替换、伪锁、worker身份替代session、JSON双编码、精度丢失或tx逃到readPool；ClaimIntent原子SQL未改。exploration.go原文件已超过2000行，本项只在原方法内做局部兼容，无生产文件重构。SQLite metrics是多次同statement correlated indexed scan，尚无大数据benchmark，这不等于已证明其吞吐与PG LATERAL一致。新的test文件约700行，覆盖广但fixture在同一探索业务工厂，未额外拆分生产层。

3c接缝：资产bindings/contextCoverage/完整task deletion及prepare共享资产屏障仍留3c；scopeTarget与contextCoverage不得合并。这里的intent物理/假删除已经可双库运行，不代表deleteAssets=true的完整任务删除已经完成。source测试现有Cleanup只作清理且不替代3c删除验收。server GraphOverview stale覆盖断言、finding/archive SQL、runtime自动恢复和UI仍未修改。
