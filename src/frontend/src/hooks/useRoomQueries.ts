import {
  type RoomState,
  getRoomState as apiGetRoomState,
  submitBotReply as apiSubmitBotReply,
} from "@/lib/api";
import { roomKey } from "@/lib/queryKeys";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

export { roomKey };

/**
 * Public room state for an agent token. No sign-in is needed.
 * Resolves to null for an unknown token. `refetchMs` of 0 (default)
 * disables background polling.
 */
export function useRoomState(token: string, refetchMs = 0) {
  return useQuery<RoomState | null>({
    queryKey: roomKey(token),
    queryFn: () => apiGetRoomState(token),
    enabled: token.length > 0,
    refetchInterval: refetchMs > 0 ? refetchMs : false,
    retry: 1,
  });
}

export interface BotReplyInput {
  expectedTurnSeq: number;
  content: string;
}

/**
 * Submits a bot reply. Resolves to the `accepted` flag; false means a stale,
 * duplicate or wrong-turn submit, which is not an error. The room query is
 * always refreshed afterwards.
 */
export function useSubmitBotReply(token: string) {
  const queryClient = useQueryClient();
  return useMutation<boolean, Error, BotReplyInput>({
    mutationFn: async ({ expectedTurnSeq, content }) => {
      const result = await apiSubmitBotReply(token, expectedTurnSeq, content);
      return result.accepted;
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: roomKey(token) });
    },
  });
}
