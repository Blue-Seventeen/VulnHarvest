# Task4c implementation report

Status: DONE within the archive/storage/package boundary. Task5 retains startup mode gating, full shutdown/restart policy and worker startup behavior. No synchronization or historical PostgreSQL import was added.

Workspace: `C:/Users/cy/.codex/worktrees/portable-v0-0-1/Hack3rX_2026_011_ARTEX`; branch `codex/portable-v0.0.1`; assigned base `02ec7967a36e13d807d24f7c8dfa9d1c892efa9d`.

## Implemented

- Archive queue/list/claim/restore/delete/progress/warnings explicitly select PostgreSQL or SQLite SQL. PostgreSQL advisory locks, row locks and asset/anchor table locks remain real. SQLite uses its native immediate writer transaction; read-only snapshots use the existing independent deferred reader pool. No global SQL rewriting, fake PG functions, new schema version or format version was introduced.
- Snapshot tables, ownership metadata and statistics use the same read-only repeatable transaction. `hostsForTaskDeletion` receives its owning DB and that same transaction. A real streaming writer callback updates archive progress and LLM usage while the snapshot remains open; both stores retain the old row/counter view and commit the independent progress write.
- SQLite uses code-owned table/column/type whitelists. SQL NULL, nested JSON objects/arrays, boolean values, UTC timestamps and integer `9007199254740993` survive snapshot and restore. `UseNumber` is retained in all archive row decodes, including asset metadata and aggregate JSON; trailing multiple JSON values are rejected. SQLite assets arrays are actual archive arrays. Streamed LLM INSERT statements are prepared/reused; stream writers detect short writes.
- `RestoreTaskArchiveTx` exposes the restore transaction body without acquiring/committing another writer. Standalone DB restore owns/commits its transaction; the actual server calls this body inside Task4b `WithInstalledSnapshotsTx`. Evidence bodies/GC are pinned through SQL metadata commit. Progress writes, warning activities, package consumption and task loads occur after the shared transaction ends.
- Database failures, including a real deferred-FK commit failure, roll back the task/graph/evidence metadata and the installed file journal. The package remains retryable. Evidence body files may exist unreferenced after rollback; the existing grace/GC policy handles these, rather than falsely claiming SQL rolls back physical files.
- After SQL commit, file-journal cleanup failure retains the unresolved extraction/journal and committed live files. Retry calls `recoverTaskArchiveRestoreStages` before consuming the package and never replays rows or reverses committed files. Precommit rollback failures are joined into the returned error and preserve the journal. Existing archive/install helper compensation errors are no longer swallowed.
- Natural-key asset merges, conflicting asset-ID remapping, anchors, finding asset arrays, node payload arrays and task-link provenance remain connected. Typed historical retest snapshot asset IDs/finding asset arrays receive the same remapping; historical text and other context stay intact. Company/profile/category disappearance keeps existing warnings/degraded-reference behavior. Restored retest conversations also clear unavailable profile references rather than failing their FK.
- Manifest table counts and streamed LLM counts are checked before/within restore; malformed table names/identities fail. Archive failure updates use the active-state CAS so stale failures cannot overwrite restore jobs. Cold delete dependency checks run in the same transaction. SQLite AUTOINCREMENT and side ordinal high-water marks do not reuse restored/existing IDs.
- Restored task/exploration stubs retain metadata, task remaining deadline, queue defaults and exploration round counter. Existing side `memory`/`context_info` additive defaults are preserved. Older packages may omit all newly covered tables.

## Narrow PostgreSQL compatibility corrections and relationship coverage

The original PG snapshot omitted `main_sessions`, `finding_retests`, their `conversations`/`conversation_activities`, and `task_intercept_rules`. These are now additive optional tables in the existing version-3 format in both stores. The task's retest conversation association additionally selects `conv-<ID>` LLM raw records, skill/tool ledgers, conversation-owned intercept approvals and conversation-owned side sessions/requests. These rows are compacted/restored in FK order. Task-associated LLM usage stays selected by task/exploration; conversation usage also remains in its restored activity token columns.

Current schema inventory contains 51 application tables plus the SQLite-only `portable_sequences` bookkeeping table. The archive whitelist/snapshot covers these 27 application tables:

`tasks`, `explorations`, `exploration_nodes`, `exploration_edges`, `exploration_anchors`, `task_intercept_rules`, `task_constraints`, `activity`, `task_relations`, `task_asset_links`, `task_llm_profiles`, `task_scope`, `main_sessions`, `finding_retests`, `conversations`, `conversation_activities`, `findings`, `finding_traffic_bindings`, `traffic_evidence_snapshots`, `llm_records`, `llm_usage`, `skill_usage`, `tool_usage`, `intercept_pending`, `side_question_sessions`, `side_question_requests`, `assets`.

Ownership selectors are task/exploration IDs, finding->retest->conversation IDs, explicit task asset membership plus task links/anchors/finding references, and finding->binding->snapshot. Conversation-owned side sessions follow retest conversations; their requests retain session key, ordinal, generation, run/version, raw answer, model identity, usage, memory and context. Graph nodes/edges, scope/source relationships, retest/conversation IDs and evidence version/binding IDs remain unchanged unless an asset-ID remap is required. The DB tests assert precise typed references after that remap.

The other 24 application tables remain in the primary DB and are not compacted:

`agent_prompt_vars`, `agent_prompts`, `agent_skill_visibility`, `agent_triggers`, `agent_visibility`, `agents`, `asset_intercept_rules`, `companies`, `company_scope`, `intercept_rules`, `llm_profile_health`, `llm_profiles`, `mcp_servers`, `mcp_tools_cache`, `notification_channels`, `notification_deliveries`, `notification_events`, `scheduler_state`, `server_logs`, `settings`, `task_archives`, `task_categories`, `task_templates`, `tools`.

These are global configuration/catalog/notification/runtime/history or retained cold metadata. Global models, API keys, prompts and tool configuration are not deleted or recreated; the actual model/API-key preservation test confirms this. Notifications have no finding FK and remain historical facts in the primary DB. Public archive aggregate fields contain only counters, labels, dates and skill/finding statistics; no report bodies/API keys are copied to that UI payload. `portable_sequences` remains in place and is raised to the restored ordinal maximum.

## File ownership and conversation admission

- Existing task `files/tasks/<taskID>` and exploration `exp<explorationID>-*` transcript paths are preserved.
- Exact retest `conv-<conversationID>` transcript, `sessions/conv-<conversationID>` upload/tool-output directory and `noa/chat-conv-<conversationID>` context directory are staged and restored to their original roots. Task `noa/exp<explorationID>-*` context is included.
- Conversation-side transcript naming is `exp0-btw-<requestID>`; exact request IDs selected from the owned conversations are staged. Other `exp0` transcripts are excluded. The basename whitelist rejects unsafe side IDs. Session/NOA restore rejects directories outside the archived exploration/conversation metadata.
- The real archive flow pins associated conversations before staging. Active chat, attachment writes or side runs are rejected; ordinary conversations are not automatically cancelled. Pending conversation-side snapshots are flushed; failed persistence rejects archive, retains the pending context and releases the pin.
- Real send, metadata/profile edit, single/batch delete, side ask/clear and attachment write entrances share the archive admission mutex and revalidate current conversation existence. A request that loaded a row before compaction cannot start a runner after the pin releases and the row has been deleted. Send after compaction is 404; pinned writes are 409.
- Lock order is `archiveConversationMu -> chatMu / side.commands / side.mu`. The mutex is never held while waiting for runners. Uploads register an in-flight counter under the mutex and release it before reading/writing the potentially large multipart body; archive rejects that counter rather than waiting under a global admission lock.
- New retest creation uses existing `LockFindingEvidenceTx` inside its creation transaction: task then finding, including archive-state rejection and retained/global findings' nullable-task behavior. Retest HTTP creation holds the same admission mutex until `chatBusy` registration, closing the DB-create-to-run-registration race.

## TDD evidence

1. `go test ./db -run 'TestPortableArchiveContract/SQLite' -count=1 -timeout=60s`
   - RED: `SQL logic error: near "FOR": syntax error (1)` at archive queue, before dialect support.
   - GREEN: SQLite focused roundtrip PASS after typed/dialect adaptation (1.356s initially); richer dual-store roundtrip subsequently PASS 3.423s.
2. Temporarily remove only the four original missing session/retest snapshot entries; run `go test ./db -run 'TestPortableArchiveContract/PostgreSQL' -count=1 -timeout=45s` with the dedicated archive DB.
   - RED: `snapshot omitted retest/session associations`; package FAIL 2.464s.
   - Restored additive entries; both stores pass the richer suite. Evidence `%TEMP%/artex-task4c-pg-omissions-red.txt`.
3. Temporarily replace only actual server shared callback with the previous zero-argument `WithInstalledSnapshots` and nested standalone DB restore; run `go test ./server -run 'TestPortableArchiveServerContract/SQLite/SuccessGC' -count=1 -timeout=22s`.
   - RED: `restore stalled before actual metadata write` at the bounded 15-second restore context; package FAIL 16.827s.
   - Restored sharing callback; SQLite and PG actual server scenarios PASS. Evidence `%TEMP%/artex-task4c-server-writer-red.txt`.
4. Added assertion for task intercept rule and conversation-only raw record ownership before widening selectors.
   - RED: `snapshot omitted retest/session associations` with missing task-intercept count/conversation raw record.
   - GREEN after explicit selectors; both stores PASS. Later assertions cover conversation skill/tool usage and restored raw record bytes.
5. Temporarily remove the new retest task/archive guard; run `go test ./server -run 'TestPortableArchiveServerContract/(SQLite|PostgreSQL)/ConversationAdmission' -count=1 -timeout=60s`.
   - RED in both stores: `late retest admitted: <nil>`.
   - Restored guard; dual-store GREEN 4.871s (later upload-counter variant GREEN 2.976s). Evidence `%TEMP%/artex-task4c-retest-admission-red.txt`.
6. Temporarily remove active-conversation/side checks in the archive pin helper while retaining the new retest guard; run the same dual-store conversation admission command.
   - RED in both stores: `active retest archived`; package FAIL 3.196s.
   - Restored checks; real archive rejection/idle retry, failed checkpoint persistence, seven concurrent write endpoints, bulk delete, stale-row revalidation and fresh send404 pass. Evidence `%TEMP%/artex-task4c-active-conversation-red.txt`.

Intermediate fixture mistakes were corrected before treating output as product evidence: an undefined test formatter, a traffic length literal of 24 instead of the actual 23 bytes, and dynamic SQLite scalar registration after opening the writer connection. Tests now renew that owned writer connection and use unique function names. No production driver behavior was changed for these fixture problems.

## Final verification

All executed PG fixtures use `127.0.0.1:27347/vh_archives`; contract reset checks `current_database() == 'vh_archives'` before any schema reset. Package TestMain's DSN was explicitly set to this same endpoint for focused/scoped PG gates. All package/traffic/evidence/workspace files use `t.TempDir`; peer pools/collectors and fixture stores close. No `G:/ARTEX-0.3.15` or previous instance/history was accessed.

Final amended scoped command:

```powershell
go test -json -p 1 ./db ./evidence ./server -run '^(TestPortableArchiveContract|TestPortableArchiveEdgeContract|TestPortableArchiveServerContract|TestQueryArchiveRowsStreamsJSON|TestWriteArchiveRowsProducesJSONSequence|TestTaskArchive.*|TestRecoverInterruptedArchiveRequiresManualRetry|TestFindingTrafficLegacyArchiveDefaults|TestFindingEvidenceLockNamespace|TestFindingArchiveVersionNormalization|TestEvidence.*|TestRetest.*|TestTaskChatUploadRejectsDeleteBarrierBeforeCreatingDirectory)$' -count=1 -timeout=180s
```

| Package | Pass events | Fail | Skip | Package elapsed |
|---|---:|---:|---:|---:|
| db | 41 | 0 | 0 | 21.512s |
| evidence | 8 | 0 | 0 | 3.520s |
| server | 30 | 0 | 1 | 15.053s |
| Total | 79 | 0 | 1 | 3 package PASS |

Counts include enclosing subtest pass events. The single skip is exactly `TestTaskArchivePackageSkipsSymlink`: Windows `os.Symlink` failed with `A required privilege is not held by the client.` Symlink behavior is not claimed runtime-verified by this Windows gate; Linux native/race validation is a controller follow-up.

Raw first scoped gate remains `%TEMP%/artex-task4c-final-gate.jsonl` (78 pass, 0 fail, 1 skip; db 12.409s/evidence 3.360s/server 14.470s). It passed before the final exact conversation-side transcript selector and owned-context file roundtrip test. Raw amended result is separate `%TEMP%/artex-task4c-amended-gate.jsonl` with the counts above.

Additional checks:

- `go test -p 1 ./... -run '^$' -count=1 -timeout=180s`: all 17 packages compile/link, no selected behavior tests; not a full-project test-suite claim.
- `go vet ./...`: exit 0, no diagnostics.
- Explicit gofmt on touched Go files and `git diff --check`: clean.
- The actual server contracts cover row failure, delayed SQL commit failure, GC from a separately opened peer pool, real finding/binding/snapshot metadata, physical evidence bodies rebuilt from this newly generated package, file journal cleanup failure and retry without duplicate findings. SQLite stream integrity test uses 64 large records and hashes raw request/response bytes before/after; count and sequence progression are asserted in both stores.

## Self-review, boundaries and Task5 follow-up

- Reviewed transaction ownership, lock order, every runtime archive SQL crossing, all current schema relationship selectors, package file selection and callback commit/rollback boundaries. Narrow findings fixed during self-review: missing session/retest/task-rule tables; conversation-only raw/usage/side references; historical retest asset references; missing task/conversation NOA/upload roots; exp0 conversation-side transcript naming; stale fail CAS; short-write checking; ordinal high-water mark; rejected/failed archive pin release; lost rollback errors and preserved postcommit journal.
- Existing archive source files are large (restore was already a multi-hundred-line combined workflow). The new focused SQLite codec/aggregate file is about 430 lines; it keeps the allowed type/column list visible rather than splitting into unrelated modules. No general repository restructure was performed.
- PostgreSQL behavior remains the original composite-row path, with narrow relationship/atomicity/guard corrections proved above. SQLite remains an independent schema/WAL/read-write-pool implementation.
- Archive filesystem operations are journalled recoverable moves, not one globally atomic transaction with SQL or traffic. Traffic import keeps its existing idempotent semantics; package SHA verification and DB rollback prevent corrupt/failed DB restore from becoming live. Body files installed before failed metadata commit remain GC candidates under the existing grace policy.
- Task5 must preserve startup ordering: resolve `recoverTaskArchiveStages`, `recoverTaskArchiveRestoreStages`, and `recoverTaskArchiveDeletePackages` before claiming archive jobs/admitting writers. Recovery must first inspect committed task/archive state: commit means preserve live files and clean only the stage; uncommitted restore means reverse recorded moves. Startup must not delete an unresolved journal or run GC outside its evidence-lock contract. Portable startup/restart pauses tasks and does not automatically resume model execution. No automatic runner startup was added here.
- This task does not prove authenticated full-app HTTP startup, real-model runs, crash/power-loss durability on every filesystem, Linux runtime or the full unrelated test suite. Those remain the controller's later runtime/native gate scope.

## Files changed

`db/task_archives.go`, `db/task_archives_restore.go`, new `db/task_archives_sqlite.go`, new `db/task_archives_portable_test.go`, narrow `db/finding_retests.go` guard; `server/task_archives.go`, `server/task_archive_package.go`, `server/task_archive_package_test.go`, new `server/task_archives_portable_test.go`, `server/server.go`, `server/conversations.go`, `server/side_questions.go`, `server/finding_retests.go`, `server/chatupload.go`; this report.

`implementation/verification.md` belongs to the controller and was neither edited nor staged by this implementer. No test raw logs or fixture credentials are committed.

## Review fix round 1 — Important I1

Base: `a5810da`. Scope is exclusively the confirmed missing conversation-side raw LLM session association from `task-4c-review.md`. Minor M1 (admission mutex held during JSON decode) and M2 (expected fault-injection log capture) remain deferred in the controller-owned `.superpowers/sdd/portable-v0.0.1/progress.md` ledger; they were not implemented or otherwise broadened into this fix.

### Change

`archiveTaskLLMSessionsSQL` replaces the previous main-conversation-only helper. Its code-owned SQL returns both the existing exact `conv-<conversationID>` session and exact `exp0-btw-<requestID>` sessions reached through:

`task -> finding -> finding_retest -> conversation -> side_question_session -> side_question_request.id`.

The snapshot and compact DELETE both call this one helper. The existing explicit recorder task-ID predicate remains intact. No prefix `LIKE`, arbitrary `exp0` enumeration, format/schema change, skill/tool selector expansion or restore redesign was added. It joins the request's stored `session_key` to the side-session row and constructs the recorder session from the generated request ID, not from the session key or client request ID.

The new dual-store `ConversationSideRawRecords` edge contract creates real task/finding/retest/conversation rows, saves a real side snapshot and calls `StartSideRequest` to obtain the production UUID request ID. Its eight raw records include four owned records (task main, conversation main, and two raw recorder rows for the same side request) plus four unowned controls (another task's side request, a similar session prefix, the owned side session key used incorrectly as a request ID, and another conversation main). It asserts:

- NDJSON count 4 and side-request count 1; exactly the owned record IDs/session strings and exact original request/response UTF-8 bytes enter the stream.
- Compact leaves zero owned raw records and retains all four unrelated records with unchanged bytes and SHA-256 hashes.
- Restore returns all eight records under their original IDs with exact request/response bytes and hashes and the original side request-to-session relationship.
- `IsTaskArchiveRestored` reports the committed marker used by the server's existing cleanup retry path. An accidental direct replay rejects within its transaction; the eight raw records/hashes remain unchanged, so retries cannot double these records.

### RED / GREEN

Before changing production selectors:

```powershell
go test -json ./db -run 'TestPortableArchiveEdgeContract/(SQLite|PostgreSQL)/ConversationSideRawRecords' -count=1 -timeout=60s
```

RED: both SQLite and PostgreSQL failed with `owned raw count=2 side request count=1; want 4/1`. Package elapsed 4.410s, exit 1. The only missing rows were the two generated conversation-side request sessions; both main recorder selection paths remained covered. Log: `%TEMP%/artex-task4c-fix1-red.jsonl`.

After the single shared-selector fix, the identical command passed both stores, 5 test pass events (including enclosures), 1 package PASS, 0 fail/skip; package elapsed 3.322s, exit 0. Log: `%TEMP%/artex-task4c-fix1-green.jsonl`.

### Final focused verification

```powershell
go test -json ./db -run '^(TestPortableArchiveContract|TestPortableArchiveEdgeContract)/(SQLite|PostgreSQL)/(ConsistentReaderWriter|ScopeProfilesSideContext|CountsCASDelete|StreamSequenceIntegrity|ConversationSideRawRecords)?$' -count=1 -timeout=90s
```

Result: exit 0, 22 test pass events / 18 leaf tests, 1 DB package PASS, 0 failures, 0 skips, elapsed 17.735s. Go's slash-filter and optional empty third segment also selected all other existing `TestPortableArchiveEdgeContract` children; the actual coverage was the two-store main ArchiveContract and all eight EdgeContract children per store. No unrelated DB/server/full-project behavior suite was rerun. Raw results: `%TEMP%/artex-task4c-fix1-focused-gate.jsonl`.

Relevant-package compilation: `go test -p 1 ./db ./evidence ./server -run '^$' -count=1 -timeout=90s` — exit 0; db 1.603s/evidence 1.524s/server 2.080s, no selected tests. `go vet ./db ./evidence ./server` — exit 0, no diagnostics. Explicit touched-file gofmt and `git diff --check` — clean.

All PG tests and package TestMain above explicitly used the dedicated `127.0.0.1:27347/vh_archives` fixture; reset still checks `current_database() == 'vh_archives'` first. SQLite paths and package fixture files use `t.TempDir`. No previous instance, API keys or `G:/ARTEX-0.3.15` were read. The fix adds no network/model calls.

Self-review: verified the same selector occurs at both snapshot/compact crossings and runs before compaction deletes its side-session/retest source rows; exact session construction uses the stored generated request ID. Both store contracts prove exclusion controls remain hot, count/bytes/hash stay exact and replay rollback preserves accounting. This resolves Important I1. Minor M1/M2 remain explicitly deferred for later ledger triage.

Changed files for this fix: `db/task_archives.go`, `db/task_archives_restore.go`, `db/task_archives_portable_test.go`, and this appended report. `implementation/verification.md` and the controller ledger were neither edited nor staged.
