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
- 固定 `d01d7db` 源码在独立 Linux amd64 容器执行 `go test -race -mod=readonly ./db -run '^TestPortableAsset(Contract|ReviewFixContract)$/SQLite' -count=1 -timeout=180s`，72.515s，通过且没有 race 报告。覆盖两组 SQLite 资产契约及真实 peer writer/reader；未选择 PostgreSQL 组，不代表后续任务或全应用 race 验收。

## 任务配置与上下文（分项已审查）

- 提交 `d6a3774` 适配任务配置、公司快照、直接来源、LLM 配置链与修订检查、分类、模板、拦截规则及三次批量上下文查询；完整探索图、范围覆盖度与删除并发仍在后续分项。
- 最终分项双库 suite：53 个测试 pass 事件及 1 个包 pass，0 fail、0 skip，20.540s。主代理独立运行 `TestPortableTaskContract`：50 个 pass 事件（含包），0 fail、0 skip，20.225s。全包编译入口通过，未当作全项目业务回归；独立差异审查通过，另记录并发测试同步确定性的一项非阻断建议，后续并发整合中处理。
- 自审确认 SQLite `AFTER UPDATE` 与 `RETURNING` 的时间戳竞态，相关 DTO 在同一事务读取最终时间戳后返回。另修复 PostgreSQL 分类赋值的计数 CTE 读到更新前快照：清分类再赋回的场景由错误返回 0 修正为实际 1；两库覆盖真实返回值与锁/回滚合同。

## 探索图、活动与意图（分项已审查）

- 提交 `1839ebc` 适配图与活动 JSON、约束/摘要、来源查询、Unicode 搜索、UTC 日桶、令牌与会话统计、意图控制及递归谱系。16 个 worker 跨独立连接领取同一意图的断言实际执行；范围覆盖度、资产绑定及完整任务删除仍待后续分项。
- 最终双库 suite：51 个测试 pass 事件及 1 个包 pass，0 fail、0 skip，20.939s；主代理独立复跑全部 `TestPortableExplorationContract`：52 个 pass 事件（含包），0 fail、0 skip，27.086s。全包编译入口、vet 和 diffcheck 通过；差异审查通过，另记录令牌 metrics 增加其余三维非零断言的非阻断建议。尚未做本项 race 或实际 Agent 验收。
- 两处窄 PostgreSQL 行为修正：来源意图分页与计数补上其他来源读取接口已有的终态限制；软删除意图后，迟到的旁路快照写入返回父节点已失效，不能重建已删除会话。本任务自身未结束意图仍可见，原子领取 SQL 保持。
- 固定 `1839ebc` 在独立 Linux amd64 容器运行 `go test -race -mod=readonly ./db -run '^TestPortable(Task|Exploration)Contract$/SQLite' -count=1 -timeout=240s`，30.880s，通过且无 race 报告；覆盖任务配置与探索图两组 SQLite 契约，包含 16-worker 领取、状态控制及真实 peer 事务。未选择 PostgreSQL 组，也未包含随后未提交的完整删除适配。

## 任务资产、覆盖度与删除（分项已审查）

- 提交 `f7a2524` 适配任务资产绑定、来源、范围及两种覆盖度查询、删除准备回调和共享资产保护。保留原导入用 Upsert 任意标签合同，实际 Agent、HTTP 资产接口及任务种子采用每项同一事务写入资产与必要关联；关联失败回滚该项并返回错误。
- 完整任务分项检查串行执行：DB 151 个测试 pass 事件，138.679s；Agent 21 个，16.600s；Server 25 个，17.220s。另各有 1 个包 pass，均 0 fail、0 skip，合计 171 个叶用例。主代理读取 JSONL 核对结果；全包编译入口、`go vet ./...`、diffcheck 通过，独立差异审查通过。
- PostgreSQL 窄修正包括保留资产时仍移除已删除任务成员关系，以及公司锁、来源任务锁与资产锁的统一顺序。真实 handler 的晚到写入在删除提交后被拒绝，失败注入检验证明资产、来源、范围和锚点不会部分提交。四维令牌统计和事务竞争观测补齐前项审查建议。
- 本项发现 SQLite 原生 `ON DELETE RESTRICT` 可能返回扩展码 1811，当前共享外键分类尚未识别。删除回滚已验证，分类缺口明确交后续漏洞/证据分项集中修复，并要求区别普通触发器错误；不把此缺口标作已解决。
- 固定 `f7a2524` 的独立 Linux amd64 容器实际运行 race：`TestPortableTaskBindingsContract/SQLite`（DB 17.560s），以及 SQLite 真实调用入口 `TestPortableTaskCompanyTools`（Agent 9.580s）、`TestPortableTaskAssetServer`（Server 8.994s），均通过且无竞态报告。首轮因缺少 CGO 编译器未运行测试；补齐临时容器测试依赖后重试成功。未连接 PG、未运行完整 Server 生命周期或模型；主机模块缓存只读。
- 审查留下一项非阻断证据限制：SQLite 的原生忙等待由前置探测证实，真实调用另以连接占用和结果未返回观测；没有声称已观测到该调用内部的忙等待钩子。原子性与删除后的拒绝行为由最终持久状态验证。前项已审查的配置、探索图及底层驱动合同保留，并由本轮完整任务测试再次覆盖。

## 漏洞、证据与复测

- 提交 `02ec796` 适配漏洞查询与资产树、同事务复测快照、证据绑定与版本比较、报告版本、归档证据行及错误分类。快照保留嵌套 JSON、SQL NULL、UTC 时间和超过 2^53 的整数。
- 最终修正后的双库检查：DB 90 个测试 pass 事件，25.055s；Evidence 27 个，34.678s；Server 13 个，14.152s。合计 130 个测试事件和 3 个包通过，0 fail、0 skip，主代理已核对原始 JSONL。先前完整检查曾发现 PostgreSQL 删除竞态，修正后整组重跑通过；不以先前部分成功代替最终结果。
- 原有 HTTP 流量正文、ZIP 导出、复测状态及校验断言通过两库测试；使用只注入存储的实例，完整鉴权、启动生命周期和模型执行仍待后续。全包编译入口、vet、diffcheck 通过；Linux 证据包交叉编译通过，尚不算 Linux 运行验证。
- 窄 PostgreSQL 修正：漏洞记录与节点镜像一起提交或回滚；初始化保留自定义工具绑定及大整数 schema；任务删除与漏洞操作竞争时，以最终漏洞行判定已删除或保留的独立记录。
- SQLite 原生 `RESTRICT` 外键错误 1811 分类已修复，区别普通触发器错误并覆盖包装错误。驱动只提供错误码与文字，用户刻意复用完全相同原生诊断时无法进一步区分，已在共享分类处记录限制。
- Windows 证据正文保留文件 `Sync`，通过平台专用发布操作替代不支持的目录 `Sync`；POSIX 保留重命名和目录同步。新增证据共享事务接口，真实元数据写入后再释放垃圾回收保护；完整归档恢复接入仍由下一分项完成。
- 独立分项审查通过，Critical / Important / Minor 均为 0。审查确认归档完整恢复、完整运行时及模型执行仍属于后续验收，不将共享接口测试扩大为完整产品通过。
- 固定 `02ec796` 的独立 Linux amd64 容器实际运行 `-race`：SQLite Finding / Evidence / FindingServer 契约分别 17.575s / 7.989s / 4.920s，原生错误分类及归档版本规范化另为 1.694s，均通过，无竞态报告，整体退出码 0。仅使用本任务临时源码、容器与缓存，未运行 PG 或完整 Agent。

## 归档恢复

- 提交 `a5810da` 完成显式双库归档查询、类型白名单快照与恢复、流式模型记录、共享资产/ID 重映射及实际服务器共享事务接入。快照使用同一只读事务，进度写入可同时推进；恢复的证据清理保护保持到元数据提交。
- 修正后的最终双库检查：DB 41 个测试通过事件，21.512s；Evidence 8 个，3.520s；Server 30 个，15.053s。合计 79 个事件、68 个叶用例和 3 个包通过，0 fail；Windows 的 `TestTaskArchivePackageSkipsSymlink` 因符号链接权限不足跳过。主代理已读取原始 JSONL 核对。
- 原 PostgreSQL 归档遗漏的主会话、复测会话/活动及任务审批规则，以现有版本的可选附加表补齐；精确关联的原始模型记录、旁路记录、transcript、session 和 noa 文件一起保留。全局模型/Key、提示词、工具等配置继续保留主库，不进入公开归档统计字段。
- 实际服务器测试覆盖数据库行写入及提交失败、文件安装回滚、提交后日志收尾失败的恢复重试、独立连接垃圾回收、会话准入及重复恢复。文件移动是有日志的可恢复流程，不能与数据库共同视作一个原子事务。
- 固定 `a5810da` 的独立 Linux amd64 原生 race：SQLite 归档/边界 DB 12.923s、实际 Server 恢复 11.074s；另文件包/日志/复测 transcript 检查 1.677s，均退出 0 且无竞态报告。符号链接用例另以详细输出证明实际 RUN → PASS，包 1.400s，无测试跳过。此证据未运行完整应用或模型。
- 全包编译入口、vet 和 diffcheck 通过；便携启动、界面、Agent 及全套 PostgreSQL 回归尚未完成。
- 独立审查确认的旁路 raw 记录遗漏由 `a9bca99` 修正：快照与压缩清理共用精确 request ID 关联选择器。新增双库 raw 字节、hash、计数、排除控制与恢复重试验证；修正后的相关检查有 22 个通过事件、18 个叶用例，17.735s，0 fail/skip。固定新提交的 Linux 原生 race 聚焦用例 2.321s，通过且无竞态报告。
- 归档分项及修正范围审查通过，无开放重要问题。两项非阻断建议已记录并分别交后续运行时/测试工厂处理：缩短会话准入锁的请求解码区间、捕获预期故障日志。完整启动恢复顺序和模型能力验收仍由后续分项完成。

## 流量代理运行时（分项已审查）

- 源码提交 `f416cc2` 新增保留 listener 的真实代理入口及关闭收尾，后续报告提交不改变生产/测试源码；完整运行时尚未集成。
- 主代理核对保存的 Windows JSONL：新代理生命周期测试 19 个叶用例（22 个 pass 事件），2.534s；原协议回归 25 个叶用例（39 个事件），4.888s；traffic 全包 47 个叶用例（48 个事件），10.831s。均 0 fail/skip、无损坏 JSON 行。
- 固定该源码的 Linux 嵌套代理 `-race` 检查失败，2.506s：实际 HTTP/2 捕获用例检测到 attacker HTTP server 初始化配置与 `http2.ServeConn` 的并发读写。原始失败证据保留，不以普通协议成功覆盖该失败。
- 同源码的 Linux root traffic 聚焦 `^TestTraffic` race 检查通过，2.217s；只覆盖该命名范围，不计作完整 traffic 或应用生命周期检查。Windows race 运行仍未成功。
- 修正源码 `93f4ddc` 隔离 HTTP/2 服务配置，并为每条已建立的上游 TLS 连接创建一次 HTTP/2 ClientConn，保留共享客户端的八请求冷启动并发用例。中间 `ffadaa4` 虽消除初始化竞态，但该新增场景在 Go 1.27 下仍失败；原失败证据均保留，最终结果不覆盖历史失败。
- 最终修订的 Windows JSONL 已由主代理读取：生命周期 33 叶用例 / 37 事件，3.432s；原协议 25 / 39，4.863s；traffic 全包 47 / 48，6.175s。均 0 fail/skip、无损坏 JSON 行；证据使用 `*-final.jsonl`，区别中间修订日志。
- 固定 `93f4ddc` 的 Linux 原生 `-race`：嵌套代理生命周期及原 HTTP/HTTPS/h2/WS/SSE 协议范围 7.993s，root traffic 全包 12.558s，均退出 0、无竞态报告。临时容器已清理，仅使用本次独立测试文件和只读宿主模块缓存。
- 补丁来源校验通过：104 个上游文件，98 个逐字节未改，6 个改动、2 个新增；原 zip、补丁重放和最终原生测试源码一致。来源文档准确披露早期缺失依赖下载尝试留下的缓存 lock，不把源码未补丁扩大为所有缓存零变化。
- 完整分项审查及修正范围复审通过，I1/M1/M2 均已解决，新增 Critical/Important/Minor 为 0。工具环境、应用总关闭与恢复、界面、发行和模型验收仍待后续完成；本项不等于完整便携运行时通过。

## 工具环境与执行路径（分项已审查）

- 源码 `6178c15` 新增统一检测/执行快照、工具 relay、实际 shell/Grep/Python/MCP 接入、代理/CA 临时覆盖及项目内 norma 补丁。正式启动、runtime API 和应用总退出流程尚待接入。
- 主代理核对 Windows JSONL：根聚焦检查 14 个叶用例、3 个包通过，无失败；原有仅寻找 python3 的 TestExecPython 跳过，新 Python 实际调用用例通过。norma 工具/MCP 聚焦 7 个叶用例、2 个包通过，无失败/跳过。最后已知 MCP 配置路径候选的新增修正另有 3 用例聚焦验证，4.478s；先前完整日志没有该新增第十用例，不混记。
- 完整 norma tool Windows 套件有三个失败，已在原始 zip 的独立基线复现相同失败；不声明完整套件通过，不修改旧断言掩盖问题。应用级资源收尾和全业务回归仍属后续验收。
- 固定 `6178c15` 的 Linux 原生 race：runtimeenv 全包 33.796s（仅 Windows npm 包装器用例按平台跳过）；实际 shell/PTY 两用例 4.056s；MCP 真实握手/关闭三用例 1.208s；server Python/MCP 四项适配 1.862s；搜索无自动安装与 Go 回退另为 1.142s，均退出 0、无竞态报告。测试所需 bash/Python 仅安装于一次性测试容器。
- 两个 SDK 原 zip 来源独立校验通过：norma 210 原文件、205 未改、5 改动、6 新增；代理仍 104 原文件、98 未改、6 改动、2 新增。norma 原 zip 缺 LICENSE 的来源限制如实记录。
- 审查发现的版本探测子进程残留由 `9f6af31` 修复：Windows 专属 Job Object、Unix 专属进程组及输出收尾保证超时/输出超限/取消/父正常退出后停止本次探测后代。四条真实心跳 RED/GREEN 通过，最终 Windows 聚焦 4 叶用例 6.940s；完整检查 14 叶 50.354s，无失败/跳过。完整日志先于仅测试夹具的原子写入强化，强化后已重跑聚焦范围。
- 固定修复的 Linux runtimeenv 全包 race 38.502s 通过，无竞态，四条心跳实际执行，仅 Windows 包装器用例按平台跳过；后续 Windows 启动失败真实 16 次探测检查通过，未观察句柄泄漏。SDK/来源补丁未因这次应用探测修正改变。
- 分项完整审查及修正范围复审通过，I1 已解决，新增 Critical/Important/Minor 为 0。五平台当前仅编译证据，不等于原生运行验收；正式启动和完整应用收尾仍属下一分项。

## 应用运行时（分项已审查）

运行时 Task5b 首版源码 `3f052006` 的固定 Linux amd64 `server ^TestRuntime -race` 为 13.275s、退出 0，无竞态；缺少 Python 与未注入专用 PostgreSQL 密码两项跳过。原审查发现五项重要问题（旁路 transcript 路径、SDK 补丁来源清单、旧格式 journal 双端校验、state/data 重叠、PostgreSQL 模式环境检测），由 `aad0e5d` 修复，修正范围复审已通过，五项及两项次要问题均解决、无新增问题。失败与修正证据均保留，不将原版检查扩大为修订后的通过。

主代理固定 `aad0e5d` 的 Linux 原生修正检查：server 归属/恶意 journal/根重叠/实际旁路归档搬迁/恢复与关闭范围 `-race` 15.070s，CLI 便携真实启动及工具日志、原子 metadata 9.481s，均退出 0、无失败/跳过或竞态报告。CLI 测试 harness 带 `-race`，其构建的应用子进程没有该标志，故这是原生启动证据，不声称子进程已被竞态插桩。norma 与代理原 zip verifier 分别独立退出 0（norma 210 原文件、6 改动、7 新增；代理 104/6/2）。PostgreSQL 的鉴权重检测与实际选中工具目前使用 Windows 独立 `vh_runtime` 检查，尚未由这些 Linux 命令执行。

Windows 最终定向命令保留实际日志：CLI 四个用例全部通过，118.821s，包含真实便携启动、独立 PostgreSQL 启动/鉴权重检测/选中工具执行、日志与原子 metadata；Server Runtime、SQLite 归档及文件包 14.897s，归档修正范围 6.654s。配置、锁/listener、SDK cleanup、compile-only 与 vet 通过；compile-only 不算完整业务测试。运行时分项已交付，前端发行和最终业务验收仍待完成。

## 最终验收

固定首版 `3f052006` 的独立真实模型预验收未完成闭环：`deepseek-v4-flash` 的 Worker 响应耗时 129.922s 超过测试用 75s budget，工具中断，受控目标 GET 为 0。该失败作为测试预算校准依据保留。随后固定修正源码 `aad0e5d` 在独立中文空格路径、新 SQLite 实例和 loopback receipt 目标上完成核心真实链：`deepseek-v4-flash` 实际 GET `/receipt` 1 次，nonce 写入 `record_fact`（fact 4），第二轮 Planner 读取 graph 节点，`prove_goal`（goal 2 / fact 4）成功，任务为 `done`；9 次 LLM 调用、97351 input / 3660 output tokens，重开同一 SQLite 后无自动重放，所有本次进程已停止。严格 verifier 仍为 `pass=false`：goals 阶段没有调用 `set_goals`，goal provenance 为 `system` fallback，而不是 `goals`；因此不能声称 goals 角色工具装配完整通过。报告与验证器在 `live-model-final-report.md`、`live-model-preview-verify.py`，密钥仅在 ignored 测试实例/fixture，未进入代码、包或 CI。

任务/探索图原源码聚焦基线：在专用 `vh_task3` 初始空库执行 `^Test(Task|Exploration|Intent|Constraint)`，21 个 pass 事件、0 fail、0 skip，7.076s；不是完整 DB 回归。

漏洞/复测原源码聚焦基线：固定原始提交 `b183535` 的独立源码归档，在专用 `vh_findings` 初始空库执行 `go test -json ./db -run '^Test.*(Finding|Retest)' -count=1 -timeout=180s`，35 个测试 pass 事件及 1 个包 pass，0 fail、0 skip，9.063s。不包含服务器、磁盘证据或便携适配；后续同业务断言需在两种数据库实际运行。

归档原源码聚焦基线：同一固定 `b183535` 独立源码归档，在专用 `vh_archives` 初始空库执行六项数据库归档测试（JSON 流、格式兼容、完整往返、排队依赖与中断后手动重试），6 个测试 pass 事件及 1 个包 pass，0 fail、0 skip，5.179s。未运行后台归档 Worker 或文件包层，不能代替后续 SQLite 往返与文件一致性验收。

尚未完成。SQLite 全业务、Agent 闭环、文件持久化、运行时、UI、五平台构建与运行、PostgreSQL 回归逐项完成后填写准确证据。

用户提供的独立测试 LLM 配置只用于上述本次新实例，测试密钥仅存于 Git 忽略的测试目录；接口模型列表与 echo 检查是前置检查，不能替代上述实际 Agent 验收及其目标拆解限制。没有从旧实例读取 Key。

## 界面与发行（分项已审查）

生产源码 `d6abe837` 构建五种内嵌前端便携 ZIP，后续 `394aa34` 只调整 smoke 的环境检测请求等待上限，不改变包内容。品牌、环境卡片、手动恢复按钮、四项后端上游更新入口禁用、白名单打包和启动脚本已实施。主代理读取保存日志：Go 包/更新入口定向检查、9 项 Node 测试及 TypeScript 通过；五包均已构建，完整普通业务和四种其他平台原生运行尚未完成。

Windows x64 实际 ZIP smoke 验证占用端口、内嵌资源、鉴权、空 PATH 检测、初始化、锁、冷复制和启动脚本错误传播。仅测试强退，不能视为便携 Windows 控制台优雅关闭已验。已保留 C: 空间不足及 5 秒检测等待失败证据：移动独立测试 scratch 到 G: 并延长 harness 等待后通过，不把这两个操作称作运行时缺陷修复。

Windows 工具 relay 复制完整 40,564,736-byte 程序到每个别名，启动及检测产生 24 个副本 / 973,553,664 bytes；强退后副本保留，冷复制重启累计 40 个 / 1,622,589,440 bytes。正常 ToolManager.Close 窄探针只证明本会话清理成功，旧强退会话未自动回收。此问题已列入后续原生验收缺陷，最终包须验证 transient storage 不再无界增长。PostgreSQL CWD 配置优先级及便携相对技能目录问题也已列入业务回归修复；当前源码比较证据不冒充运行测试。

主代理用固定 `394aa34` smoke harness 与上述 `d6abe837` Linux amd64 ZIP，在隔离 Linux 容器执行原生 smoke，退出 1：端口占用、资源、空 PATH 检测、鉴权、锁和冷搬迁重启步骤已执行，Unix 正常退出后别名数为 0；最后 launcher 在空 PATH 找不到外部 `dirname`，解析到无关 CWD 后无法启动实际 binary。此失败已交分项审查，不能把该次测试记为通过。日志 `controller-linux-task6-394aa34.log`，容器已结束并移除。

修正 `50946715` 只改变两个 Unix launcher 和相应真实进程测试，用 POSIX 参数展开和 shell 内建解析目录。主代理已读取 6 项 absolute/relative/bare RED/GREEN 及固定源码重建 Linux amd64 ZIP 的原生 smoke 日志，均在修正后退出 0；实际二进制经兼容启动脚本传递真实错误 stderr/exit 也通过。原四个 Unix `dist/` 归档含旧脚本，已明确标记过期，后续五个最终包必须重建。分项审查和修复范围复审已通过，Unix launcher 重要问题已解决；三项测试维护建议已分别交后续处理。完整业务回归、其他平台原生验收和 Windows relay 问题仍未完成。
