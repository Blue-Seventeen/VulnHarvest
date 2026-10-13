# Portable configuration capability acceptance

2026-10-13；工作树 `portable-final-acceptance/Hack3rX_2026_011_ARTEX`，分支 `codex/portable-v0.0.1`。首批配置验收补丁仅新增 `server/capability_acceptance_*_test.go` 和本报告；后续 running cold resume 补充包含一个经 RED/GREEN 验证的取消分类修复，详见本文 addendum。未修改既有 controlled test 或代理实现。

五个新增测试均经 authenticated API 写配置，关闭真实 `Server`/`Manager`，使用同一临时 SQLite、data 和 skills 根目录重建生产构造器，再观察真实 Agent 请求或生产工具装配与执行。没有用 mock resolver 替换生产装配，没有直接向业务表灌最终状态；数据库只读用于取得 API 创建的 conversation。所有模型 Key 是 `fake-acceptance-*`，所有 HTTP endpoint 是本次 `httptest.NewServer`，不读取旧部署 Key、默认 CA 或受保护安装目录。

## Requirement → test → evidence → limit

| 需求 | 精确测试 / 源入口 | 可观察证据 | 范围限制 |
| --- | --- | --- | --- |
| 提示词正文、版本、模板变量重开后进入 Agent | `TestPortableReopenedAgentPromptBudgetsAndContext`，`server/capability_acceptance_llm_test.go:92` | API 写入两个 prompt 版本；重开 API 返回 starter + 两个编辑版本；真实 ChatAgent 的每个非摘要请求含当前正文和渲染后的 DataDir，均不含旧正文 | 版本表验证配合真实请求消费；未穷举全部 prompt variables |
| Agent max_turns、wrapup 正文与预算、上下文窗口 | 同上 | 精确 5 个主工具轮 + 2 个 settlement 工具轮；自定义 wrapup 仅从第 6 个实际 Agent 请求出现；第 5 轮提供 20K input token usage，32K 配置触发一次生产 compaction summarizer 请求，后续实际 Agent 请求消费摘要 receipt；合计 1 个 active 请求 + 7 个 bound 请求 + 1 个摘要请求 | 受控 usage 是协议 fixture；未证明任意真实模型 tokenizer 的误差；本测试使用内置 compaction，NOA 单独由既有测试负责 |
| Agent run_seconds 实际预算与收尾 | `TestPortableReopenedAgentWallClockBudget`，`server/capability_acceptance_llm_test.go:189` | 重开后配置 1 秒预算中断生产 `sleep({seconds:4})`；实际模型请求恰好 2 次，第二次包含自定义 wrapup；耗时限定 0.9–3 秒且 conversation API 存有 settlement receipt | 生产契约对工具使用 deadline，并在轮次边界收尾；不会截断正在进行的模型 HTTP 请求 |
| LLM fake key、global active、Agent binding、output/settings、重试、fallback | prompt 测试 + `TestPortableReopenedProfileRetryAndBoundFallback`，`server/capability_acceptance_llm_test.go:234` | global Agent 实际请求 active-profile；Agent binding 覆盖 conversation pin；真实 HTTP 校验各 profile Authorization、非流式、417 `max_completion_tokens`、无 `max_tokens`、thinking、reasoning、session header；API 不泄露完整 fake key；重开后 503 恰好一次 SDK retry 得成功 receipt；再重开后 bound head 的 401 恰好一次 pre-output pool fallback 到 backup，使用 backup fake key并持久化真实输出 | 每个失败层只测一个代表案例；不再复制 task chain / mid-stream retry / replay 全矩阵；无真实远程 Key |
| 工具 schema、description、default、绑定、禁用与脚本执行输入 | `TestPortableReopenedToolsScriptsAndFilesystemSkills`，`server/capability_acceptance_tools_test.go:22` | API 修改 custom HTTP schema/default；重开后的生产 `AugmentTools` 与 `SearchExtraTools` 返回编辑后的 description/default；`ExecuteExtraTool` 空参数通过生产 decorator 注入默认值并实际 POST 到本地 receipt；disabled/unbound 工具不装配、不广告；生产 script executor 真正启动子进程，子进程确认临时 `.py` 内容、JSON stdin、TOOL_VALUE、session env、UTF-8 env、CWD；临时 `.py` 被清理 | 受控解释器是当前 Go test executable，验证 **fake interpreter launch/input/cleanup contract，非 Python 语言语义**；无 Python/Node 依赖；工具装配使用共同的生产 ToolResolve/decorator，未穷举所有 built-in handlers |
| filesystem Skill 正文、references、visibility | 同上 | API 创建 visible/hidden Skill 并写 reference；重开后 Skill listing 仅含 visible；hidden invoke 报错且无指导消息；visible invoke 的 Extra 独立 guidance 含实际正文、插值后的技能根目录和 caller context；生产 Read 读取持久化 reference；调用 meter 落入 usage API；无绑定 Agent 没有 Skill | 根目录按应用 bootstrap 前提预创建；不测试 Markdown 语义理解；reference suffix 保持 Skill 原有 `/`，由生产文件工具处理 Windows 路径 |
| MCP HTTP、stdio argv/env、Skill gate、visibility、disable | `TestPortableReopenedMCPHTTPStdioAndSkillGate`，`server/capability_acceptance_mcp_test.go:24` | 生产 HTTP JSON-RPC initialize → notification → tools/list/call，校验配置 header、session header；fixture 容许 Close 发出的 DELETE 并校验其 header/session 格式，但未断言 DELETE 次数；stdio 真正启动当前测试 executable，收到含空格/中文/引号/美元号的原始 argv 与指定 env；两次 handshake/list + 1 次 call 的 7 个子进程事件准确；Skill load 前 gated HTTP/stdio call 均被生产 ExecuteExtraTool 阻挡且没有传输 call，load 后都产生真实 receipt；无可见性 Agent 不连接；禁用后重开 direct MCP 不再连接/广告，disabled Skill-referenced MCP 从未连接；发现 cache 经 API 保留 | 仅 Streamable HTTP 与 stdio，legacy SSE 由 mcphttp 既有 tests 覆盖；child 仅记录本次命名 env/sentinels/显式 argv，不 dump ambient env；此 test 不复测 runtime tool discovery/launch overlay |

## Executed validation

Windows amd64 当前工作树：

```text
go test ./server -run '^TestPortableReopened' -count=1 -timeout 120s -v
PASS — all 5 tests, server 7.500s

go test ./server -run '^(TestPortableReopened.*|TestControlledAgentCompleteAndStream)$' -count=1 -timeout 120s -v
PASS — all 5 new tests + existing controlled Complete and SSE subtests, server 12.529s

git diff --check
exit 0
```

既有 `TestControlledAgentCompleteAndStream` 在组合运行中实际复核了 complete/SSE 两种协议的 11-call planner → worker native HTTP receipt → fact → 第二轮 planner graph/node-detail → goal proof/done，以及真实构造器重开后无模型/receipt 重放。没有新增旁路目标编排。

首批配置测试开发阶段出现并纠正了 fixture 假设：Skill create 的正确 HTTP status 为 201、技能根目录前提、MCP Close 的 DELETE，以及 MaxDuration 不取消模型请求的既有契约。该批配置测试未发现需要生产修复的适配 bug；后续 running cold resume 测试发现的 shutdown 缺陷及 RED/fix/GREEN 另见 addendum。

完整 root SQLite/PG suite、nested module suites、Linux race、5 平台 CI 与发行包 gate 由主控统一运行；本报告不将上述未在本子任务独立执行的 gate 宣称为通过。

## Existing coverage usable for final matrix

以下是本次读取源码后的覆盖映射，除 controlled test 外没有在本子任务重复执行：

- `TestRuntimePortableHistoryPaused` (`server/runtime_test.go:15`)：持久化非终态历史在 portable 启动时保持暂停，不建立 Engine runtime，main.sqlite 位于 state 而非 workspace。它用 DB 建历史，不能替代真实 worker checkpoint 恢复测试。
- `TestRuntimeManualArchiveDoesNotReplayHistory` (`server/runtime_test.go:62`)：手工新归档完成，原 queued 归档保持 queued，避免启动重放。
- `TestRuntimeClosedFolderRelocation` (`server/runtime_relocation_test.go:53`) 与 `TestRuntimeInterruptedJournalRelocation`：关闭迁移及中断 journal 迁移，含 state、workspace artifact、archive、完成 side request 与 transcript 保留。
- `TestRuntimeFileBoundary` (`server/runtime_files_test.go:32`)：workspace/skill 文件 API 阻挡通过 link 访问 state，并允许安全链接。
- `TestPortableArchiveServerContract` (`server/task_archives_portable_test.go:50`)：SQLite/PG business peer 的 archive/restore、row/commit/file retry 与 conversation admission。
- `TestTaskArchiveOwnedContextFilesRoundTrip` (`server/task_archive_package_test.go:303`)：task workspace、conversation uploads、NOA archive、side transcript 的包内 round trip，拒绝非所属 session 文件；使用文件 fixture，不是实际 NOA 模型执行。
- `TestResumeYieldsFreshContext` (`server/engine_cancelcause_test.go:155`) 与 pause/delete barrier tests：取消上下文恢复及并发 barrier。

边界：新增 prompt test 验证真实压缩请求及 transcript 保存，既有 controlled test 验证已完成历史不重放。后续下述补充已经证明**首次 Worker 模型响应前**的运行中任务冷重开和显式恢复；仍不将其推广为任意工具执行中途或 NOA checkpoint 的崩溃恢复 E2E。Skill gate 的历史重放由既有 agent/deferred 和 norma coverage 负责。

## Running task/conversation cold restart addendum

补充文件 `server/capability_cold_resume_test.go`、`server/capability_conversation_restart_test.go`。唯一生产变更为 `server/engine.go:1233`：将命名取消原因 `shutdown` 加入 `taskExecutionPaused` 的可恢复集合。

真实 RED 证明该修复必要：API 创建任务后，原 goals/planner 完成 4 次模型响应，实际 Worker 已领取 intent 3 并进入第 5 次 HTTP 模型请求；handler 读取 body 后仅等 `r.Context().Done()`。真实 Server.Shutdown 取消请求，但修复前 `runIntent` 把该 intent 写为 `blocked`，重开后的 task 已 paused、意图却不能领取。`cold-resume-red.log` 保留命令输出（exit 1，server 3.957s）及真实 `context canceled → blocked` 日志；cause contract 同时对 `shutdown` 返回 false 失败，pause/delete/goal/deadline/planner-kill 分支均按原语义通过。

生产影响已核对：`taskExecutionPaused` 唯一生产调用在 `runIntent` 的任务取消结算分支（`server/engine.go:1106`）；此处在 pause/delete 回滚时已返回 open。新增 shutdown 使正常进程关闭的未完成 worker 也保留在 frontier。PG 共用同一 Engine，亦保留可恢复意图；portable 的 `pausePortableHistory` 仍将历史 task 暂停、不建 runtime。`AbortGoalMet`、`AbortSettleDrainTimeout`、planner kill 的分类均未改变；delete 原语义没有变更。

| 需求 | 精确测试 | 真实证据与边界 |
| --- | --- | --- |
| 运行中 task 冷重开、无自动重放、显式 resume 完成 | `TestPortableRunningTaskColdReopenRequiresExplicitResume`，`server/capability_cold_resume_test.go:27` | 首次 Worker request 尚未交 `controlledAgentModel.respond` 就被 Shutdown 取消；原 Goal/Intent ID 都来自实际工具返回；重建真实 Manager/Server 后 GET task 为 paused、非 queued，Engine started=false、activeLLM=0、runtime=0，原 intent=open；观察 1.1 秒（超过 Worker polling 周期）模型请求仍 5、响应 4、receipt 0；POST task control resume 后原 intent 完成；总模型 HTTP 请求 12（含 1 次被取消）、成功受控步骤恰好 11、receipt 恰好 1；实际 fact/proves/met 和 worker transcript 含随机 nonce；done 后再次重开仍不重放 |
| 正常 shutdown 的确切命名取消与终态边界 | `TestShutdownCauseReturnsInterruptedWorkToFrontier`，同文件 | 实际 Engine.Shutdown 产生 `AbortShutdown`；shutdown/pause/delete 回滚为可恢复；goal met、deadline settlement drain、planner kill 均不可当作暂停恢复；上行 E2E 实测 manual resume expectation |
| 真实 running standalone conversation 与 retest 关闭后不重放 | `TestPortableRunningConversationAndRetestDoNotReplayOnReopen`，`server/capability_conversation_restart_test.go:20` | API 创建消息与复测，两个实际 ChatAgent HTTP 请求均进入 handler 且 API running=true；复测状态实际为 running。source finding 经真实 Manager task + production evidence Store.Record 建立；未写 SQL 注入 retest 状态/结果。关闭取消并 join 两个 HTTP request；重开后两 conversation idle、历史保留、retest stopped/finished/空 verdict，active retest 空，原 finding evidence/triage 保留；观察 1.1 秒模型调用仍恰好 2。会话契约是 idle 而非 task 的 paused；此补充测试正常 Shutdown，不伪称 SIGKILL |

HTTP handler 所有等待均受请求 context 取消；测试 cleanup 先 Shutdown 当前真实 Server，再关闭 httptest Server，失败分支不会把 handler 留在无释放通道中。恢复后 Planner 的 fixture 请求由测试在观察到真实持久化 worker intent=done 后释放；并未伪造 tool result、写入最终 fact/proof，或重新扩写 goals role。

已有精确补充映射：`TestPortableFindingContract/Restart` → `testFindingRestart` (`db/findings_portable_test.go:190`) 验证持久化 running retest 在真实 DB 重开后仍在，再显式调用 RecoverFindingRetests 变 stopped；`TestRetestDeletionAndRestart` (`db/finding_retests_test.go:125`) 验证恢复后 completion 不可覆盖 stopped；生产 Manager 构造器在 `server/manager.go:419` 调用该恢复函数。既有 `TestRetestHTTPThroughConversationAndTools` / `TestRetestStopsFailuresAndNoVerdict` (`server/finding_retests_test.go:157/:220`) 是受控 provider + 手建 Server 契约；新增真实构造器/HTTP 补充不再依赖那些替代装配 hook。`TestConversationListRunningState` (`server/conversation_status_test.go:9`) 仅验证运行中 UI 状态映射，新测试补齐实际 run/startup 边界。

补充 fresh 验证及原始日志：

```text
go test ./server -run '^(TestPortableRunningTaskColdReopenRequiresExplicitResume|TestShutdownCauseReturnsInterruptedWorkToFrontier)$' -count=1 -timeout 75s -v
RED exit 1, server 3.957s → minimal engine change → GREEN exit 0, server 5.942s

go test ./server -run '^(TestPortableRunning.*|TestShutdownCauseReturnsInterruptedWorkToFrontier|Test.*(Pause|DeleteCancellation|ResumeYields|SettleDrainAndGoalMet|ControlWork|KillWork|Timeout).*)$' -count=1 -timeout 180s -v
GREEN exit 0, server 8.190s; includes new running task/conversation/retest tests, actual Engine.Shutdown cause and existing cancellation/pause/delete/settlement contracts
```

日志：`cold-resume-red.log`、`cold-resume-green.log`、`cold-resume-regression-green.log`。主控负责最终新提交的 Server 双库 suite 与 Linux race；旧 aaba 验证快照的 full/package 结果不能代替这个生产修复的最终验证。
