#!/usr/bin/env bash
# Root ./... does not traverse these module replacements. The norma scope
# preserves patched launch/ownership assertions. Whole upstream tool coverage
# is not claimed: its original Windows Unix-shell tests have known failures.
set -euo pipefail
norma_pattern='TestExecutionSnapshot|TestRipgrepNever|TestRuntimePTY|TestPrepared|TestStdioClientOverPipes|TestRuntimeCleanup|TestCleanupRemovesDir'
if test -n "${GITHUB_STEP_SUMMARY:-}"; then
  printf '\nNested commands:\n\n`cd third_party/norma && go test ./tool ./mcp -run %s -count=1 -timeout=5m`\n\n`cd third_party/go-mitmproxy && go test ./proxy -count=1 -timeout=5m`\n\nKnown upstream Windows whole-tool baseline: Unix `$HTTP_PROXY` and `expr` shell syntax; those tests remain unchanged.\n' "$norma_pattern" >> "$GITHUB_STEP_SUMMARY"
fi
(cd third_party/norma && go test ./tool ./mcp -run "$norma_pattern" -count=1 -timeout=5m)
(cd third_party/go-mitmproxy && go test ./proxy -count=1 -timeout=5m)
