import assert from "node:assert/strict";
import test from "node:test";
import { isLongGap, LONG_GAP_MS } from "./messageGap";

test("two-hour gap is measured in milliseconds", () => {
  const start = 1_700_000_000_000;
  assert.equal(isLongGap(start, start + LONG_GAP_MS - 1), false);
  assert.equal(isLongGap(start, start + LONG_GAP_MS + 1), true);
});

test("first message has no previous message", () => {
  assert.equal(isLongGap(undefined, 1_700_000_000_000), false);
});
