import {
  type CreateGroupInput,
  type GroupDetail,
  type GroupId,
  type GroupSummary,
  type MemberInput,
  type TurnMode,
  addMember as apiAddMember,
  checkAuth,
  createGroup as apiCreateGroup,
  getGroupDetail as apiGetGroupDetail,
  listGroups as apiListGroups,
  pauseGroup as apiPauseGroup,
  removeMember as apiRemoveMember,
  resumeGroup as apiResumeGroup,
  rotateMemberToken,
  sendLeaderMessage as apiSendLeaderMessage,
  setMaxTurns as apiSetMaxTurns,
  setTurnMode as apiSetTurnMode,
  skipCurrentTurn as apiSkipCurrentTurn,
  stopGroup as apiStopGroup,
} from "@/lib/api";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

export type { CreateGroupInput, MemberInput };

export const groupsKey = ["groups"] as const;
export const groupKey = (id: GroupId) => ["group", id.toString()] as const;
export const authKey = ["auth"] as const;

/** Whether the current browser session holds a valid admin cookie. */
export function useIsAdmin() {
  return useQuery<boolean>({
    queryKey: authKey,
    queryFn: () => checkAuth(),
    retry: 1,
    staleTime: 30_000,
  });
}

/** All groups, most recently updated first, refreshed about every 5 seconds. */
export function useGroups() {
  return useQuery<GroupSummary[]>({
    queryKey: groupsKey,
    queryFn: async () => {
      const groups = await apiListGroups();
      return [...groups].sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));
    },
    refetchInterval: 5000,
  });
}

/** Group detail, polled every 2.5s. Null means unknown id. */
export function useGroupDetail(id: GroupId | null) {
  return useQuery<GroupDetail | null>({
    queryKey: id === null ? ["group", "none"] : groupKey(id),
    queryFn: async () => {
      if (id === null) return null;
      return apiGetGroupDetail(id);
    },
    enabled: id !== null,
    refetchInterval: 2500,
  });
}

/** Refreshes the list and, when given, one group's detail. */
function useRefreshGroups() {
  const queryClient = useQueryClient();
  return (id?: GroupId) => {
    void queryClient.invalidateQueries({ queryKey: groupsKey });
    if (id !== undefined) {
      void queryClient.invalidateQueries({ queryKey: groupKey(id) });
    }
  };
}

export function useCreateGroup() {
  const refresh = useRefreshGroups();
  return useMutation<GroupId, Error, CreateGroupInput>({
    mutationFn: (input) => apiCreateGroup(input),
    onSuccess: () => {
      refresh();
    },
  });
}

export function useSendLeaderMessage() {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; text: string }>({
    mutationFn: ({ groupId, text }) => apiSendLeaderMessage(groupId, text),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

type GroupAction = "skip" | "pause" | "resume" | "stop";

const groupActionFns: Record<GroupAction, (id: GroupId) => Promise<void>> = {
  skip: apiSkipCurrentTurn,
  pause: apiPauseGroup,
  resume: apiResumeGroup,
  stop: apiStopGroup,
};

function useGroupAction(action: GroupAction) {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, GroupId>({
    mutationFn: (groupId) => groupActionFns[action](groupId),
    onSuccess: (_data, groupId) => {
      refresh(groupId);
    },
  });
}

export function useSkipTurn() {
  return useGroupAction("skip");
}

export function usePauseGroup() {
  return useGroupAction("pause");
}

export function useResumeGroup() {
  return useGroupAction("resume");
}

export function useStopGroup() {
  return useGroupAction("stop");
}

export function useSetTurnMode() {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; mode: TurnMode }>({
    mutationFn: ({ groupId, mode }) => apiSetTurnMode(groupId, mode),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useSetMaxTurns() {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; maxTurns: number }>({
    mutationFn: ({ groupId, maxTurns }) => apiSetMaxTurns(groupId, maxTurns),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useAddMember() {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId } & MemberInput>({
    mutationFn: ({ groupId, name, roleTitle, accentColor, isMock }) =>
      apiAddMember(groupId, { name, roleTitle, accentColor, isMock }),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useRemoveMember() {
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; botId: string }>({
    mutationFn: ({ groupId, botId }) => apiRemoveMember(groupId, botId),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useRotateMemberToken() {
  const refresh = useRefreshGroups();
  return useMutation<string, Error, { groupId: GroupId; botId: string }>({
    mutationFn: ({ groupId, botId }) => rotateMemberToken(groupId, botId),
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}
