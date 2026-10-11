# Task 4a 实施报告

范围：`db/assets.go`、`asset_dsl.go`、`companies.go`、SQLite 专用 helper/UDF、资产测试工厂；其他文件仅必要锁参数与来源读取接缝。未访问既有安装/模型密钥，未启动模型、MCP 或通知；未实现 Task3 graph/coverage、finding、archive runtime。

所有命令 cwd 为 `C:\Users\cy\.codex\worktrees\portable-v0-0-1\Hack3rX_2026_011_ARTEX`。测试 PostgreSQL 使用显式 `ARTEX_ASSET_TEST_PG_DSN=postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_assets?sslmode=disable`。工厂在 reset 前执行 `SELECT current_database()`，不是 `vh_assets` 则 Fatal；逐 case 重建专用 public。SQLite 文件全部 `t.TempDir()`，所有打开错误 Fatal；两种数据库复用相同业务断言。

## RED 与阶段验证

1. 初始 `go test ./db -run '^TestPortableAssetContract$' -count=1 -timeout=90s`：SQLite RootDomain FAIL `no such function: pg_advisory_xact_lock`；PG 同断言 PASS；包 FAIL 4.042s。
2. 扩展既有 40 项断言后 `go test ./db -run '^TestPortableAssetContract/SQLite' -count=1 -timeout=120s`：SQLite 数组长度旧测试 SQL、Append、查询 cast、ANY/delete 等 FAIL；公司组已运行。完成显式 SQL/查询/scanner 接缝后同命令 PASS，包 5.555s。
3. 新 JSON/DSL/scope/删除候选/回滚契约首次执行：SQLite DSL `unrecognized token ':'`、scope `near '='`、hosts `no such function: ANY`；PG DSL 的测试表达式误写 `||`，修正为既有语法 `OR`。
4. `go test ./db -run '^TestPortableAssetContract/(SQLite|PostgreSQL)/(HostSideEffectFailureRollback|DSL|PeerScopeMutation)$' -count=1 -timeout=60s`：SQLite FAIL `host side effect failure was swallowed`，PG 回滚与 SQLite peer 阻塞通过。原 DSL 容错接受 `title=` 为 bare text，测试改用真实非法 numeric `port>=bad`。
5. 修复 nested upsert/linkHostAssets 错误传播后 `go test ./db -run '^TestPortableAssetContract/SQLite/(HostSideEffectFailureRollback|DSL)$' -count=1 -timeout=60s`：PASS，包 1.837s。

6. `go test ./db -run '^TestPortableAssetContract/SQLite/AttributionPrecedenceAndWarnings$' -count=1 -timeout=30s`：新增两列 ICP 同时匹配、非匹配 domain/IP 存在的回归，RED `dual ICP tie-break`；改为一次加载规则和资产、关闭 cursor 后 Go 匹配，再逐行 tx UPDATE，GREEN 1.422s。另补删除公司后 unmatched explicit orphan provenance 的回归，RED `unmatched explicit provenance changed: "scope"`；仅 scope-derived 或确实匹配 fallback 的行重写 source 后 GREEN 1.434s。
7. 真实 PG 只读 oracle：`jsonb {n:1}={n:1.0}` 为 true；原 `DISTINCT ON(elem::text)` 计数 2、`DISTINCT elem` 计数 1，psql 输出 `t|2|1`。双库 `AuthNumericSemanticDedup` RED（HTTP 两份 numeric 等价对象）；PG 改 `DISTINCT ON(elem)`，SQLite 使用 `big.Rat` 无损值语义 key，HTTP/other 两条链 GREEN。不是启动时重写历史数据。
8. 双库 `ArrayEscaping` RED：PG 原 text[] encoder 把 `a"b\c_%` 变为 `a"bc_%`；补 backslash escaping，GREEN 2.071s。
9. `JSONPrecision` 的 params HTML 边界 RED：SQLite 将 `<` HTML escape 后误选，PG 选 `[`；修正真实 jsonb 文本引号编码。U+2028 与 z 边界确认 PG 测试库 `en_US.utf8` 的隐式排序选 z。主代理明确将原 `(location,name)` 身份与 `text DESC` tie-break 解释为显式跨库 `elem::text COLLATE "C" DESC`；SQLite 使用相同 canonical JSON 文本字节序，Unicode/HTML/键长排序/大整数回归 GREEN（JSONPrecision+AuthNumericSemanticDedup 合组 3.151s）。不使用 English 近似排序。
10. DSL 顶层 OR 回归：临时恢复原无括号 predicate，`CountDSL root_domain=="dsl.example" OR title=="missing"` RED 3 行，期望 service 2 行；立即恢复当前实现（finally），证明独立 type/task/scope predicate 的外层括号能防泄漏，随后双库 DSL/scope/stable pagination GREEN。原 RED 日志 `task-4a-or-red.log`。
11. `ScopeReadDuringWrite` RED：同 DB 持有 immediate writer 后纯 scope count CTE 等 writer，1 秒内 FAIL `pure scope CTE waited for the writer pool`。新增明确 pure-read pool 接缝，不改变 DB 的保守 WITH 路由；GREEN 1.768s，同时验证无 tx 看到旧 committed scope、有 scoped tx 看到本事务新 endpoint、commit 后看到新值。

最终全项命令、测试数和提交前 gate 见末尾。中间完整双库进度 run 101 个 pass 事件（含组/包），无 fail/skip；首次最终 run 的 54/54 双库契约也通过，但之后发现纯读取 CTE 和 explicit orphan 两个具体风险，因此在对应 RED/GREEN 后重新运行最终 suite。这些阶段结果不替代末尾最终验证。

## 已实现的接口约定

- `AssetStore.selectCols()` 显式选择 PG/SQLite 宽 SELECT；复用 Task2 `JSONScanner`，对象/数组 decode 使用 `UseNumber`。`taskMembership` 提供真实 JSON 成员 predicate，`scopeTargetSQL()` 为声明范围 predicate（SQLite 单独常量）。`dslPredicate` 被普通/scoped count 和 query 共同使用；新增 `CountDSLInScope`，在分页前统计与 `QueryDSLInScope` 同一集合。
- `sqliteScopeTargetCTE`：当前 live task + 一层 direct live sources；company/root/subdomain/IP-CIDR（包括 IP-literal domain）/exact normalized ICP；keyword 不纳入；不含 anchor/fact 的并集。Task3 的 contextCoverage 必须另加 anchors 和 tested，不能将它替代 DSL scopeTarget。
- 锁、scope insert/recompute/resolve helper 接受显式可选 `*DB` dialect；缺省保留 PG 真 advisory 行为。资产/公司调用全部传 owning DB；`insertTaskCompanies` 及 `RegisterTaskAssetScopes` 仅必要参数接缝传 dialect，任务 SQL 全适配仍留 Task3。
- `hostsForTaskDeletion(q,taskID,explorationID,dialect ...*DB)`：AssetStore API 和 `DeleteTaskCascadePrepared` 的 prepare 调用已传 owning DB。唯一仍待 Task4c 适配的生产调用为 `db/task_archives.go` 的 `(*DB).snapshotTaskArchive` → `hostsForTaskDeletion(tx,taskID,expID)`；必须改传 `d`，且继续使用归档 snapshot 的同一只读 tx。它保留 task_ids 与 anchors 归属并集，以及其他 live task 的 host 保护。本项未实现归档 SQL、快照或恢复。
- `DB.readPool()` 是包内纯读接缝，SQLite 返回独立 reader，PG 返回原 pool。已知纯读取 CTE 可用它，不依赖 DB.Query 的 WITH 保守分类；Task3 可复用。`AssetStore.readQuery/readQueryRow` 有 s.tx 时用同一 tx，否则显式 reader；DSL count/query、GetByIDsInScope、无事务的 HostsForTaskDeletion 已采用。`query/queryRow/exec` 保留有 tx 时复用、无 tx 时按 DB 的默认路由；DeleteByHost RETURNING 不误走只读池。Task3 snapshot/coverage 若已有 tx 必须继续该 tx，不能用外部 reader 代替它的状态视图。

## 行为与边界

- 八个资产 upsert 使用独立、显式 SQLite SQL 字面量，保持自然键/表达式与 partial indexes、字段 COALESCE、explicit provenance。没有全局 runtime SQL 字符串替换。
- `portable_asset_array_merge` 真实 UDF 保持 JSON 数组：task IDs/string 集合、auth JSON 对象的规范化文本去重、ports 非空 service 优先、params `(location,name)` + PG jsonb text DESC tie-break。`portable_json_overlay` 保持顶层覆盖与 null 键。对象 decode/reencode不经过 float64。
- AppendIPPort/AppendIPBoundDomain/DeleteByTaskID 复用 scoped tx，外层调用开立即事务；嵌套 upsert/host side effect 错误向外传播，整笔回滚。
- PG advisory lock 与全部 natural-key target 保留。SQLite scope/公司创建更新删除使用立即事务；UpsertCompany 用 INSERT DO NOTHING RETURNING 区分 created，再 UPDATE，未用 RowsAffected 判断 upsert 插入。

## 完整覆盖清单与 PG 调整范围

- root_domain/IP/subdomain（包括 A/AAAA、副作用 root 与 bound IP）/app bundle 与无 bundle 分支/HTTP 与 other service/endpoint：自然键稳定、字段合并、host/IP副作用、task_ids 来源、tech/auth/params/open_ports/record_value/extra 均有共同契约；非法 IP 拒绝及 warning 均保留。
- JSON：真实数组与嵌套对象/null；预先写入不同键顺序的 auth 原始 JSON，再 upsert 等价对象；numeric 1/1.0 与嵌套大数 .0 值语义去重；9007199254740993/9007199254740995 task IDs 和 auth/params/extra 大整数无 float64 round-trip；extra 顶层覆盖保留左右 null；ports 优先非空 service；params 同 key 合并、不同 location 独立，键长排序/HTML/Unicode tie-break 真实 PG 对照。
- 公司：Upsert created、logo/规范名、Get/GetByName/List/Scope空数组、scope add/update/create/delete、最长 IPv4/IPv6 prefix、domain 优先、ICP 精确规范化、最小 ID 平局、explicit 不覆盖与 orphan fallback、keyword 不归属、非法 IP warning、替换与创建注入失败完整回滚；PG-only advisory 观测保留，SQLite 新 peer 立即事务阻塞与提交后最新归属。
- DSL/查询：同 count/query/scoped predicate；空全文、Unicode模糊、array member/non-member、quote/percent/underscore、numeric错误、独立 task/type 与顶层 OR、正负 offset/limit、相同 last_seen 下 id DESC 稳定分页、unlimited offset、ByType/ByCompany/ByTask/GetByIDs/CountsByType/CountsByTypeForTask/HostsByTask。
- declared scope：当前及一层 direct live source，排除间接 source；root-domain 成员包含其他任务产生的 subdomain/service/endpoint；网络包括 IP-literal domain；IPv6/normalized ICP；GetByIDsInScope 排除跨范围 ID，先过滤后分页，task<=0 global fallback。纯 CTE reader 与 scoped immediate tx 状态视图也测试。
- 删除：DeleteByTaskID 专属删除 + shared只脱关联，同一 tx；在 detach 步骤注入错误，专属 DELETE 也回滚；task_asset_links trigger 自动关联与移除、其他 task provenance 保留，QueryByTask/DSL hydrate source/summary/node；DeleteByHost/CompanyID/IDs、空 IDs；删除/归档 host候选包含 anchor-only、排除其他 live task 的共享 host。

PG 的窄修正（均未在 startup 批量重写数据）：auth 从 text identity 改 jsonb value identity；params 最后 tie-break 从隐式 locale 改明确 C；text[] 增加 backslash escaping；subdomain A/AAAA 先使用其 IP 再 fallback ICP；host 副作用错误不吞；DeleteByTaskID 两步变为一个受 company scope lock 保护的事务；DSL 组合 predicate 外层括号防 OR 越界。其他 PG 网络操作、锁、自然键、统计和查询排序保持原路径。主代理已确认 C-collation 窄兼容调整在本项授权范围内。

## 自审与风险/后续范围

- 自审逐项核对 brief 与实际 diff：没有 runtime SQL 全局字符串替换、伪 lock、float64 JSON reencode；每个 scoped append/upsert/resolve/来源查询读写继续用原生 tx；公司批量读 rows 关闭后写；scope 纯读不抢 writer；PG 独立真实锁没有删掉。新增 UDF 在两个 SQLite pool 中真实注册，未增加另一套全局方言配置。
- 两个旧 PG 测试靠 text 中 NUL 强制错误，SQLite允许该字符，不能作为跨库错误模型；双库契约用真实 abort/RAISE 触发器替代，验证同一失败后的事务和 company/asset状态。PG-only锁观测只在 PG 子组执行；SQLite从不Skip。
- Go 归属重算批量加载规则与 assets，计算约为 O(资产数×规则数)，每行 tx UPDATE；本项验证功能与事务完整性，未作大规模生产数据性能 benchmark。JSON集合顺序不作为去重契约；选中的 auth 数值表示可能为 1 或 1.0，值和精度必须一致。
- SQLite last_seen 使用 UTC bind/now 的 TEXT显式 CAST；PG保留原::text。时间排序与 UTC绑定有回归，未要求两驱动时间字符串小数位/时区后缀逐字相同。
- params 的 C 规则使部署 locale 不影响 duplicate value 选择；已有数据仅在后续 upsert 时按新规则合并，未执行迁移改写。历史 SQLite不合法 JSON由schema约束拒绝，decode错误向外返回；极端超过 PG numeric 可表示范围的 JSON数字未做额外跨库范围校验。
- finding/archive/Task3 graph、coverage与完整 task mutation API、runtime、真实模型 E2E、全项目suite/业务race仍由后续任务负责。本项用最小真实task/exploration表fixture隔离这些未适配依赖，不声称它们完成。未启动任何应用/模型/MCP/通知。
- 只提交本项 13 个 Go 文件和此报告；不 stage `implementation/verification.md` 或其他人的文件。基线 `2969c08`，分支 `codex/portable-v0.0.1`。

## 最终验证与提交 gate

最终完整 suite（所有代码修改完成后；exit 0）：

```powershell
$env:ARTEX_ASSET_TEST_PG_DSN='postgres://vh_test:vh-portable-test-only@127.0.0.1:27347/vh_assets?sslmode=disable'
go test ./db -run '^(TestPortableAssetContract|TestParseScopeLine|TestParseAutoScopeLine|TestParseStructuredCompanyScope|TestCalcCSegment|TestMarshalStringArray|TestNormalizeURL|TestCompanyScopeMutationLockDoesNotReuseInfrastructureLocks)$' -count=1 -timeout=180s -json > '.superpowers/sdd/portable-v0.0.1/task-4a-final-suite.jsonl'
```

决定性输出：`ok github.com/Autumn-27/artex/db 72.467s`。JSON统计为 SQLite **55** case pass（含 peer 与 write期间reader 专项），PostgreSQL **54** case pass（含真实 advisory lock 专项）；整个命令 **144 test pass 事件**（含子组/纯解析子测试），另有 package pass；**0 fail、0 skip**。SQLite组5.87s，PG组65.37s，Contract总组71.24s。最后一轮也包含 dual ICP、A record IP优先、公司删除后 unmatched explicit orphan、auth numeric equality 与 Unicode C tie-break。

`go vet ./db`：exit 0，无输出。`git diff --check`：exit 0，无输出。曾在临时恢复 OR 原实现时 Python写入CRLF导致 diffcheck报告行尾 whitespace；对该文件重新 gofmt 后已修正，最终 check 无告警。自审完成；最终 staged 文件名单再次逐项检查，明确排除他人的 verification 文件。

最终 JSONL、初次最终 run 的 `task-4a-final-suite-before-readseam.jsonl`、读池修复后但 explicit orphan修复前的 `task-4a-final-suite-before-explicit-orphan.jsonl`、中间 `task-4a-progress.jsonl` 和 OR RED 日志均只保留本工作树作复核证据，不额外stage生成日志。所有 SQLite文件由测试临时目录管理，测试库为本次专用 `vh_assets`，不修改其它库。

本报告随本项提交；提交SHA由交接回复和 `git log -1` 给出，避免报告自引用SHA。提交信息：`feat(db): port asset and company scope contracts to sqlite`。

## Fix Round 1 补充交接

统一审查 `task-4a-review.md` 的 I1–I4 与 Minor 已按真实双库反例修复，详细报告 `task-4a-fix-1-report.md`。本补充保留初轮历史，但**取代初轮对无时区后缀仅属格式差异的判断**：裸UTC字符串会改变客户端的瞬间解释，SQLite资产last_seen及公司created_at/updated_at现在明确输出RFC3339 UTC `Z`，非UTC bind→API JSON字符串→带时区解析回同一瞬间的回归覆盖Get/GetByName/List。

numeric canonical text现在去整数前导零/负零而保留fractional scale，覆盖指数、0/0.0、1.20e-2、大整数和精确小数及真实params merge；params身份按原PG `->>` nullable text提取，覆盖number/string/bool/array/object/null/missing两维key；公司matcher与declared-scope网络UDF均使用netip保留family，不Unmap，mapped IPv6不会匹配IPv4规则、可以匹配family6 mapped-prefix。PG身份、SQL与shared scope normalization本轮未改。六处嵌套d.SQL已压平。

原calcCSegment对mapped UpsertIP另有前置CIDR限制，经PG只读确认并如实记录，协调者指示本轮不扩大修复；mapped回归采用合法直接seed资产/网络，未声称该旧UpsertIP全路径成功。聚焦新反例组GREEN 7.515s，0fail/skip；原契约+新反例的必要最终统一suite与gate结果见单独fix报告。

Fix1最终统一suite **PASS 117.942s**：原SQLite55/PG54 case，新反例每库7组及9项numeric子测试（各16 case/subcase），总179 test pass事件，0fail/skip；`go vet ./db` 与 diffcheck均exit0。I1–I4无已知未解决项；stage只包含本轮6个Go文件及两份报告，未包含他人的verification。
