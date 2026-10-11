# Task5a1 实施与验证报告

最新 Fix1 源码为 `93f4ddcb87aabe2bfa42480a737a4a2d8f91d919`；本轮 I1/M1/M2 修复、共享 transport 冷并发兼容修正与 Windows gate/provenance 已完成。自有 Linux1.27.2 最小 cold-start race 连续3次通过，controller 正对最新固定提交做完整 native race gate。下方初轮和 Fix1 中间提交的失败证据保留为历史记录，不能以普通 Windows 或最小 Linux gate 声称 I1 已最终验收关闭。

**验收状态：native Windows 普通 gate 已通过，controller Linux race gate 未通过；Task5a1 尚不能标记最终验收完成。** 固定源码 `f416cc2` 存在已经证明的 attacker HTTP server 初始化与 h2 ServeConn 竞态，见下方独立验证补充。按 controller 指示暂不修改源码，将该问题并入完整 review 后统一 fix1。

本子项实现代理保留 listener 与完整 stop/join，接入 traffic 索引关闭；不声明 Task5a/Task5 或完整便携启动完成。源码固定提交 `f416cc2b3adaff92668165582a48168964bc88c6`，起点 `faa57b7f92f0ae0750bb515bd5616586ff9d09c3`；之后的报告提交只增加本报告与验收日志，生产文件和测试保持固定源码提交一致。未修改 controller implementation/verification 文件。

## 接口与归属（交 Task5b）

```go
// github.com/lqqyt2423/go-mitmproxy/proxy
func (*Proxy) Serve(net.Listener) error
func (*Proxy) Start() error
func (*Proxy) Close() error
func (*Proxy) Shutdown(context.Context) error

// github.com/Autumn-27/artex/traffic
func (*Traffic) Serve(net.Listener) error
func (*Traffic) Start() error
func (*Traffic) Close() error
func (*Traffic) Shutdown(context.Context) error
func (*Traffic) ProxyAddr() string
```

- Serve 接受原始、已保留的 TCP listener，SDK 内仍经过 wrapListener/ConnContext。不能改为直接 HTTP Serve(raw) 或预先 tls.NewListener。原 CA、HTTP/HTTPS、CONNECT、WS/WSS、h2 捕获链保留，`Proxy.Version` 仍为 `1.9.2`。
- 任意非 nil listener 调用 Serve 即转移归属，包括已关闭 proxy、重复/并发 Serve、Serve/Accept 失败分支；被拒绝的 listener 也关闭。nil 返回错误。只准一次 Start/Serve；关闭后不允许重启同一实例。
- NewProxy/Open 或尚未调用 Serve 的失败由 caller 关闭其保留 listener；调用 Serve 后由 Proxy 接管。caller 创建的 Serve goroutine 不属于 SDK：上层保存 buffered result channel，并在 Close/Shutdown 后读取/join；正常关闭保留 `http.ErrServerClosed` sentinel。
- Traffic.Serve 在 handler admission 前发布 `ln.Addr().String()`，ProxyAddr 用实际端口；拒绝的后续 Serve 不覆盖已有地址。建议 5b 把保留 listener 的地址直接传入 `traffic.Open(dir, ln.Addr().String())`，并在服务 ready 后才给 Agent/浏览器使用。PG Start 仍实际绑定配置的固定端口；裸 `:port` 保留向客户端返回 loopback 的兼容行为，`:0` 也返回真实分配端口。
- 关闭时不持 registry mutex 调用 addon；所有 work Add 与 closing 使用同一 gate。socket registry 包含 raw upstream、client wrapper、server callback wrapper；client 与 upstream 的关联受 closeMu 保护，不再在 client.Close 读取正在变更的 ConnContext.ServerConn。

关闭顺序：Proxy 禁止新工作 → 取消 owned contexts → 停入口/attacker listeners → 断所有普通/CONNECT/h2/WS 上下游 socket → 关闭两 HTTP servers → 完成 disconnect callbacks → 等 HTTP/CONNECT handlers、attacker Serve、h2+watcher、TLS handshake worker、双向 copy/WS workers → 关闭 idle transport。Traffic 先 signal reclamation 停止，再 Proxy.Close 真正 join capture writers，随后通过 wmu barrier drain 已有删除/暂存事务，等待 reaping，最后 Close SQLite。新 Delete/Stage/Recovery 在 stopping 后被拒绝，避免 Wait 时新增回收线程。

Shutdown 与 Close 共享 force-stop+join 边界，包含长驻 tunnel；不会无期限尝试排空活跃 CONNECT/WS 流。即使 context 已过期也先强制关闭并等待真实 owner，之后才返回 context error。因此 Traffic.Shutdown 返回前索引已安全关闭，不能把它误解为只有 cancel 请求。Addon 必须返回，且不能在自己的 callback 同步调用 Close；任意 Go callback 无法被强杀。已成功创建的 HostDeleteStage 必须按既有契约 Commit/Rollback，5b 须先停并 join archive 等调用者，不能遗留持锁 stage 再关闭 Traffic。

应用总体 Manager/Server Shutdown、主 CLI、portable 模式/锁/listener 选择、norma/工具检测均未在本子项实施。

## TDD 与行为证据

最初红测：缺 Proxy.Serve/Traffic.Serve/Traffic.Shutdown，attacker Close 无法唤醒 Accept。随后实际红测还捕获并修复：上游 disconnect callback 晚于 Close；client disconnect 遗留 keep-alive upstream；活动 DeleteStage 被索引 Close 越过；重复 Traffic.Serve 覆盖真实地址；关闭后 Recovery 未拒绝；PG 裸 `:port` 丢失 loopback 客户端地址。

双向 transfer 测试另将原始 helper.go 暂时恢复于项目副本（从只读 cache 读取原字节），得到 `transfer returned while second reader still running`；随后恢复项目补丁并通过。所有 cache 源文件均未写入。旧协议 Close/Shutdown 回归捕获 raw listener 关闭与 http.Server 标志的竞态，已保留原 `http.ErrServerClosed` 兼容 sentinel。

自有测试全部使用 t.TempDir/OS 分配本地端口和受控本地 target；HTTP/HTTPS/h2 断言真实响应正文和 addon capture 数；WSS 做真实回显并验证 WebSocketEnd 在 Close 返回前；transparent CONNECT 传输真实 HTTP 正文；stalled client/server TLS 分别证明 worker 退出；在 HTTP/HTTPS/h2 capture callback 内阻塞，证明 Close 等待其写操作；traffic 测试阻塞真实 capture，证明索引仍开放到 writer 完成，并验证大正文 blob/索引文件树在 Close 后无变化。

原 upstream 协议测试使用它们原有受控端口（29083–29106），其默认 CA 位置通过**仅该测试命令进程内**的临时 USERPROFILE 指向全新临时目录，并在 finally 恢复；没有读取/写入原用户默认 CA。新 lifecycle 测试 CA 均在各自 t.TempDir。未访问 `G:/ARTEX-0.3.15`、旧实例/配置/Key 或其端口；未执行外部模型/目标请求。

## 最终 Windows gate 与日志

平台：native Windows/amd64，`go version go1.26.3 windows/amd64`。以下成功日志均来自最终源码，JSON 计数排除 package pass 事件，区分 top-level、leaf 与 parent/subtest pass 事件；所有失败/skip 均为 0。

| 范围 | top-level pass | leaf pass | test pass events | 留档 |
|---|---:|---:|---:|---|
| SDK 新 lifecycle | 14 | 19 | 22 | `task-5a1-validation/proxy-lifecycle.jsonl` |
| SDK 原协议回归 | 14 | 25 | 39 | `task-5a1-validation/proxy-upstream-protocols.jsonl` |
| root traffic 全包（含新增 5 项） | 44 | 47 | 48 | `task-5a1-validation/traffic.jsonl` |

准确命令（根仓库的 go test 不会遍历嵌套模块）：

```text
# cwd = third_party/go-mitmproxy
go test -json ./proxy -run 'TestReserved|TestClose|TestClientDisconnect|TestConcurrentServe|TestListenerOwnership|TestAttackerListener|TestShutdownDeadline|TestTransparentCONNECT|TestServeFailure|TestTransferWaits' -count=1 -timeout=60s
go test -json ./proxy -run 'TestConnection|TestProxyClose|TestProxyShutdown|TestWebSocket|TestSSE|TestAttackerHTTP2' -count=1 -timeout=120s
go test ./proxy -run '^$' -count=1 -timeout=60s
go vet ./proxy
# cwd = root
go test -json ./traffic -count=1 -timeout=90s
go test ./... -run '^$' -count=1 -timeout=120s
go vet ./...
git diff --check
git diff --cached --check
```

Root 全包 compile、root 全包 vet、嵌套 proxy compile/vet 均 exit 0；`root-compile.txt` 留档编译。`CGO_ENABLED=0 go build ./traffic` 在 windows/amd64、linux/amd64、linux/arm64、darwin/amd64、darwin/arm64 全部 exit 0，见 `cross-build.txt`；这是编译，不代表其他平台 native 协议运行通过。日志汇总为 `summary.json`。最终成功命令仅进程内 GOPROXY=off；没有修改宿主 PATH/Go 配置。

覆盖限制/实际失败：Windows `go test -race` 编译后运行得到 `exit status 0xc0000139`，不称 race 通过，controller 将用独立 Linux 容器固定源码做 race。嵌套模块全包 compile 曾因缺失 `github.com/samber/lo v1.53.0` 而触发 Go 下载尝试，网络 timeout；未声称 SDK addon/cmd/examples 全包通过，也未改 upstream go.mod/go.sum 或清理宿主 cache。该尝试留下 cache 的 `v1.53.0.lock`，不能宣称所有 cache 字节零变化；两个 SDK 源目录未修改，最终逐文件仍与缓存 zip 原始字节一致。另有 direct-toolchain/GOSUMDB=off 配置与 Windows 临时 exe handle 的失败尝试，已回到正常 Go launcher，仅命令范围 GOPROXY=off，并完成上述最终成功 gate；未修宿主环境。

## Controller 独立 Linux race 补充（未通过）

报告初版文档提交 `93650e251327649d4bb2877b4b4a6f819c665be1` 后，controller 对固定 source `f416cc2b3adaff92668165582a48168964bc88c6` 执行 nested proxy native Linux race gate，session 84475 exit 1。日志 `.superpowers/sdd/portable-v0.0.1/linux-proxy-listeners-race.log:21-130` 记录两处 `WARNING: DATA RACE`：

- `http2.(*Server).serveConn` 在 `golang.org/x/net@v0.55.0/http2/server_wrap.go:114` 及 `:127` 读取 BaseConfig/调用 HTTP2 配置；调用来自项目副本 `proxy/attacker.go:171`。
- 同时 attacker.start 的 `http.Server.Serve` 在 `net/http.(*Server).configureHTTP2` 写入相同 HTTP server 配置；对应 `proxy/attacker.go:131`、`proxy/lifecycle.go:114`。
- `TestReservedListenerCapturesHTTPHTTPSAndH2/h2` 因 `race detected during execution of test` 失败。普通 Windows gate 的成功不能覆盖这项失败。Controller 保留原日志；本报告提交不暂存或修改 controller 的证据文件。

已在最终报告中记录真实未通过状态，且没有提前修代码/更新 patch 来掩盖失败。完整 provenance/patch/application review 后由 controller 统一 fix1，再对固定修复源码复验 Linux race 与协议 gate。

## Provenance、patch 及 self-review

- 原版本/模块声明、Go directive、MIT LICENSE（Copyright (c) 2020 liqiang）、docs/tests/dependency manifests 保留；只复制 go-mitmproxy，未 vendor transitive dependencies。根 require 仍为 v1.9.2，只增本地 replace。
- `third_party/upstream-manifest.json` 为 `schemaVersion:1, modules:[...]`；后续 5a2 在 modules 列表追加 norma，不覆盖此记录。每个 upstream file 有 path/size/raw SHA-256；changed 有 upstream/patched SHA-256 和 patchID；added 有 size/hash；deleted 单列为空。
- 104 baseline、98 unchanged、6 changed、2 added、0 deleted。Changed：proxy/attacker.go、connection.go、entry.go、helper.go、proxy.go、websocket.go；added：proxy/lifecycle.go、listener_lifecycle_test.go。未改文件全部逐 byte 相等，绝不以行尾规范化冒充 unchanged。
- Zip SHA-256 `0abd197455a47fc6045176cb29727959e51686de62d8bcd09be000236c3744d2`；Go h1 与 root go.sum/cache .ziphash 同为 `h1:+QTbDyUUaVRmEavPq0/eWzJ2K+K4JdL4ff91s/adnxk=`。仅离线验证，不宣称本次 SumDB 联机验证。
- 独立模块相对 patch 为 `third_party/patches/go-mitmproxy-v1.9.2.patch`；pristine zip 应用 git apply patch 后，106 patched 文件全部与 native-tested worktree byte-for-byte 相等。`third_party/verify_upstream.py --archive <zip>` 可复核原 archive/file hashes、全部 change lists、patch hash 与精确 baseline-to-copy diff；无网络或 cache 写操作。
- source 固定提交的 Git archive 与 `core.autocrlf=true` 全新 sparse clone 均再次通过 archive/manifest/patch 校验，106 文件也均与 native-tested source 完全相同，见 `committed-provenance.txt`。`.gitattributes` 狭义 `-text` 覆盖该 SDK/patch/manifest；保留原 README 等 6 个未改文件的 whitespace 例外及 patch context 语法例外，modified/new Go 源仍正常 whitespace 检查。全 staged/unstaged diffcheck exit 0。
- Self-review 分来源层/完整 module-relative patch/app integration 三层完成：确认同一 closing gate 禁止 Wait 期间 Add；普通/hijacked、TLS/h2、copy/WS 与 disconnect callbacks 都有实际等待；callback 不在 registry 锁内执行；accepted-before-close 的 sink 在 index.Close 前完成；mutation barrier 与 reaping enrollments 共用 wmu；重复 Close 返回保存结果；listener reject/failure 不遗留 owner；PG 裸端口兼容已用真实 HTTP 验证。没有把 98 个未改 upstream 文件当作新增原创逻辑，也没有扩大到 5a2/5b 的生命周期归属。

本报告与验证日志的后续 commit 不改变 `f416cc2` 的生产/测试字节，controller 对该 fixed source 的 review/race 结论可直接应用。剩余验收是 controller 的独立来源/补丁/application review、统一 fix1 修复已证实的 h2 竞态及 Linux race 复验；5b 组合关闭时须遵循上述 ownership 和 stage 完成边界。

## Fix round1/5：I1、M1、M2

起点 `14ad19f944b761f96bb1629df8ee6229f5d75fdd`，完整阅读 `task-5a1-review.md`，使用 systematic-debugging/TDD，未扩展 CLI/env/norma/旧实例，未修改 controller 的 implementation/verification.md。Fix1 源码提交 `ffadaa4857eaf48cf6d85fec8aa2d50fce338eda`；后续报告提交仅更新本文，不改变该固定源码。

I1 根因与修复：沿独立 Linux race 栈读取 x/net/http2 v0.55.0/v0.58.0 `server_wrap.go` 与 Go HTTP server 初始化路径。x/net BaseConfig 分支读取 `a.server.TLSConfig`/`HTTP2` 创建 one-off server，同时 attacker.start 的 `http.Server.Serve` 初始化相同字段。`a.server` 的配置只有 handler/owned BaseContext/ConnContext，没有自定义 timeout、limit、TLS 或 HTTP2 字段；h2 早已显式传入 owned `Context` 和 `Handler`，因此最小修复是删除共享 `BaseConfig: a.server`，使用 x/net 自身的独立 per-connection 默认配置。没有添加 sleep/轮询猜测、改变 timeout/limit、修改依赖版本或用“通常先启动”掩盖竞态。主 RED 证据为原始固定源码的 Linux race FAIL，两组读写栈与真实 h2 失败仍原样保留；新增 `TestColdStartConcurrentH2Capture` 在冷启动时发 8 个并发真实请求，验证全部取得 `HTTP/2.0` 正文与 8 个 capture。该普通回归证明协议不回退，不能替代 race GREEN。

M1 RED/GREEN：新真实 CONNECT 关闭日志测试在修复前失败，具体输出为 `Proxy.entry.httpsDialLazyAttack` 的 error 级 `use of closed network connection`。SDK 增加集中分类：只有生命周期已经 closing **且**错误是 context cancel/deadline、已关闭 socket/pipe、EOF/reset/broken-pipe 或 WS close 才降 debug；运行中的相同断连仍 error，closing 时真实 TLS certificate/capture persistence 故障也仍 error。替换 entry/TLS/WSS/WS 转发及 attack 的相关日志入口，没有全局关闭 logger，也没有让所有 WS 1006 无条件变成无害。真实关闭 CONNECT/WSS 断言不再产生 error；运行中构造完整畸形 TLS record 和真实 WSS upstream 异常断连，等 error hook 事件后才 Close，证明真实故障不被关闭竞态掩盖；10 个 classification 子测试另检查 closing=false/true 下 cancel、socket、WS1006、TLS/capture 故障的真实 logger level。完整畸形 TLS fixture 先前不完整 record 仅触发等待，已修正测试输入；WSS fixture 改为等待 error hook，而不是读到客户端断连就抢先 Close。

M2 文档：`third_party/PATCHES.md` 改成“两个 scoped SDK module-cache **source files** 未写入/补丁”；显式保留初轮缺失 transitive module 下载超时留下 `samber/lo/@v/v1.53.0.lock` 的例外。没有删除/修改宿主 cache 或声称所有缓存零变化；本轮成功 gates 仅进程范围 `GOPROXY=off`，没有再次下载或 Windows race 宿主排障。

Fix1 Windows 最终 focused gate（2026-10-12，Go1.26.3，0 fail/0 skip）：

| 范围 | top-level | leaf | test pass events |
|---|---:|---:|---:|
| SDK lifecycle（含 Fix1 5 个新增 top-level） | 19 | 33 | 37 |
| SDK 原 HTTP/TLS/h2/WS/SSE 协议 | 14 | 25 | 39 |
| root traffic 全包 | 44 | 47 | 48 |

准确命令：

```text
# cwd third_party/go-mitmproxy
go test -json ./proxy -run 'TestReserved|TestClose|TestClientDisconnect|TestConcurrentServe|TestListenerOwnership|TestAttackerListener|TestShutdownDeadline|TestTransparentCONNECT|TestServeFailure|TestTransferWaits|TestColdStart|TestClosing|TestRunning|TestLifecycleError' -count=1 -timeout=60s
go test -json ./proxy -run 'TestConnection|TestProxyClose|TestProxyShutdown|TestWebSocket|TestSSE|TestAttackerHTTP2' -count=1 -timeout=120s
go test ./proxy -run '^$' -count=1 -timeout=60s
go vet ./proxy
# cwd root
go test -json ./traffic -count=1 -timeout=90s
go test ./... -run '^$' -count=1 -timeout=120s
go vet ./...
python third_party/verify_upstream.py --archive <original-v1.9.2.zip>
git diff --cached --check
```

上述 compile/vet/diffcheck 均 exit0。原协议 command 仍只在测试进程内覆盖 USERPROFILE 到新 temp 并 finally 恢复，避免触碰原默认 CA；新测试均独立 temp/loopback targets。JSONL、root-compile.txt、summary.json、committed-provenance.txt 放在 `task-5a1-fix1-validation/`，按 controller 授权不强制提交本轮忽略目录证据；原始首轮失败及 controller Linux日志没有覆盖/重写。

Fix1 provenance 仍为 104 upstream、98 unchanged、6 changed、2 added、0 deleted。刷新 module-relative patch/manifest 的 patched/new SHA-256；baseline/license/docs/go.mod/go.sum/version 原字节不动。原 zip 应用刷新后的 git apply patch 得到 106 SDK 文件，全部与 native-tested source byte相等；fixed `ffadaa4` 的 Git archive 再经 verifier/archive 校验，106源文件也全部与 tested worktree相等。没有把 log/tests 修复当成未改 upstream，manifest按原baseline准确累计本项目变更。

Fix1 self-review：确认 h2 路径没有再读取 Serve 会写的 BaseConfig；h2 Handler/context、max concurrent streams、socket/callback/WG gate与既有关闭所有权保留；日志降级同时受 closing 和 typed-cause 两条件约束，真实TLS/WS/capture fault仍可观察；测试logger只在串行case使用，hook事件等待真实error而非sleep竞态；来源陈述与已披露lock例外一致；仅本人 SDK/patch/manifest/PATCHES.md 暂存，controller 未提交文件保持原样。尚须 controller 固定修复 SHA Linux race GREEN 与完整复审通过，才能关闭 I1 和标记 Task5a1 验收完成。

### Fix1 继续验证：Go1.27 共享 transport 冷并发

Controller 对中间源码 `ffadaa4` 的 native Linux1.27.2 nested gate 没再出现 WARNING DATA RACE，旧 reserved h2/close+h2 通过，但新增 `TestColdStartConcurrentH2Capture` 的8共享transport请求全失败（unexpected EOF/captures=0）；完整原证据为 `linux-proxy-fix1-race.log`，没有覆盖。没有把 Windows1.26成功当作 Linux1.27成功，也没有拆分为独立client/transport、增加超时或sleep来弱化 fixture。

按最早不确定阶段继续调查：只在 owned `task5a1-fix1-debug` Linux1.27.2 容器的 `/work/source` 临时副本加入一处 DialTLSContext trace；SDK source与host module-cache/download 均以 readonly 挂载，GOMODCACHE在容器 `/tmp/modulecache`，GOPROXY为只读file cache。Trace 证明同一 client address 和同一 upstream `*tls.Conn` pointer 被重复返回8次，随后上游socket关闭、请求context被取消。恢复 `BaseConfig: a.server` 的相同普通test仍 FAIL，排除了“移除BaseConfig默认配置造成协议退化”。记录 `task-5a1-fix1-validation/linux-coldstart-diagnostic.log` 与 `linux-coldstart-with-baseconfig.log`，诊断没有加入项目生产源码。

版本边界解释：x/net v0.55.0/v0.58.0 在Go1.26走旧 `client_conn_pool`，按目标合并进行中的dial；Go1.27通过 `transport_wrap.go` 交给 net/http.Transport管理，多次并发dial合法。上游SDK把已握手的同一socket永久当作“新dial结果”返回，在Go1.27导致同一TLS流被多次ClientConn初始化/被冗余dial关闭。缺少本地 `golang:1.26.3-alpine` 镜像，没有下载新版本或换版本代替修复；Windows1.26与Linux1.27的构建分支、同socket trace及恢复BaseConfig对照共同构成根因证据。

继续修复 `93f4ddc`：每个 tunnel 对其已握手 upstream TLS socket只调用一次 `NewClientConn`，令其 `ClientConn.RoundTrip` 承担多路请求，不再通过DialTLSContext反复归还同一个socket。`http2.ConfigureTransports` 先初始化兼容Go1.26/1.27的adapter，DisableCompression仍true；独立 h2 server配置和显式owned Context/Handler仍保留。一次临时零值Transport.NewClientConn尝试在Go1.27 wrapper发现未初始化t1的panic，已定位并使用公开ConfigureTransports纠正；该中间尝试未提交，不忽略其诊断。初始化失败明确关上下游并error日志，既有raw registry/callback/handler关闭与join边界不变。

同一8请求shared transport fixture在owned Linux普通运行连续3次PASS，`-race` 连续3次PASS（1.359s，无WARNING）；最小命令 `go test -race -v ./proxy -run '^TestColdStartConcurrentH2Capture$' -count=3 -timeout=60s`，日志 `task-5a1-fix1-validation/linux-coldstart-singleconn-race.log`。该结果只证明最小回归；controller 对最新源码完整focused+原协议+traffic native race仍是关门条件。容器内gcc/musl仅用于自身测试，没有改宿主MinGW/PATH或cache。

最终 Windows重新运行完全相同focused命令，lifecycle19top/33leaf/37events、原协议14/25/39、traffic44/47/48均0fail/0skip；nested proxy compile/vet及root full compile/vet全exit0。新日志为 `proxy-lifecycle-final.jsonl`、`proxy-upstream-final.jsonl`、`traffic-final.jsonl`、`root-compile-final.txt`、`summary-final.json`（在本轮忽略证据目录，不forceadd）。刷新patch/manifest仍104/98/6/2/0，latest固定Git archive经原zip+verifier以及实际git apply replay验证，106SDK原始byte完全等于native-tested worktree，见 `committed-provenance-final.txt`。Controller可对93f4ddc独立验收，无需测试未固定workingtree；随后报告commit仅文档。
