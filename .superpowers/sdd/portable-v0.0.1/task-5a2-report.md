# Task5a2 自动检测与实际执行选择报告

最新 Fix1 生产源码为 `9f6af31cb96be73d8a4ed08f80e9abab67347899`，Windows 四路径 probe-owner 行为 gate 与 controller 固定 SHA Linux runtimeenv 全包 `-race` 均已 PASS，详见末尾 Fix1。后续补充仅 Windows Start-failure 测试和本报告，生产源码、SDK副本、manifest/patch保持不变。初轮审阅 I1 已实施修正，最终 verdict 待 scoped re-review。

范围完成到分项实现与 Windows 行为 gate；Task5b 最终 portable 构造/启动/恢复/API/总生命周期，以及 controller 固定 SHA Linux native/race gate 尚需独立执行。本项不声明 Task5 完成。

起点 `84e26dc7b00a1ca12af5de5fbde52bf1c0b063f6`。仅使用本 worktree、独立临时目录/loopback fixture。未读取旧 ARTEX 实例、任何旧配置/secret/Key，未修改模块缓存或代理实现，未触碰 controller implementation/verification.md。源提交 SHA 交接见本报告后续记录。

固定源码提交为 `6178c153d2e753d6fe781850504faa916c28e22f`，报告提交只写本报告，生产/测试/来源副本与 patch 不再改变。代码实施状态 DONE_WITH_CONCERNS（明确未跑 native Linux/race 与原 upstream Windows legacy 三项失败；controller 负责独立 review/gate）。

## 5b 接口与单一 owner

`runtimeenv.New(Options{BaseDir, StateDir, HostPATH, RelayExecutable, ProbeTimeout, ProbeOutputLimit})` 独占 process-global SDK owner；BaseDir/StateDir/RelayExecutable 必须绝对路径，HostPATH 从加 alias 前宿主环境捕获，空值表示不搜宿主。默认每候选 2 秒/16KiB，硬上限 10 秒/64KiB。

`(*ToolManager).Check(ctx, Config{Paths map[string]string, MCPCommands []string}) (Snapshot,error)` 先检测和准备完整新 generation aliases，再一次发布 norma/tool.ExecutionRuntime（带 JSON metadata）。`Snapshot()` 从同一 publication 深复制，因而环境结果/实际 executable/ChildPATH 同 generation；failed/cancelled check 保留旧值。自定义 MCP 只输入 command 名/路径，绝不传 business args/env。可选工具缺失不阻断 Check。

工具 keys：`python,node,npm,npx,rg,nmap,curl,shell,chromium,playwright`。Windows shell 选 PowerShell/pwsh；PTY 明确 unsupported。已知工具先 runnable runtime/bin，再有效 configured path，再过滤 host PATH；失效旧电脑路径/格式/loader/探测超时/非零/输出溢出均记录候选失败后回退。Node 必须本 generation 选中，Windows npm/npx `.cmd/.bat` 仅已知 `node_modules/npm/bin/{npm,npx}-cli.js` layout 转 Node+argv；未知 wrapper 不猜测，也不错误归为 binary architecture。Chromium 仅 file/PE VERSIONINFO/architecture metadata，执行 unverified，绝不启动 GUI。Playwright 只读 bounded 本地 package.json name/version，绝不 npm/npx 下载。

`runtimeenv.Command(ctx,name,args,dir,env)` 与 norma shell/Grep/Python 读取发布快照，使用绝对选中路径。SDK snapshot 有 `Generation,ShellPath,ShellArgs,ChildPATH,Commands,Metadata`，setter 校验、深复制、atomic publication，无探测副作用。SDK `ExecutionSnapshot()` 对外深复制。

`runtimeenv.MCPCommand(command,args,env,dir)` 返回尚未 Start 的命令，显式 business argv 原样保留，包括用户主动 npx/npm package。生命周期 context 不绑定短握手；交 `mcp.NewStdioClientWithCommand(ctx,server,cmd)`，ctx 只限 initialize，Client.Close stops tree+Wait。自定义 command 新增/变更后 5b 应 rescan 再发起用户手动连接；未知 .cmd/.bat 要 native/local Node entrypoint，能力 unverified，不伪称通用 batch 兼容。

`server.MCPLaunchOverlay{ProxyAddr,CACert,Env}` 在真实 stdio connect 时合并，browser 先 strip 旧 proxy args，再注入实际 retained listener proxy/CA；不改 DB MCP.Args/Env。`wireAgentAugment` 可选 overlay provider，生产 server 装配 provider；没有 published runtime 仍使用 legacy PG 入口。5b 必须让 startup/rescan 不走 automatic discovery，并停止 portable `syncBrowserMCPProxy` 的历史持久地址写回路径；本项提供 overlay，无需持久动态端口。

`cmd/artex.run` 首行动作为 `runtimeenv.RunRelay()`。正常 artex 运行无副作用；native alias 只读取相邻 `.tool-relay.json`，转 selected absolute executable+prefix+原 argv/stdin/out/err/exit code。Unix exec（优先硬链接），Windows independent executable copies（运行映像 hardlink 锁会导致 Close 不能删）；所有 known tool unavailable 也生成 fail alias，不让已拒绝候选从 host PATH 复活。原 runtime/bin/自身 alias dirs/相对/空 PATH 项排除；Windows child 移除内置 curl alias，中文/空格 argv 真实验证。旧 generation 在 running child/session 期间保留。

Close 顺序交 5b：禁止新调用/Check → 停并 join foreground/background/PTY/MCP/所有其它消费者 → `ToolManager.Close()` 清 SDK ownership 并移除本 owner 全 generation aliases。Close 幂等返回保存错误，Windows scanner 临时 sharing conflict 有 2 秒有界重试；不能一边 child 活着一边 Close alias。只允许一个 process-global owner；同进程多个 Manager 独立 selections 暂不支持。SDK 全局 tests 串行且保存/恢复 snapshot。

## 来源与补丁

只读 archive `D:/software/gopath/pkg/mod/cache/download/github.com/!autumn-27/norma/@v/v0.4.3.zip`，zip/cache 全 210 文件逐 raw byte 对比。原 source/module/version/docs/copyright 保存；archive 无 LICENSE/COPYING/NOTICE，如实记录，未伪造 license。

原 h1 `DnmOhJPL6OZSMnCVYO5Ujh4yVINuDarEickVi9jx1hI=`；zip SHA256 `fd6e096f5ee5399b51d26fb6c3b7a43c04b5dd43bb1807dc1fe676d63e24f720`。210 baseline，5 changed（tool/bash.go、task.go、shell_session.go、ripgrep.go、mcp/stdio.go），6 added（runtime execution/PTY tests、prepared MCP test、平台 stdio process helpers），0 deleted；205 原文件 unchanged。`third_party/patches/norma-v0.4.3.patch` 独立 module-relative 精确 diff，manifest 记录全部原始/changed/added hashes；proxy 原 entry/raw hashes 未改。已有 verifier 已支持 --module，无需重写。

## TDD 与关键真实红绿

- SDK 新接口初始红：missing snapshot API；补 stub/接口后真实红确认 foreground 仍跑 PowerShell、no rg 仍执行 npm install。改实际入口后绿。
- detector 初始红 missing ToolManager/relay；集成后坏 bundled node/rg → host，真 Bash foreground/后台、shell 内裸 node/rg/Grep → host；改为 valid bundled 后 Check，后续实际 launch 与 Grep 路径切换。alias preparation 故障保持旧 generation；旧 PATH 文件仍在。
- Windows npm/npx known layout Chinese space，真实 node sentinel 看 CLI 路径与完整用户显式 argv。固定 --version probes 不写 business marker。未知 wrapper/optional missing 用 metadata/error，check 不执行自定义 MCP/browser。
- MCP prepared 初始 red missing entry；真实 initialize/tools/list/tools/call，取消 handshake context 后 session 仍活，Close.Wait，invalid pipe 不 Start；green。
- Windows package-runner descendant heartbeat 红证明 old Close 只杀父而继续写；treeKill 先于 stdin.Close（否则 runner EOF 退出使 taskkill 错失 tree）后绿。
- snapshot sync timeout heartbeat 红出现 relay descendant 持 output pipe，cmd.Wait 超过 20 秒；snapshot-only process-tree Cancel+2 秒 WaitDelay 后绿；背景 tree kill 原路径实际绿。仅清理由本 test 产生的 exact temp process，未碰其它进程。
- Python executor 红证明旧 interp 仍被调用；snapshot 选中 absolute Python 后 green，随后 Chinese stdin/env 真红发现 Windows GBK 解码；UTF-8 JSON IO 设置后 green，missing selected Python 不 fallback。
- curl mutation 红：临时移除 child-local PS alias cleanup，实际命中 Invoke-WebRequest，恢复后 green；没有修改用户 profile/global alias。
- Playwright metadata 红 missing；bounded package metadata 后 green。version output 红泄漏额外 text，改只取 parsed version 后 green。
- Windows copy alias scanner transient handle 红 Close 拒绝；真实 held no-share-delete handle 的 bounded retry 后 green。

## 已执行 gate（Windows Go1.26.3 amd64）

日志均在本计划 workspace，不强制提交 test logs。所有 root server 命令用独立 `ARTEX_PG_DSN=postgres://sandbox@127.0.0.1:1/sandbox?sslmode=disable&connect_timeout=1`，不连接旧实例；未用 model/Key。

1. `go test -json ./runtimeenv ./server ./cmd/artex -run 'TestRuntime|TestCheck|TestBounded|TestWindowsKnown|TestLocalPlaywright|TestProbeVersion|TestCloseWaits|TestBackgroundRelay|TestSynchronousRelay|TestExecPython|TestShutdownContext' -count=1`：PASS。runtimeenv 全 9 tests PASS 34.477s；server 3 actual adapter tests + helper PASS 5.295s；cmd shutdown case PASS 0.794s。既有 `TestExecPython` 只搜 python3，本机仅 Python `python`，因此 SKIP 1；新增 real Python case 使用实际本机 Python 已 PASS，Windows PTY unsupported 被断言而非 skip。日志 `task-5a2-windows-final.jsonl`。
2. norma nested 单独 `go test -json ./tool ./mcp -run 'TestExecutionSnapshot|TestRipgrepNever|TestRuntimePTY|TestPrepared|TestStdioClientOverPipes' -count=1`：3 tool behavior tests PASS 1.873s，4 MCP tests PASS 2.460s，无 SKIP。日志 `task-5a2-windows-norma-final.jsonl`。
3. 尝试完整 norma `go test ./tool ./mcp -count=1`：MCP 全包 PASS 2.656s；tool 3 FAIL（`TestBashEnvInjection` 的 `$HTTP_PROXY` Unix syntax、`TestManagerSpawnCompleteNotifies` 的 `expr 6 \\* 7`、legacy `TestCleanupRemovesDir`）。另从原 zip 提取独立 pristine baseline，以同 three-case 命令原样复现 3 FAIL 4.541s；这是 upstream Windows 基线问题，未改原测试藏失败，legacy Cleanup owner gap 交 5b。日志 `task-5a2-windows-upstream-baseline.jsonl`。不宣称原完整 tool suite PASS。
4. `go vet ./runtimeenv ./server ./cmd/artex` PASS；nested `go vet ./tool ./mcp` PASS。
5. root `go test ./... -run '^$'` PASS；norma nested `go test ./... -run '^$'` PASS。是 compile gate，不是完整 business suite。
6. `python third_party/verify_upstream.py --module norma --archive <original norma.zip>` PASS；相同 proxy `--module go-mitmproxy --archive <original proxy.zip>` PASS（104/6 changed/2 added/0 delete），原 proxy source 未改变。
7. `git diff --check` PASS。五平台 `CGO_ENABLED=0 go build ./cmd/artex` gate 结果见后续记录；cross-build 不替代 native execution/PTY/race。

## 自审与明确边界

重查实际 resolution（exec.Command 前 absolute，而非事后仅 PATH）、同 snapshot executable+PATH、nil legacy、failed publish、unavailable blocking aliases、旧 generation、Windows wrappers/PS curl、MCP 握手 context/child ownership、bounded probe output、version-only exposure、准确 original bytes。未改 proxy 与 Tasks1-4 storage。

未做 final portable CLI wiring、runtime API、startup history pause/global tasks joining、branding/frontend/release、LLM business acceptance。无完成 native Linux/race/macOS 执行声称；controller 将固定源码 SHA 跑 Linux runtimeenv 全包与 changed norma tool/MCP、root server selected launch fixtures。Windows 完整原 tool suite三项基线 FAIL 已明确保留。未知 arbitrary batch MCP、browser file-only、Playwright package metadata 执行能力始终 unverified。

## 最终补充与交接

- 五平台 `CGO_ENABLED=0 go build -o <独立计划workspace输出> ./cmd/artex` 全部 PASS：windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64。构建在最后“known MCP saved path 纳入 configured 候选”的微调之前完成；该微调未改公共 app 构造签名，随后 native Windows related TestConfiguredKnown/TestCheckNever/TestWindowsKnown 和 runtimeenv vet 重新 PASS，Linux 固定 source gate 等 controller。不能以 cross-build 声称 macOS/Linux native 执行。
- 最后自审增加一条真实 red：仅 `Config.MCPCommands={configured absolute node path}` 而 HostPATH/Paths 空，known node selection 当时缺失，实际 MCP 无法启动。修正为 known filename 的 saved local path 也按固定 harmless version args 进入 known tool configured 候选（显式 Paths 优先），custom 未识别 filename 仍 file-only/unverified。业务 argv/env 未进入 probe。`go test ./runtimeenv -run 'TestConfiguredKnown|TestCheckNever|TestWindowsKnown' -count=1` PASS 4.478s；`go vet ./runtimeenv` PASS。runtimeenv source 现在共 10 tests；上方 final JSON 是此前 9-case full gate，最后三-case related gate覆盖新增改动，不冒充此前 log 含第10case。
- 最终 `git diff --cached --check` PASS；源码 commit 只包含本人 app/runtime/norma source+tests/provenance/doc，不包含 testlogs、生成构建产物、secret、controller verification。新增来源副本 210 files 属 provenance，不能把四万行 copied upstream 当作新写业务逻辑。
- report 已提供 native runner 重点：`go test -race ./runtimeenv`；norma nested `go test -race ./tool ./mcp -run 'TestExecutionSnapshot|TestRipgrepNever|TestRuntimePTY|TestPrepared|TestStdioClientOverPipes'`；root `go test -race ./server -run 'TestRuntimePythonActual|TestRuntimeMCPAdapter|TestRuntimeMCPDynamic|TestRuntimeServerMCPHelper'`。Linux shell/PTY require bash，Python case要求真实 python/python3。unknown batch/Windows-specific cases在Linux按平台 skip，不能记为全平台无SKIP。
- 5b 的必要 construction 顺序：保留实际 proxy listeners/CA → 建唯一 ToolManager（runtimeenv.New，无 probe/下载）→ 从 preserved config 读取 executable paths 和 MCP command 字符串 → Check/publish → 再允许 Agent/工具/MCP用户调用。startup/rescan禁止业务MCP discovery；用户手动新增/修改配置后 rescan，再连接或启动。关闭：先禁止新调用→所有 consumers stop+join→ToolManager.Close→后续 state/DB teardown。尚未发布 runtime 的 PG legacy 默认不改变。

## Fix1（I1：probe 自己的派生进程归属）

BASE `37b2d5fceacaa797a2edea7544fd4264419bf561`；修复生产源码 `9f6af31cb96be73d8a4ed08f80e9abab67347899`。初轮 review spec❌/Needs fixes 的唯一 Important I1 真实证明：默认 CommandContext 只杀版本候选父进程，WaitDelay 只限 pipe 等待，Check/Close 返回后派生 heartbeat child 仍写入。本轮只改 `runtimeenv` probe owner，不扩大 startup/Manager/Shutdown、MCP显式能力或SDK scope；controller `implementation/verification.md` 保持未暂存、未编辑。

实现：

- 版本探测仍只使用原 fixed harmless argv、原 timeout/output bounds、filtered PATH 和 sanitized version/failure。改为 `exec.Command`，由独立 owner 管取消；未增加 package业务调用、下载或版本限额修补。
- Windows 为每次探测建独立 `KILL_ON_JOB_CLOSE` Job Object。候选以 `CREATE_SUSPENDED` 启动，先 assign 私有 Job，再只恢复该 owned PID 的暂停线程；杜绝 resume/派生在 job assignment 之前发生。停止用 `TerminateJobObject`，最多2秒查询 `ActiveProcesses` 至零再释放job；超限/查询失败显式报 cleanup failure，不能假称成功。Job归属不依赖父进程仍存活，所以覆盖父正常退出后遗留child。
- Unix 为 probe 新建process group；取消/退出时 SIGKILL整个owned group，group在leader退出后仍可寻址。common runner join直接child（cmd.Wait），并 drain/close+join自己唯一bounded output copy goroutine。未知非child进程的OS收尸仍由平台parent/init负责；不声称Go能Wait不属于自己的任意grandchild。实际 fixture 证明return后无继续执行/文件写入。
- stdout/stderr 改显式os.Pipe与owned copy goroutine，不让 cmd.Wait 自带output复制等待遮挡父正常退出；直接child退出即stop owner，随后join root+output。输出drain有2秒兜底，异常关闭reader也join copier并返回cleanup失败。
- Windows failure自审：CreateJob/SetLimit/Start失败都close已得job；Start后 Open/Assign/thread snapshot/owned thread/Resume失败都stop+drain Job→Kill+Wait尚未恢复的child。每个job/process/thread/snapshot handle有释放路径；没有修改父Codex/job、用户PATH、modulecache。

TDD真实 RED：

`go test -json ./runtimeenv -run TestProbeOwner -count=1 -timeout=50s` against `37b2d5f` 的生产probe：timeout、overflow、success-parent-exit三case都FAIL **owned probe descendant continued writing after probe and ToolManager.Close**（4.20/2.91/2.93秒，整体17.635秒）。同fixture外部cancel初次一次读取采样误判PASS；立即增强为非空heartbeat+120毫秒多次采样后，`-run TestProbeOwnerCancelled` 确认同一句真实FAIL，7.594秒。测试已清理自己的exact childPID，没有触碰无关进程。原始证据：`task-5a2-fix1-probe-red.jsonl`、`task-5a2-fix1-cancel-red.jsonl`。

GREEN与最终gate：

1. Windows owner修复初次 `go test -json ./runtimeenv -run TestProbeOwner -count=1 -timeout=50s` PASS **6.705秒**（timeout、overflow、success-parent-exit、externalcancel；外部cancel断言Check error=context.Canceled并保留旧generation/path/SDKpublication）。日志 `task-5a2-fix1-probe-green.jsonl`。
2. fixture随后以临时file+rename原子发布heartbeat，防止强杀恰在truncate/write之间留下空marker；非空读取也有1秒显式失败界限。这是test-only强化，生产代码未改变。最终同命令 PASS **6.940秒**，4 leaves（timeout2.17秒、overflow0.98秒、success1.01秒、externalcancel2.21秒），0fail/0skip。每case都在probe和ToolManager.Close返回之后继续观测，真实断言无heartbeat变化。日志 `task-5a2-fix1-probe-final.jsonl`。
3. Windows `go test -json ./runtimeenv -count=1 -timeout=120s` 全包 PASS **50.354秒**，14leaf（15个PASS events包括subtest父group）、0fail/0skip。日志 `task-5a2-fix1-runtime-windows.jsonl`。该full gate在最后test-only原子fixture强化前，生产代码与9f6af31相同；强化后focused gate已重新跑，不假称full日志含更新后的fixture。
4. 追加Windows真实 OS Start-failure测试（生产不再改）：`go test ./runtimeenv -run TestProbeOwnerWindowsStartFailure -count=1 -timeout=20s` PASS **0.768秒**。warm后连续16次missing-executable探测均error/无child/无output，root Windows `GetProcessHandleCount` 无递增泄漏，验证已经创建的Job/pipes释放与copy join。未把Assign/Resume API failure注入当作实测；这些失败分支为上文自审。测试新增仅 `runtimeenv/probe_failure_windows_test.go`，Linux不编入。
5. `go vet ./runtimeenv` PASS；`git diff --check -- runtimeenv` 与source staged diffcheck PASS。仅平台owner改动，额外 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o <独立输出> ./runtimeenv` PASS。未无理由重跑完整root业务/SDK套件/五平台构建；SDK原字节、副本patch和provenance未改，无需重生成。
6. Controller独立固定源码 **9f6af31**：Linux `go test -race ./runtimeenv` 全包 PASS **38.502秒**，四条owner heartbeat RUN→PASS且无race；仅Windows npm wrapper platformcase SKIP1。运行session96878 exit0，隔离container已删除。controller证据 `linux-environment-fix1-race.log`、fixed source tar，属于本fix新源码gate；Windows新增startfailure testcase不在该Linux源码包内，但production完全相同。

前次6178c15的controller Linux gate是初轮source历史证据，不冒充Fix1 gate。补充纠正：review最初对norma两个日志是否含-race的陈述已由reviewer/controller核对原命令并纠正，这些旧gate确实使用-race；本fix没有改norma。既有pristine upstream Windows三项tool FAIL仍准确保留，没有修改上游测试或隐藏失败。

交5b的唯一owner/Check/Close接口和先stop+join consumers再Close契约不变；本轮额外保证Check自己启动的probe在任何退出路径都stop/drain/join，不能将这一归属推给5b。没有接最终portable构造/HTTP/全应用关闭或LLM acceptance。最终需要controller基于Fix1 diff进行scoped re-review。
