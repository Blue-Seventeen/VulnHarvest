# Task4b implementation report

Status: DONE within the Task4b boundary. Full archive restore integration belongs to Task4c; application startup/recovery gating belongs to Task5; full runtime/authenticated HTTP and model-runner acceptance belongs to Task7.

Workspace: `C:/Users/cy/.codex/worktrees/portable-v0-0-1/Hack3rX_2026_011_ARTEX`.
Branch: `codex/portable-v0.0.1`; implementation base: `b9352bd`.
Commit subject: `feat(portable): adapt findings evidence and retests across stores`.

## Implemented behavior

- Findings retain their standalone IDs and exploration-node relationship. SQLite filtering uses explicit JSON membership/empty-or-missing-asset predicates and escaped Unicode case-insensitive search. Pagination, grouping, export, severity/status counts, cold aggregate totals, asset-tree parent reconstruction and company attachment use the same business predicates.
- Asset-tree ID/host batches use explicit JSON parameters and `json_each` on SQLite, preserving PostgreSQL's native arrays and SQL. Aggregate finding timestamps use the existing time scanner because SQLite aggregate expressions lose declared DATETIME metadata.
- Finding name/severity/class edits update the finding row and node payload in one transaction. JSON paths/column names remain code-owned whitelist values. Node-write failures propagate and roll back the finding update; rows-affected and public setter APIs remain intact.
- SQLite retest creation uses the same immediate transaction for finding/assets/constraints snapshot, active-run deduplication, conversation, retest and first user activity. Its row codec preserves SQL NULL, nested JSON/arrays, UTC timestamps and integers above 2^53 without float64 conversion. PostgreSQL retains the original row-to-JSON snapshot query.
- Retest snapshot binds/scans use `jsonValue` / `JSONScanner`. Finish locks use explicit dialect SQL and pass the owner to the existing status/notification fact transaction. Session-deletion stop behavior, staged verdicts, history immutability and restart interruption are preserved. No model execution or automatic continuation was added.
- Package-level evidence lock helpers and `RecordFindingTx` accept an optional explicit DB owner, preserving legacy PostgreSQL calls. All portable call sites pass the owner. SQLite uses the existing native immediate writer transaction; PostgreSQL retains its real evidence advisory lock and row locks. No SQL rewriting, global transaction registry or database fallback was introduced.
- Binding metadata loads via fixed typed columns instead of PostgreSQL `to_jsonb(s)`. Snapshot/body identity, deduplication, binding order, version CAS, report-version sentinel values and independent evidence-body retention remain unchanged. GC aliases use SQL accepted by both stores.
- `restoreFindingTrafficTx` restores validated snapshots first, then bindings through a fixed column whitelist with typed IDs, positions and UTC timestamps. It rejects cross-task bindings, duplicate/tampered snapshot metadata and invalid binding columns. Existing archive-version normalization is preserved and tested.
- `evidence.WithInstalledSnapshotsTx(ctx, snapshots, source, func(*sql.Tx) error)` exposes the pinned evidence transaction. The old zero-argument callback wrapper remains for compatibility. The new contract actually writes findings/bindings through this transaction while GC in a separately opened DB pool waits; after commit GC preserves the referenced bodies.
- Windows body publication now uses staged body `File.Sync`, close, hash/length verification and `MoveFileEx` with `MOVEFILE_WRITE_THROUGH`. POSIX retains atomic rename followed by directory fsync. Publication, permissions, body sync and verification errors are not swallowed.
- Retester seeding skips only the PostgreSQL advisory-lock SQL on SQLite. Traffic/workflow seeders decode JSON with `UseNumber`, fill missing optional fields and CAS the original loaded schema. Binding migrations compare decoded known legacy lists by semantics/order-independent membership, then CAS the original loaded bytes/value. Custom reader/binder lists, enabled flags, descriptions, existing fields and prompts survive. Successful one-time flags preserve later user unbinding.
- Engine FK/unique/check/busy classification uses the shared DB classifiers. Existing PostgreSQL FK/unique log strings remain compatible.

## Intentional PostgreSQL corrections proven during this task

1. Original `setFindingCol` ignored a node-payload update error after committing the finding update. A real PostgreSQL trigger rejecting the mirror produced RED `payload failure was ignored`. Both writes now share one transaction, with the same real assertion in both stores.
2. Original unconditional reporter-binding additions modified custom `get_finding_traffic` and `bind_finding_traffic` lists when migration flags were absent. These additions now apply only to known old defaults. An existing `worker` reader default still migrates to `worker/reporter`; custom `pentest` lists remain unchanged.
3. JSON decoding into `map[string]any` rounded an existing user schema number `9007199254740993` to `9007199254740992`. Seeders now use `UseNumber` and preserve the exact number.
4. A concurrent task deletion could commit between the finding's initial `task_id` read and the parent task row lock. The stale parent lookup returned `sql.ErrNoRows`, defeating idempotent finding deletion and retained/global finding operations. Only this parent-not-found result now proceeds to the authoritative finding lock. Missing findings become `ErrFindingNotFound`; retained findings whose FK became NULL remain usable; all other parent-query errors propagate unchanged. No generic retry or relaxed assertion was added.

## Native SQLite 1811 distinction

`IsForeignKeyViolation` recognizes native `*sqlite.Error` code 787, plus code 1811 only with the exact native diagnostic:

`constraint failed: FOREIGN KEY constraint failed (1811)`

SQLite implements `ON DELETE RESTRICT` using an internal trigger. modernc exposes its extended code and message but no internal-vs-user-trigger provenance. The real driver tests cover inserted missing parents, native RESTRICT deletion, wrapped errors and an unrelated user `RAISE(ABORT,'user rule rejected')` trigger. The latter also uses 1811 and is correctly not classified as FK. A deliberately authored user trigger with exactly the native FK diagnostic is indistinguishable through this driver API; this limitation is documented centrally rather than introducing business-layer message matching.

## TDD evidence

Focused RED commands were followed by focused GREEN runs; production code was restored after deliberately checking the original setter/installation implementation.

1. `go test ./db -run 'TestSQLiteConstraintClassification|TestPortableFindingContract/SQLite' -count=1 -timeout=90s`
   - RED: real SQLite failures at `FOR`, `ILIKE`, `@`, PostgreSQL casts; native RESTRICT error was 1811 and classified false. This proved missing SQLite behavior with existing business assertions.
   - GREEN after explicit dialect SQL/snapshot changes: DB package PASS, 2.723s.
2. `go test ./evidence -run 'TestPortableEvidenceContract/SQLite' -count=1 -timeout=60s`
   - RED after owner routing exposed the original Windows directory sync failure: `sync .../evidence/blobs/...: Access is denied.`
   - GREEN after platform publication helpers: evidence package PASS, 2.754s.
3. `go test ./server -run 'TestPortableFindingServerContract/SQLite' -count=1 -timeout=90s`
   - RED: absent SQLite `pg_advisory_xact_lock` and native FK classified as `write_error`.
   - GREEN after seed/classifier adaptation: server package PASS, 1.944s.
4. `go test ./db -run 'TestPortableFindingContract/SQLite/TrafficArchiveRows' -count=1 -timeout=60s`
   - RED: `json_populate_recordset`/casts rejected by SQLite.
   - GREEN after fixed typed insertion: PASS, 1.521s.
5. `go test ./db -run 'TestPortableFindingContract/(SQLite|PostgreSQL)/AtomicPayloadMirror' -count=1 -timeout=60s`, temporarily restoring only the original setter.
   - RED: SQLite payload remained `name:original` after rename; PostgreSQL trigger failure was ignored.
   - Restored the atomic setter; focused two-store payload/snapshot suite passed, 3.823s.
6. `go test ./evidence -run 'TestPortableEvidenceContract/SQLite/InstalledSameWriter' -count=1 -timeout=60s`, temporarily restoring the old installation-callback implementation while retaining the platform publication repair.
   - RED: `missing same-writer evidence installation seam`.
   - Restored the sharing seam; focused two-store suite passed, 2.898s. Later strengthened to use a separately opened collector pool; both stores passed, 2.563s.
7. `go test ./server -run 'TestPortableFindingServerContract/SQLite/Seed' -count=1 -timeout=90s`
   - RED: a custom `["pentest"]` reader list became `[pentest reporter]`.
   - GREEN after restricting additions to known legacy defaults: PASS, 2.072s.
8. `go test ./server -run 'TestPortableFindingServerContract/SQLite/Seed' -count=1 -timeout=60s`
   - RED: the preserved custom schema number was rounded to `9007199254740992`.
   - GREEN with `UseNumber`, both stores: PASS, 3.213s.
9. First complete scoped gate found one failing DB leaf: PostgreSQL `ConcurrentTaskOperations` returned `sql: no rows in result set`. Evidence's 27 events and server's 13 events passed. The DB's three fail events included its enclosing subtests.
   - Deterministic RED: `go test ./db -run 'TestPortableFindingContract/(SQLite|PostgreSQL)/TaskDeleteBeforeFindingLock' -count=1 -timeout=60s` used the actual task-deletion preparation barrier and observed PostgreSQL's real task-row waiter through `pg_stat_activity`. Both delete-finding and retain-finding cases reproduced the stale-parent result; rollback retained the parent as expected.
   - GREEN after the narrow parent-not-found correction: DB concurrency suite PASS, 5.439s. The evidence task-operation contract, preserving expected missing-source errors for creation and strict bind/delete behavior, passed both stores, 3.141s.

An initial added rich-snapshot PostgreSQL fixture used a JSON string for an actual `JSONB[]` field and failed with `22P02`; the fixture was corrected to use the existing `[]string` JSONB-array parameter form. This was a fixture error, not a schema/product change. Intermediate Python restore output briefly changed LF to CRLF; final gofmt restored LF, and diffcheck is clean.

## Final verification

All PostgreSQL runs used explicitly supplied `ARTEX_FINDING_TEST_PG_DSN`; `ARTEX_PG_DSN` was set to the same dedicated test endpoint so package TestMain did not rely on application config. The endpoint is `127.0.0.1:27347/vh_findings`. Each contract reset first checks `current_database() == 'vh_findings'`. No other database was reset.

Final amended scoped gate, once after the last code/test amendment:

```powershell
go test -json -p 1 ./db ./evidence ./server -run '^(TestPortableFindingContract|TestPortableEvidenceContract|TestPortableFindingServerContract|TestSQLiteConstraintClassification|TestFindingArchiveVersionNormalization|TestFindingEvidenceLockNamespace|TestFindingTrafficLegacyArchiveDefaults)$' -count=1 -timeout=180s
```

| Package | Passed test events | Failed | Skipped | Package elapsed |
|---|---:|---:|---:|---:|
| db | 90 | 0 | 0 | 25.055s |
| evidence | 27 | 0 | 0 | 34.678s |
| server | 13 | 0 | 0 | 14.152s |
| Total | 130 | 0 | 0 | 3 package PASS |

Counts include enclosing subtest pass events, matching the controller's baseline reporting convention. Raw JSONL was kept outside source at `%TEMP%/artex-task4b-amended-gate.jsonl`; the first gate is `%TEMP%/artex-task4b-final-gate.jsonl`. Neither log nor credentials are committed.

Additional final checks:

- `go test -p 1 ./... -run '^$' -count=1 -timeout=180s`: exit 0, all 17 packages compile/link; 15 report no selected tests and 2 have no test files. This is compilation coverage, not the full project test suite.
- `go vet ./...`: exit 0, no diagnostics.
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o <owned-temp>/artex-task4b-evidence-linux.test ./evidence`: exit 0. This checks POSIX helper compilation; POSIX runtime/fsync was not executed on this Windows host.
- Explicit touched-file `gofmt` and `git diff --check`: clean. Final staged diff is checked before commit.

## Contract coverage and boundaries

The two stores run the same existing findings pagination/groups/stats/asset-tree/follow-up and retest dedup/history/deletion/validation/triage assertions, plus meaningful new typed snapshots, restart, cold stats, archive-row validation, atomic mirror failure and concurrent deletion tests. Evidence contracts cover real traffic capture fixtures, inline/spilled/legacy binary bodies, deduplication, version/report CAS, export after original traffic removal, SQL/file failures, failed new-binding rollback, shared-body GC, native peer-pool GC exclusion and same-task creation/deletion/bind/retest concurrency.

Server contracts use minimal injected owned DB/traffic stores and real handlers. They reuse every assertion in `TestFindingTrafficAPIAndExport`, including body downloads, cross-finding access rejection, ZIP binary/link integrity and corrupt-body export failure; `TestRetestActiveStatusLifecycle`; and `TestRetestValidationSeedingAndScope`. Seed contracts additionally prove preserved custom definitions/prompts/bindings, exact large numeric schema values, missing-field upgrades, reordered/whitespace legacy arrays, and no re-add after successful migration and later unbinding. Native engine errors cover FK, unique, check and SQLite busy.

These fixtures do not invoke `NewManager`, `New`, the authenticated full `Handler`, a notification dispatcher, model runner, live LLM, MCP or application background services. Full authentication/middleware/startup/full-runner assertions remain Task5/Task7's acceptance work. No dynamic-port/UI/runtime recovery changes were made. The existing installed ARTEX directory was not accessed; only this worktree, owned temp files/directories and the authorized disposable test database were used.

## Task4c transaction handoff

Use `WithInstalledSnapshotsTx` for archive metadata restoration and pass its native `*sql.Tx` into the actual DB restore operations. Do not call another DB writer/`Begin` while its SQLite callback is active. The pin/GC protection remains held through metadata commit; the test writes real findings/bindings and lets GC from another pool proceed only after that commit. External progress updates and final result loading may occur outside this transaction. The old zero-argument wrapper cannot safely wrap SQLite callbacks that reacquire the writer; the existing full archive call site is intentionally Task4c's integration responsibility.

No claim of complete archive round-trip or application runtime acceptance is made by this report.

## Files changed / self-review

- DB: `errors.go`, `findings.go`, `finding_assets.go`, `finding_retests.go`, `finding_retest_snapshot.go`, `finding_traffic.go`, `finding_traffic_archive.go`; matching findings/assets/retest tests and `findings_portable_test.go`.
- Evidence: `store.go`, `publish_windows.go`, `publish_posix.go`, `store_test.go`, `portable_test.go`.
- Server: `engine.go`, `finding_retests.go`, `finding_traffic.go`, `finding_workflow.go`; minimal factory routing in existing traffic/retest tests and `findings_portable_test.go`.
- This report; no `implementation/*.md`, schema, runtime/UI or unrelated source file is staged.

Self-review checked every brief requirement against the diff, owner propagation to evidence transaction calls, JSON precision/null/time handling, failed-binding/mirror rollback, exact native error distinction, native peer-pool evidence protection and test resource shutdown/join ordering. The two intentional seed corrections and the pre-existing PostgreSQL mirror/stale-parent corrections are described above. Independent diff review is the controller's next step after this commit.
