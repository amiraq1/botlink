import assert from "node:assert/strict";
import test from "node:test";
import { type GroupDetail, GroupStatus, MessageRole, TurnMode } from "./api";
import {
  applyGroupAction,
  applyLeaderMessage,
  applyMaxTurns,
  applyTurnMode,
} from "./optimistic";

function detail(): GroupDetail {
  return {
    group: {
      id: 1,
      name: "G",
      description: "",
      topic: "",
      status: GroupStatus.running,
      turnMode: TurnMode.auto_round_robin,
      maxTurns: 20,
      turnSeq: 3,
      turnQueue: [],
      mockDelaySec: 12,
      turnStartedAt: 1,
      createdAt: 0,
      updatedAt: 1,
    },
    members: [],
    messages: [],
    hasMore: false,
  };
}

test("turn controls patch the status immediately", () => {
  assert.equal(
    applyGroupAction(detail(), "pause", 50).group.status,
    GroupStatus.paused,
  );
  const resumed = applyGroupAction(detail(), "resume", 50);
  assert.equal(resumed.group.status, GroupStatus.running);
  assert.equal(resumed.group.turnStartedAt, 50);
  const stopped = applyGroupAction(detail(), "stop", 50);
  assert.equal(stopped.group.status, GroupStatus.stopped);
  assert.equal(stopped.group.turnStartedAt, 1);
  const skipped = applyGroupAction(detail(), "skip", 50);
  assert.equal(skipped.group.turnStartedAt, 50);
  assert.equal(skipped.group.status, GroupStatus.running);
});

test("turn mode and max turns patch their fields", () => {
  assert.equal(
    applyTurnMode(detail(), TurnMode.leader_round, 7).group.turnMode,
    TurnMode.leader_round,
  );
  const capped = applyMaxTurns(detail(), 12, 7);
  assert.equal(capped.group.maxTurns, 12);
  assert.equal(capped.group.updatedAt, 7);
});

test("leader messages render instantly with a negative id", () => {
  const patched = applyLeaderMessage(detail(), "hello team", 123);
  assert.equal(patched.messages.length, 1);
  const pending = patched.messages[0];
  assert.ok(pending.id < 0);
  assert.equal(pending.role, MessageRole.leader);
  assert.equal(pending.senderName, "القائد");
  assert.equal(pending.content, "hello team");
  assert.equal(patched.group.updatedAt, 123);
});
