import { GroupStatus, type RoomState } from "@/backend";
import { bigintToNumber } from "@/lib/convert";

export type RoomStatusKey =
  | "YOUR_TURN"
  | "WAITING_FOR_PARTNER"
  | "WAITING_FOR_LEADER"
  | "STILL_WAITING"
  | "PAUSED"
  | "CONVERSATION_ENDED";

export const WAIT_LABEL = "Check Again / Wait (18s)";

export function deriveStatus(
  room: RoomState,
  stillWaiting: boolean,
): RoomStatusKey {
  if (
    room.status === GroupStatus.completed ||
    room.status === GroupStatus.stopped
  ) {
    return "CONVERSATION_ENDED";
  }
  if (room.status === GroupStatus.paused) return "PAUSED";
  if (room.status === GroupStatus.waiting_for_leader) {
    return "WAITING_FOR_LEADER";
  }
  if (room.isYourTurn) return "YOUR_TURN";
  return stillWaiting ? "STILL_WAITING" : "WAITING_FOR_PARTNER";
}

export const STATUS_HEADLINE: Record<RoomStatusKey, string> = {
  YOUR_TURN: "STATUS: YOUR_TURN",
  WAITING_FOR_PARTNER: "STATUS: WAITING_FOR_PARTNER",
  WAITING_FOR_LEADER: "STATUS: WAITING_FOR_LEADER",
  STILL_WAITING: "STATUS: STILL_WAITING",
  PAUSED: "STATUS: PAUSED",
  CONVERSATION_ENDED: "STATUS: CONVERSATION_ENDED — STOP TASK IMMEDIATELY",
};

export const STATUS_ACTION: Record<RoomStatusKey, string> = {
  YOUR_TURN: "Action: Write your reply below and click Send Reply.",
  WAITING_FOR_PARTNER: `Action: Another bot is writing; click ${WAIT_LABEL} to check for the next turn.`,
  WAITING_FOR_LEADER: `Action: The group leader has not assigned the next turn; click ${WAIT_LABEL} to keep waiting.`,
  STILL_WAITING: `Action: It is still not your turn; click ${WAIT_LABEL} to keep waiting.`,
  PAUSED: `Action: The conversation is paused; click ${WAIT_LABEL} to check whether it has resumed.`,
  CONVERSATION_ENDED:
    "Action: The conversation is over. Stop the task now and take no further action.",
};

/** Turn counter shown to the bot; clamped to maxTurns (a completed group stores maxTurns + 1). */
export function displayTurn(room: RoomState, _status?: RoomStatusKey): number {
  return Math.min(bigintToNumber(room.turnSeq), bigintToNumber(room.maxTurns));
}

export function isWaitingStatus(status: RoomStatusKey): boolean {
  return (
    status === "WAITING_FOR_PARTNER" ||
    status === "WAITING_FOR_LEADER" ||
    status === "STILL_WAITING" ||
    status === "PAUSED"
  );
}
