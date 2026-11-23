import {
  type GroupDetail,
  GroupStatus,
  MessageRole,
  type TurnMode,
} from "@/lib/api";

export type GroupAction = "skip" | "pause" | "resume" | "stop";

const MAX_PENDING = 500;

/** Monotonic negative ids keep optimistic bubbles unique and easy to filter. */
let pendingMessageId = 0;

/**
 * Optimistic patch for the admin turn controls. It mirrors the fields the
 * server will change so the UI reacts immediately; the authoritative state
 * arrives with the follow-up invalidation. Skip cannot know the next speaker
 * locally, so it only refreshes the turn clock.
 */
export function applyGroupAction(
  detail: GroupDetail,
  action: GroupAction,
  now: number = Date.now(),
): GroupDetail {
  const group = { ...detail.group, updatedAt: now };
  switch (action) {
    case "pause":
      group.status = GroupStatus.paused;
      break;
    case "resume":
      group.status = GroupStatus.running;
      group.turnStartedAt = now;
      break;
    case "stop":
      group.status = GroupStatus.stopped;
      group.currentTurnBotId = undefined;
      group.turnQueue = [];
      break;
    case "skip":
      group.turnStartedAt = now;
      break;
  }
  return { ...detail, group };
}

export function applyTurnMode(
  detail: GroupDetail,
  mode: TurnMode,
  now: number = Date.now(),
): GroupDetail {
  return {
    ...detail,
    group: { ...detail.group, turnMode: mode, updatedAt: now },
  };
}

export function applyMaxTurns(
  detail: GroupDetail,
  maxTurns: number,
  now: number = Date.now(),
): GroupDetail {
  return {
    ...detail,
    group: { ...detail.group, maxTurns, updatedAt: now },
  };
}

/** Appends a leader bubble tagged "القائد" so sending feels instant. */
export function applyLeaderMessage(
  detail: GroupDetail,
  text: string,
  now: number = Date.now(),
): GroupDetail {
  const messages = [
    ...detail.messages,
    {
      id: --pendingMessageId,
      groupId: detail.group.id,
      senderBotId: "leader",
      senderName: "القائد",
      senderColor: "#1D6BFF",
      role: MessageRole.leader,
      content: text,
      createdAt: now,
    },
  ];
  return {
    ...detail,
    messages:
      messages.length > MAX_PENDING ? messages.slice(-MAX_PENDING) : messages,
    group: { ...detail.group, updatedAt: now },
  };
}
