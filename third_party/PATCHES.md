# Scoped SDK patches

## norma v0.4.3 — portable-tool-selection-1

The root still requires `github.com/Autumn-27/norma v0.4.3`, with only a scoped
source replacement to `./third_party/norma`. All 210 original archive files
were copied and compared against the read-only cache, preserving raw bytes.
204 are unchanged; six upstream files are patched, seven files added, none
deleted. The original module declaration, go.mod/go.sum, docs, versions and
available copyright remain unchanged. No transitive module is vendored.
The published archive contains no LICENSE/COPYING/NOTICE: this absence is
recorded accurately and no upstream license is invented. The compressed zip
SHA-256 is `fd6e096f5ee5399b51d26fb6c3b7a43c04b5dd43bb1807dc1fe676d63e24f720`;
its recorded Go content hash is `h1:DnmOhJPL6OZSMnCVYO5Ujh4yVINuDarEickVi9jx1hI=`.
The existing proxy manifest entry and raw hashes are retained unchanged.

`patches/norma-v0.4.3.patch` is the exact module-relative baseline diff,
separate from the copied source. `verify_upstream.py --module norma --archive
<original.zip>` checks every original byte/hash, the copy/change/add lists,
and the exact patch. Git `-text` attributes preserve raw-byte provenance.

- `tool/runtime_execution.go` adds immutable process-global selection with
  validation/deep copies, one atomic publication, absolute command construction,
  generation PATH and environment merging. Its single owner must join consumers
  before clearing it. Existing PG execution remains legacy until publication.
- `tool/bash.go`, `tool/task.go`, `tool/shell_session.go` use the same selected
  interpreter for sync/background/PTY. Windows PTY remains unsupported; Unix
  uses its existing PTY. Snapshot sync timeouts stop relay descendants and bound
  output-pipe waiting. Windows PowerShell removes its built-in curl alias only
  inside the child, so bare curl obeys selected PATH.
- `tool/ripgrep.go` removes automatic npm installation/root commands in every
  mode, retaining Go fallback. Published selection bypasses legacy sync.Once;
  actual rg launch captures both the selected executable and PATH together.
- `mcp/stdio.go` adds `NewStdioClientWithCommand`, validates unstarted pipes,
  cleans partial construction failures and preserves handshake-only context.
  `stdio_process_{unix,windows}.go` owns process-group/tree termination; Close
  stops the tree before closing stdin (a package runner may exit on EOF), then
  waits its child. Explicit user package commands remain supported.
- Tests: actual selected foreground/background/PTY or honest Windows error,
  Go grep/no npm sentinel, real stdio initialize/tools/list/tools/call, handshake
  cancellation and package-runner descendant heartbeat shutdown.
- Task5b lifecycle adds admission/closing gates and joins process-output/size,
  PTY reader, dispatch and monitor owners before deleting their session output.
  Exact changes are in `tool/task.go`, `tool/shell_session.go` and
  `tool/shell_tools.go`; `tool/runtime_cleanup_test.go` validates actual child
  completion, output directory cleanup and rejected post-Cleanup launches.

App-side `runtimeenv` owns Check, read-only metadata/safe bounded known version
probes, per-generation native aliases, fallback and lifecycle. npm/npx .cmd are
translated only for known npm Node CLI layouts using this generation's Node;
unknown batch wrappers remain unresolved. Custom MCP and browser detection
never execute business args, package requests, GUI or service flags. Versions
retain only parsed version numbers. Optional absence does not prevent Check.
Aliases expose selected tools only and explicitly fail unavailable names;
runtime/bin is removed from child host PATH, old generations persist to Close.
Windows aliases are copies (running image hardlinks cannot be deleted); Unix
prefers hardlinks and relay uses exec. `cmd/artex` dispatches aliases before any
flags/update/lock/storage work. Task5b constructs the owner in both portable and
postgres startup, and authenticated runtime/check publishes actual selections.

Server Python and explicit MCP launches use the published execution selection.
MCP proxy/CA overlay is session-only and preserves saved args/env; PG's legacy
path remains when no snapshot is published. Application integration initializes
and checks before tool use, supports explicit rescan and suppresses automatic
MCP discovery on portable startup. It joins consumers before ToolManager.Close.
The SDK patch itself does not implement the application's HTTP API or recovery.

Windows focused behavior gates passed, including real shell curl alias and
Chinese/space argv; the original complete norma tool suite had three existing
Windows failures, reproduced against the pristine archive (Unix shell syntax
in two tests and legacy Cleanup completion). The Task5b owner patch fixes
Cleanup completion; the two Unix-shell-syntax tests remain Windows limitations.
Native Linux/race/PTY gate is
separate; full app business/LLM acceptance belongs to Tasks5b/7.

## go-mitmproxy v1.9.2 — portable-proxy-lifecycle-1

`go.mod` still requires upstream `github.com/lqqyt2423/go-mitmproxy v1.9.2`;
only its source location is replaced with `./third_party/go-mitmproxy`. The
upstream module declaration, Go directive, `Proxy.Version`, MIT license,
copyright, dependency manifests, documentation and all upstream tests are
retained. No transitive dependency is vendored. No source files in either
scoped SDK's module-cache directory were patched or written. An initial full
nested-module compile attempted to download a missing transitive module and
left a `github.com/samber/lo/@v/v1.53.0.lock` cache file before timing out; the
Task5a1 report records this exception. No host-cache cleanup was attempted.
Source evidence is the cached Go zip, compared offline, not a new online
SumDB check.

`upstream-manifest.json` records every one of the 104 upstream files using raw
byte size/SHA-256, the compressed zip SHA-256, Go h1 content checksum, and all
changed/added/deleted files. Eleven upstream files are changed; two lifecycle
files are added; 93 files remain byte-for-byte unchanged; no files are deleted.
The narrowly scoped `-text` Git attributes preserve those bytes across Windows
and other checkouts. `patches/go-mitmproxy-v1.9.2.patch` is the standalone
module-relative baseline-to-patched unified diff, including new tests.

Changes:

- `proxy/proxy.go`: keep Start, reserve TCP listener before starting attacker;
  track outgoing sockets and expose a retained-listener Serve via lifecycle.
- `proxy/lifecycle.go` (added): one-start/closing gate; Serve owns non-nil
  listeners on success/failure/rejection; stop/join sockets, HTTP/CONNECT/h2
  handlers and addon disconnect callbacks; Close and Shutdown wait for real
  completion. The caller owns and joins its own goroutine running Serve.
- `proxy/entry.go`: preserve wrapListener and ConnContext; track clients and
  connect upstream socket lifetimes without reading mutable ConnContext during
  Close. Closing sockets never invokes addons under the registry mutex.
- `proxy/attacker.go`: close-wake listener without closing send channel; owned
  request contexts; h2 service+watcher joining; TLS worker cancellation/join;
  register outgoing callback owners, including first/lazy TLS capture. H2 uses
  x/net's independent per-connection default configuration, retaining explicit
  handler/context instead of racing HTTP Serve's BaseConfig initialization.
  Each tunnel initializes one HTTP2 ClientConn on its already-handshaken
  upstream socket, so Go 1.27 transport dials cannot wrap/close that same socket
  multiple times. ConfigureTransports initializes the cross-version adapter;
  ClientConn.RoundTrip multiplexes the requests without redialing the socket.
- `proxy/connection.go`: atomic close-after-response flag used by close
  callbacks; atomic internal TLS flag lives in wrapClientConn.
- `proxy/helper.go`: close both endpoints and wait for both copy workers,
  including the first-error path; only closing + expected cancellation/socket
  disconnection lowers lifecycle errors to debug. Running TLS/WS failures and
  capture faults, including faults during shutdown, still log at error level.
- `proxy/websocket.go`: track WSS upstream during handshake; close both ends,
  join both message workers before WebSocketEnd; remove unbounded deferred
  WriteMessage close frames. Normal forwarded messages are unchanged.
- `proxy/listener_lifecycle_test.go` (added): local reserved-listener HTTP,
  HTTPS, h2, WSS, transparent CONNECT payload, stalled TLS, callback/copy joins,
  listener failure/ownership and concurrent Close regression tests.
- `proxy/proxy_test.go`, `proxy/sse_hook_test.go`, `proxy/sse_realtime_test.go`,
  `proxy/sse_test.go` and `proxy/websocket_test.go`: isolate all 15 proxy CA
  constructors and the two direct CA constructors in `t.TempDir()`. The
  untrusted-certificate assertion checks `x509.UnknownAuthorityError` rather
  than a platform-specific message. TLS, SSE and WebSocket behavior assertions
  remain intact. Two HTTP immediate-close baseline failures remain a separate
  response-completion correction; full proxy success is not claimed here.

Shutdown is deliberately stop-and-join, including live tunnels. It shares
Close's force-stop boundary; an expired context is returned only after all
owners exit. An addon must return and must not synchronously call Close from
its own callback. There is no safe way to forcefully terminate arbitrary Go
addon code. The traffic recorder's addons satisfy this owner contract.

Application integration is in `traffic/traffic.go`: Serve publishes the
retained listener's actual address; rejected calls cannot overwrite it. Close
signals reclamation, force-closes/joins the proxy, drains active mutation/stage
owners under the write-lock barrier, joins reclamation, then closes SQLite.
Start keeps configured fixed-port behavior and binds before delegating to Serve.
Server/Manager lifecycle composition belongs to Task5b.

Verification commands:

```text
python third_party/verify_upstream.py
python third_party/verify_upstream.py --archive <original-v1.9.2.zip>
cd third_party/go-mitmproxy
go test ./proxy -run 'TestReserved|TestClose|TestClientDisconnect|TestListenerOwnership|TestAttackerListener|TestShutdownDeadline|TestTransparentCONNECT|TestServeFailure|TestTransferWaits' -count=1 -timeout=60s
```

Root `go test ./...` does not traverse this nested module. Run its proxy tests
separately. Task5a1's report records actual native Windows gates and the failed
Windows race-runtime attempt; Linux race verification is a controller gate.
Task5a2 must append norma's provenance to the `modules` list without discarding
this module's baseline and patch evidence.
