# Task 1 — 模式与 SQLite 基础

状态：实施完成；未集成 seed 或业务 DAO 方言。
范围：仅独立工作树和 Go t.TempDir。未访问 G:\ARTEX-0.3.15，未读取旧实例配置，未启动应用/前端。

## 接口与连接契约

- config.ResolveMode(cli string) (string,error)：CLI > VULNHARVEST_MODE > config.mode > postgres；去空白、小写归一；非法有效来源报错；不检查 PostgreSQL DSN。
- db.OpenSQLite(path string) (*DB,error)：仅磁盘路径，modernc SQLite；不读取 PG 配置，不 seed。
- (*DB).IsSQLite() bool；(*DB).SQL(postgres,sqlite string) string。
- 保留嵌入 *sql.DB（SQLite writer），Begin/BeginTx 返回原生 *sql.Tx，业务可继续用 tx.Exec/Query/QueryRow。
- SQLite writer MaxOpenConns=1、_txlock=immediate；独立 reader MaxOpenConns=8、_txlock=deferred、每连接 query_only=ON。
- Exec/ExecContext 始终 writer；普通 SELECT Query/QueryRow 及 Context 走 reader；INSERT/UPDATE/DELETE RETURNING 走 writer。WITH/其他不确定语句保守走 writer，无 SQL 文本替换。
- BeginTx(ReadOnly=true) 使用 reader，含 LevelRepeatableRead 调用；其他事务 writer。测试证明读取快照期间 writer 能提交且原快照不变。
- 每连接 foreign_keys=ON、busy_timeout=5000、recursive_triggers=OFF；WAL。Close 关闭 reader、TRUNCATE checkpoint、关闭 writer，可重复调用；初始化失败关闭所有已建立连接。
- IsUniqueViolation、IsForeignKeyViolation 同时识别 wrapped PG/SQLite 错误；IsBusyError 按 SQLite extended code 基码识别 BUSY/LOCKED。
- 注册 now/concat_ws/greatest/least。无伪造 PG 锁函数；整数 extrema 支持大于 2^53 的 int64。

## 完整 schema 与迁移

- db/sqlite_schema.sql 独立 DDL，由本次测试 PG schema 最终实际元数据生成，不是 CREATE TABLE 片段截取：49 个最终 PG 表、520 最终列，另纳入 llm_records / llm_usage 两个运行时业务表，共 51 个业务表。
- 折叠所有后续 ALTER 的列、NOT NULL、默认、CHECK、FK、唯一约束、索引；JSON/数组是 TEXT（json_valid；PG ARRAY 额外 json_type='array'）；日期 DATETIME、BOOLEAN 原生 bool 扫描。
- GIN/GIST 替换为同列普通文本索引，PG INCLUDE 无 SQLite 对等语义：依赖 JSON/网络成员过滤的 DAO 需明确方言/Go实现，不能声称具备 PG 索引性能。
- updated_at 触发器、assets.task_ids → task_asset_links 同步触发器、删除复测会话前 stop 触发器均有业务行为测试。
- 非主键 BIGSERIAL side_question_requests.ordinal 使用 portable_sequences 持久计数及 AFTER INSERT 触发器，删除最新行后不复用 ordinal。
- PRAGMA user_version=1；未来版本拒绝；所有版本 DDL 在事务内，失败 rollback；已有库升级前 VACUUM INTO 生成可独立恢复 .backup-v<version>-<timestamp> 文件，覆盖 WAL 中已提交状态；backup chmod0600。
- db/sqlite_schema_generate.py 是构建期再生成工具，输入当前 applied PG metadata。运行时不做 SQL 重写。
- db/sqlite_schema_fixture_test.json 固化本次 PG 最终列/NOT NULL/type/index 清单，测试无需正在运行的 PG，可发现遗漏后续 ALTER 的列及索引。

## TDD 与准确验证记录

1. 新 config/mode_test.go 先运行 `go test ./config -run TestResolveModePrecedence -count=1`：FAIL，ResolveMode undefined（新增 API 尚未实现）。实施后 `go test ./config -count=1`：PASS 0.633s。
2. 新 sqlite_test.go 先运行 `go test ./db -run '^TestSQLite' -count=1`：FAIL，OpenSQLite/IsSQLite/SQL/reader/error classifiers undefined。基础实现后同命令 PASS 1.999s。
3. 补 extrema 精度回归 `go test ./db -run '^TestSQLite(Immediate|JSON|Large)' -count=1 -v`：Immediate/JSON PASS，Large FAIL，得到错误的 9007199254740992 / 9007199254740993；实现 int64 直接比较后全集 PASS 2.100s。
4. ReadOnly snapshot 首测 PASS；触发器 fixture 首测因未提供 conversations.agent_key 失败（测试构造错误），补必填字段后 PASS。没有把该 fixture 失败当成生产缺陷。
5. 最终 `go test ./config ./db -run 'TestResolveMode|^TestSQLite|TestApplySchemaRetries' -count=1`：PASS config 0.457s / db 1.944s。
6. `go vet ./config ./db`：PASS（退出码0，无诊断）。`git diff --check`：PASS。
7. race 结果另补于本文末尾。

验证覆盖：模式来源与非法模式、portable 忽略坏 PG DSN、完整最终列/NOT NULL/type/index、时间与 bool 扫描、JSON defaults、触发器、post-ALTER CHECK、乱序/重复 $N（含原生 tx）、read/write 隔离、16并发 writer、立即事务无语句已持锁、busy 等待100ms及分类、context取消、外键/唯一错误、reader 每连接 PRAGMA、writer 强制重建连接 PRAGMA、只读 LevelRepeatableRead 快照、rollback、迁移 backup 可读取、未来版本、失败迁移不留表、重复Close、关闭后读写拒绝、WAL清空、数据库搬迁重开。

## 自审与下一任务接缝

- Task 1 不 seed；Task 2 必须在迁移成功后完成补缺初始化并验证重启不覆盖配置/Key/提示词/绑定。
- SQLite TEXT → *json.RawMessage 实测 unsupported Scan；TEXT → *[]byte 正常。Task 2 各 DAO 应局部 []byte/string 扫描再赋值，或显式 scanner，不能声称目前业务 DAO 已兼容。
- SQLite AFTER INSERT 触发器的 ordinal 分配发生在 INSERT RETURNING 之后。side_questions.go 需 SQLite 分支 INSERT 后同Tx按 id 重读或显式先取序号，否则首次返回 ordinal=0。
- WITH 语句保守路由 writer。业务若持 writer Tx 又调用 d.Query 的 WITH 将等待同一写池，应使用 tx.Query 或明确独立纯 SELECT；普通读取 snapshot 使用 readonly Tx。
- updated_at AFTER 触发器同样会令 UPDATE RETURNING updated_at 看见触发器前值；当前数据库值正确，业务若依赖返回精确更新时间需方言显式 SET updated_at=now()。
- 共享 $N 不需变换：modernc v1.52.0 conn.go 明确按参数 Ordinal 匹配，QueryRow 和原生 tx 的 $2,$1,$2 实测正确。
- 五平台构建/原生运行不在 Task 1 验收范围；本文仅本机 Windows amd64。
- db/task_context_lock_test.go / db/task_delete_concurrency_test.go 两行无名 DB literal 改为命名 DB 字段，避免扩展结构导致已有测试无法编译，经主代理同意。
- 未运行全量应用/前端；独立 PG 全量回归由主代理负责，不将其他业务未适配视为 Task1 测试通过。
`go test -race ./db -run '^TestSQLite' -count=1`：未通过运行验收；编译完成后测试进程退出 `0xc0000139`（Windows DLL entry point 初始化错误类），未输出任何测试断言或 race 报告。当前 gcc 来源 E:\Software\CodeBlocks\MinGW\bin\gcc.exe；未扩大范围排查系统环境。普通测试及 vet 已通过，不能称 race 验证通过。


## Task 1 fix 1 — 未来 WAL 版本拒绝不得 checkpoint

审查复现确认初始化失败 defer d.Close() 仍会执行 wal_checkpoint(TRUNCATE)，因此未来版本虽然被拒绝，主库/WAL字节却已变化。修复仅限失败清理：分别关闭已建立 reader/writer pool，避免调用正常运行期负责 checkpoint 的 DB.Close。成功打开后的 Close 保持 checkpoint 行为。

TDD 回归 TestSQLiteRejectFutureWALWithoutMutation：独立临时 future.sqlite，外部连接保持打开，WAL 模式、wal_autocheckpoint=0、创建未来数据并设置 user_version=999；拒绝前后比较主库与非空 WAL 的 SHA-256，并确认外部连接仍读取完整未来数据/版本。

- 修复前 `go test ./db -run '^TestSQLiteRejectFutureWALWithoutMutation$' -count=1 -v`：FAIL，主库字节改变、WAL字节改变/被checkpoint（1.140s）。
- 修复后同命令：PASS（1.223s）。
- `go test ./config ./db -run 'TestResolveMode|^TestSQLite|TestApplySchemaRetries' -count=1`：PASS config 0.417s / db 2.139s。
- `go vet ./config ./db`：PASS（退出码0，无诊断）。
- 未启动应用、未seed、未访问旧实例；无其他业务改动。
