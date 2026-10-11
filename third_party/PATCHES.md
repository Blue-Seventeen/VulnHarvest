# Scoped SDK patches

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
changed/added/deleted files. Six upstream files are changed; two lifecycle
files are added; 98 files remain byte-for-byte unchanged; no files are deleted.
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
