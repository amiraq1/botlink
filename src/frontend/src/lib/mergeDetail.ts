import type { GroupDetail, Message } from "@/lib/api";

/** Live chat cap: matches the admin stream's bounded history. */
export const MAX_CACHED_MESSAGES = 500;

/**
 * Id of the newest server message, ignoring optimistic entries (negative ids).
 * Delta polling must page from a real id, never from an optimistic bubble.
 */
export function lastRealMessageId(messages: Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].id > 0) return messages[i].id;
  }
  return 0;
}

/**
 * Folds a delta poll into the cached detail: optimistic messages (negative
 * ids) are discarded once the server responds, unseen messages are appended in
 * order, and the list stays capped. Group and member data always come from the
 * fresh response.
 */
export function mergeDetail(
  cached: GroupDetail | null,
  fresh: GroupDetail | null,
): GroupDetail | null {
  if (!fresh) return fresh;
  if (!cached) return fresh;

  const merged = cached.messages.filter((message) => message.id > 0);
  const known = new Set(merged.map((message) => message.id));
  for (const message of fresh.messages) {
    if (!known.has(message.id)) {
      known.add(message.id);
      merged.push(message);
    }
  }
  return {
    ...fresh,
    messages:
      merged.length > MAX_CACHED_MESSAGES
        ? merged.slice(-MAX_CACHED_MESSAGES)
        : merged,
  };
}
