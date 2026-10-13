# Portable configuration capability acceptance

2026-10-13；工作树 `portable-final-acceptance/Hack3rX_2026_011_ARTEX`，分支 `codex/portable-v0.0.1`。本补丁仅新增 `server/capability_acceptance_*_test.go` 和本报告；未修改生产代码、既有 controlled test 或代理实现。

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
| MCP HTTP、stdio argv/env、Skill gate、visibility、disable | `TestPortableReopenedMCPHTTPStdioAndSkillGate`，`server/capability_acceptance_mcp_test.go:24` | 生产 HTTP JSON-RPC initialize → notification → tools/list/call，校验配置 header、session header 与 Close 的 DELETE；stdio 真正启动当前测试 executable，收到含空格/中文/引号/美元号的原始 argv 与指定 env；两次 handshake/list + 1 次 call 的 7 个子进程事件准确；Skill load 前 gated HTTP/stdio call 均被生产 ExecuteExtraTool 阻挡且没有传输 call，load 后都产生真实 receipt；无可见性 Agent 不连接；禁用后重开 direct MCP 不再连接/广告，disabled Skill-referenced MCP 从未连接；发现 cache 经 API 保留 | 仅 Streamable HTTP 与 stdio，legacy SSE 由 mcphttp 既有 tests 覆盖；child 仅记录本次命名 env/sentinels/显式 argv，不 dump ambient env；此 test 不复测 runtime tool discovery/launch overlay |

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

测试开发阶段出现并纠正了 fixture 假设：Skill create 的正确 HTTP status 为 201、技能根目录前提、MCP Close 的 DELETE，以及 MaxDuration 不取消模型请求的既有契约。最终未发现需要生产修复的适配 bug，因此没有生产 RED/fix 宣称。

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

边界：新增 prompt test 验证真实压缩请求及 transcript 保存，既有 controlled test 验证已完成历史不重放；二者不等于任意**运行中** worker 在冷重启后从 transcript/NOA checkpoint 继续执行的一次完整 E2E。Skill gate 的历史重放由既有 agent/deferred 和 norma coverage 负责，本补丁聚焦持久化配置重开后的首次真实装配。
