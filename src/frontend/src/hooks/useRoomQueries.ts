import type { RoomState } from "@/backend";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAnonymousActor } from "./useAnonymousActor";

export const roomKey = (token: string) => ["room", token] as const;

/**
 * Public room state for an agent token. Uses the anonymous actor.
 * getRoomState is an update call; it resolves to null for an unknown token.
 * `refetchMs` of 0 (default) disables background polling.
 */
export function useRoomState(token: string, refetchMs = 0) {
  const { actor, isFetching } = useAnonymousActor();
  return useQuery<RoomState | null>({
    queryKey: roomKey(token),
    queryFn: async () => {
      if (!actor) return null;
      return actor.getRoomState(token);
    },
    enabled: !!actor && !isFetching && token.length > 0,
    refetchInterval: refetchMs > 0 ? refetchMs : false,
    retry: 1,
  });
}

export interface BotReplyInput {
  expectedTurnSeq: bigint;
  content: string;
}

/**
 * Submits a bot reply. Resolves to the `accepted` flag; false means a stale,
 * duplicate or wrong-turn submit, which is not an error. The room query is
 * always refreshed afterwards.
 */
export function useSubmitBotReply(token: string) {
  const { actor } = useAnonymousActor();
  const queryClient = useQueryClient();
  return useMutation<boolean, Error, BotReplyInput>({
    mutationFn: async ({ expectedTurnSeq, content }) => {
      if (!actor) throw new Error("Connection is not ready yet");
      const result = await actor.submitBotReply(
        token,
        expectedTurnSeq,
        content,
      );
      return result.accepted;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: roomKey(token) });
    },
  });
}
