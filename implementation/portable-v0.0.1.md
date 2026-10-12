# VulnHarvest v0.0.1 Implementation Plan

用户已批准实施，完整产品要求见本次会话中的 SQLite 便携运行方案。此文件记录分项交付和接缝，验收结果另存 verification.md。

## Global Constraints

- 禁止访问或修改 G:\ARTEX-0.3.15；只用独立测试目录、端口、数据库、容器和卷。
- 两种启动模式 portable/postgres；CLI > VULNHARVEST_MODE > config mode > postgres。
- portable 不连接 PostgreSQL，不自动安装工具，不自动续跑任务/复测/后台对话/通知/定时任务。
- SQLite 使用 modernc.org/sqlite，独立版本 schema、外键、WAL、每连接 busy timeout，独立读池和单连接写池，立即写事务。
- 不做全局 SQL 字符串替换；共享兼容 SQL，其余查询在业务模块明确选用 SQLite 方言或 Go 等价实现。
- 不丢弃 Agent 提示词、配置、模型 Key、任务上下文、技能、文件、关联与历史；初始化只补缺。
- state 在网页工作区以外；搬迁时退出后复制整个目录；单实例锁、迁移前备份、拒绝未来 schema。
- 便携 HTTP 与流量代理保留已获取的 listener，动态端口，同源 API/SSE，实际代理地址传入 Agent/MCP。
- 产品名 VulnHarvest v0.0.1，保留内部 module/env/artex 命令；停用上游换装及镜像推送。
- 不包含同步、历史 PostgreSQL 导入、同步页面。
- 五平台构建不等于五平台原生运行验收；真实模型测试没有独立授权 Key 时须明确记录未验证。

### Task 1: 模式与 SQLite 基础

Files: config/config.go, config/mode_test.go, db/db.go, db/sqlite.go, db/sqlite_schema.sql, db/sqlite_test.go, db/errors.go.

Interfaces: config.ResolveMode(cli string) (string,error), db.OpenSQLite(path string) (*DB,error), (*DB).IsSQLite() bool, (*DB).SQL(postgres,sqlite string) string.

- [x] 先测试 CLI/env/file/default、非法模式、便携忽略 PostgreSQL DSN。
- [x] 建立最新完整表结构和索引/触发器。SQLite 数组使用 JSON text，时间列使用 DATETIME，BOOLEAN 支持 bool 扫描。
- [x] 建立 schema version、升级前备份、未来版本拒绝、rollback、Close checkpoint。
- [x] DB 保留嵌入 *sql.DB；覆盖 Exec/Query/QueryRow/Context/Begin/BeginTx，读写路由，无全局 SQL 重写。写池最大连接 1，_txlock=immediate。
- [x] 注册 now/concat_ws/greatest/least 等可共享标量函数；不注册伪造锁函数。
- [x] OpenSQLite 暂只迁移；内置配置 seed 于 Task 2 集成。
- [x] 测试每连接 PRAGMA、读写并发、外键/唯一/忙等待错误分类、重启、备份、未来版本。

### Task 2: 配置和基础 DAO

Files: db/db.go seed, config.go, tools.go, settings.go, conversation.go, logs.go, tool_usage.go, llm_usage.go, llmretry.go, llmhealth.go, skill_usage.go, intercept*.go, notification*.go, triggers.go, commands.go, chat_mentions.go, side_questions.go.

- [x] 为 SQL 选择接缝增加模块测试，完整初始化 seed，不能覆盖自定义提示词和绑定。
- [x] 适配 JSON/数组/日期/ILIKE/统计/通知领取/锁，检查记录日志正文和用量。
- [x] SQL 查询共有参数保持 $N，SQLite 不支持的显式使用 DB.SQL 或独立方法。
- [x] 使用独立 PG 库和 SQLite 验证同一业务契约。

### Task 3: 任务和探索图

Files: db/tasks.go, exploration*.go, digest.go, intent_admission.go, constraints.go, task_context.go, task_assets*.go, task_scope.go, task_categories.go, task_templates.go, task_intercept.go.

- [x] 完整 CRUD、任务关联/上下文与 node/edge/anchor、队列领取、暂停恢复、删除屏障。
- [x] FOR UPDATE/SKIP LOCKED 在立即写事务内以 SQLite 原子条件更新实现；所有关联写入继续同一事务。
- [x] 并发 worker 不重复领取，删除不留孤儿，上下文与 token 记录重启保持。存储分项通过；便携启动时的自动恢复限制仍由 Task5 交付。

### Task 4: 资产、漏洞和归档

Files: db/assets.go, asset_dsl.go, companies.go, company_scope.go, finding*.go, task_archives*.go, server/manager.go, server/engine.go, server/task_archive_package.go 等直接 PG SQL。

- [x] 数组 JSON 合并/去重、网络地址匹配采用 SQLite JSON SQL 或 net/netip Go 实现；保留原归属规则。
- [x] 漏洞/复测/证据绑定/归档快照恢复保全关联及原文件路径；数据库事务原子，文件移动使用日志补偿及恢复。
- [x] 扫描业务层直接 PostgreSQL SQL，逐个适配及错误分类；存储分项已审查，完整应用调用验收仍由 Task 7 完成。

### Task 5: 便携运行时

Files: cmd/artex, config, server/runtime.go, server/server.go, manager.go, auth.go, scheduler/notify/mcp startup; traffic; 项目内 go-mitmproxy 补丁。

- [x] executable 相对 state/data/skills，跨平台进程锁、优雅关闭、端口 listener 自动选择，启动失败不留锁。
- [x] portable 重启暂停未完成任务和对话；禁自动恢复/复测/通知/调度/下载 MCP。
- [x] 工具检测和选择 runtime/bin > 有效配置 > PATH；不执行业务命令、不下载，禁 NORMA rg 自动安装。
- [x] 鉴权 GET /api/runtime 和 POST /api/runtime/check；health 兼容字段与品牌；state 不可从 workspace 下载。

### Task 6: 界面与发行

Files: web settings runtime card, types/api/brand, selfupdate, .github/workflows/release.yml, portable launchers/build scripts, README.

- [x] 运行环境卡片和重新检测，展示模式、地址、工具来源和功能影响，不展示 Key。
- [x] 产品和包名 VulnHarvest v0.0.1；禁上游一键升级和上游镜像推送。
- [x] 五平台可解压目录、启动脚本、config example、技能、license，无 Go/Node/PG 运行依赖。分项构建和 Windows/Linux 启动证据已审查；最终五平台原生业务验收、Windows 临时存储修正及最终包重建仍属于 Task7。

### Task 7: 端到端与审查

- [ ] 独立 PG 全量回归，SQLite 同业务测试，受控 LLM Agent 闭环与能力装配检查。
- [ ] 无开发工具启动，端口占用、中文空格搬迁、重启暂停、双开、崩溃、迁移/磁盘错误。
- [ ] 五平台构建；可得平台实际运行和真实 LLM 独立任务；不足明确记录。
- [ ] 分项审查与最终审查，修复重要问题，记录准确测试结果和包哈希。
