# Task5a1 实施与验证报告

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

## Provenance、patch 及 self-review

- 原版本/模块声明、Go directive、MIT LICENSE（Copyright (c) 2020 liqiang）、docs/tests/dependency manifests 保留；只复制 go-mitmproxy，未 vendor transitive dependencies。根 require 仍为 v1.9.2，只增本地 replace。
- `third_party/upstream-manifest.json` 为 `schemaVersion:1, modules:[...]`；后续 5a2 在 modules 列表追加 norma，不覆盖此记录。每个 upstream file 有 path/size/raw SHA-256；changed 有 upstream/patched SHA-256 和 patchID；added 有 size/hash；deleted 单列为空。
- 104 baseline、98 unchanged、6 changed、2 added、0 deleted。Changed：proxy/attacker.go、connection.go、entry.go、helper.go、proxy.go、websocket.go；added：proxy/lifecycle.go、listener_lifecycle_test.go。未改文件全部逐 byte 相等，绝不以行尾规范化冒充 unchanged。
- Zip SHA-256 `0abd197455a47fc6045176cb29727959e51686de62d8bcd09be000236c3744d2`；Go h1 与 root go.sum/cache .ziphash 同为 `h1:+QTbDyUUaVRmEavPq0/eWzJ2K+K4JdL4ff91s/adnxk=`。仅离线验证，不宣称本次 SumDB 联机验证。
- 独立模块相对 patch 为 `third_party/patches/go-mitmproxy-v1.9.2.patch`；pristine zip 应用 git apply patch 后，106 patched 文件全部与 native-tested worktree byte-for-byte 相等。`third_party/verify_upstream.py --archive <zip>` 可复核原 archive/file hashes、全部 change lists、patch hash 与精确 baseline-to-copy diff；无网络或 cache 写操作。
- source 固定提交的 Git archive 与 `core.autocrlf=true` 全新 sparse clone 均再次通过 archive/manifest/patch 校验，106 文件也均与 native-tested source 完全相同，见 `committed-provenance.txt`。`.gitattributes` 狭义 `-text` 覆盖该 SDK/patch/manifest；保留原 README 等 6 个未改文件的 whitespace 例外及 patch context 语法例外，modified/new Go 源仍正常 whitespace 检查。全 staged/unstaged diffcheck exit 0。
- Self-review 分来源层/完整 module-relative patch/app integration 三层完成：确认同一 closing gate 禁止 Wait 期间 Add；普通/hijacked、TLS/h2、copy/WS 与 disconnect callbacks 都有实际等待；callback 不在 registry 锁内执行；accepted-before-close 的 sink 在 index.Close 前完成；mutation barrier 与 reaping enrollments 共用 wmu；重复 Close 返回保存结果；listener reject/failure 不遗留 owner；PG 裸端口兼容已用真实 HTTP 验证。没有把 98 个未改 upstream 文件当作新增原创逻辑，也没有扩大到 5a2/5b 的生命周期归属。

本报告与验证日志的后续 commit 不改变 `f416cc2` 的生产/测试字节，controller 对该 fixed source 的 review/race 结论可直接应用。剩余验收是 controller 的独立来源/补丁/application review 与 Linux native race；5b 组合关闭时须遵循上述 ownership 和 stage 完成边界。
