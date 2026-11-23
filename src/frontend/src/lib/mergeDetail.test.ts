import assert from "node:assert/strict";
import test from "node:test";
import {
  type GroupDetail,
  GroupStatus,
  type Message,
  MessageRole,
  TurnMode,
} from "./api";
import {
  MAX_CACHED_MESSAGES,
  lastRealMessageId,
  mergeDetail,
} from "./mergeDetail";

function message(id: number, createdAt = id): Message {
  return {
    id,
    groupId: 1,
    senderBotId: "bot_1",
    senderName: "Alpha",
    senderColor: "#3B82F6",
    role: MessageRole.bot,
    content: `m${id}`,
    createdAt,
  };
}

function detail(messages: Message[], updatedAt = 1): GroupDetail {
  return {
    group: {
      id: 1,
      name: "G",
      description: "",
      topic: "",
      status: GroupStatus.running,
      turnMode: TurnMode.auto_round_robin,
      maxTurns: 20,
      turnSeq: 1,
      turnQueue: [],
      mockDelaySec: 12,
      turnStartedAt: 0,
      createdAt: 0,
      updatedAt,
    },
    members: [],
    messages,
    hasMore: false,
  };
}

test("mergeDetail returns the fresh page when nothing is cached", () => {
  const fresh = detail([message(2)]);
  assert.equal(mergeDetail(null, null), null);
  assert.equal(mergeDetail(null, fresh), fresh);
  assert.equal(mergeDetail(detail([message(1)]), null), null);
});

test("mergeDetail appends only unseen server messages", () => {
  const cached = detail([message(1), message(2)]);
  const fresh = detail([message(2), message(3), message(4)], 9);
  const merged = mergeDetail(cached, fresh);
  assert.deepEqual(
    merged?.messages.map((m) => m.id),
    [1, 2, 3, 4],
  );
  assert.equal(merged?.group.updatedAt, 9);
});

test("mergeDetail drops optimistic bubbles once the server responds", () => {
  const cached = detail([message(1), message(-1000)]);
  const fresh = detail([message(1), message(2)]);
  const merged = mergeDetail(cached, fresh);
  assert.deepEqual(
    merged?.messages.map((m) => m.id),
    [1, 2],
  );
  assert.equal(lastRealMessageId(cached.messages), 1);
});

test("mergeDetail keeps the live list capped", () => {
  const cachedMessages = Array.from({ length: MAX_CACHED_MESSAGES }, (_, i) =>
    message(i + 1),
  );
  const fresh = detail([message(MAX_CACHED_MESSAGES + 1)]);
  const merged = mergeDetail(detail(cachedMessages), fresh);
  assert.equal(merged?.messages.length, MAX_CACHED_MESSAGES);
  assert.equal(merged?.messages.at(-1)?.id, MAX_CACHED_MESSAGES + 1);
});

test("lastRealMessageId ignores optimistic and empty lists", () => {
  assert.equal(lastRealMessageId([]), 0);
  assert.equal(lastRealMessageId([message(-5), message(-2)]), 0);
  assert.equal(lastRealMessageId([message(7), message(-1)]), 7);
});
