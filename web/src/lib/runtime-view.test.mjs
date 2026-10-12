import assert from "node:assert/strict";
import test from "node:test";
import { toolStatusLabel, pausedFeatures } from "./runtime-view.ts";

test("uses backend statuses without treating unchecked tools as ready", () => {
  assert.equal(toolStatusLabel("ready"), "可用");
  assert.equal(toolStatusLabel("missing"), "缺失");
  assert.equal(toolStatusLabel("unverified"), "未验证");
  assert.equal(toolStatusLabel("incompatible"), "不兼容");
  assert.equal(toolStatusLabel("future"), "future");
});
test("session restore only selects known paused background features", () => {
  assert.deepEqual(pausedFeatures({ scheduler: true, notifier: false, mcp: false, unknown: false }), ["notifier", "mcp", "archives"]);
  assert.deepEqual(pausedFeatures({ scheduler: true, notifier: true, mcp: true, archives: true }), []);
});
