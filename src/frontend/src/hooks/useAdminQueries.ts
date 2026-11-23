import {
  type CreateGroupInput,
  type GroupDetail,
  type GroupId,
  type GroupSummary,
  type MemberInput,
  type MemberTool,
  type Tool,
  type ToolCall,
  type ToolInput,
  type TurnMode,
  addMember as apiAddMember,
  createGroup as apiCreateGroup,
  createTool as apiCreateTool,
  deleteTool as apiDeleteTool,
  enableTool as apiEnableTool,
  getGroupDetail as apiGetGroupDetail,
  listGroupMemberTools as apiListGroupMemberTools,
  listGroups as apiListGroups,
  listToolCallsAudit as apiListToolCallsAudit,
  listTools as apiListTools,
  pauseGroup as apiPauseGroup,
  removeMember as apiRemoveMember,
  removeMemberTool as apiRemoveMemberTool,
  resumeGroup as apiResumeGroup,
  revokeAllSessions as apiRevokeAllSessions,
  sendLeaderMessage as apiSendLeaderMessage,
  setMaxTurns as apiSetMaxTurns,
  setMemberTool as apiSetMemberTool,
  setTurnMode as apiSetTurnMode,
  skipCurrentTurn as apiSkipCurrentTurn,
  stopGroup as apiStopGroup,
  testTool as apiTestTool,
  updateTool as apiUpdateTool,
  checkAuth,
  rotateMemberToken,
} from "@/lib/api";
import { lastRealMessageId, mergeDetail } from "@/lib/mergeDetail";
import {
  type GroupAction,
  applyGroupAction,
  applyLeaderMessage,
  applyMaxTurns,
  applyTurnMode,
} from "@/lib/optimistic";
import {
  authKey,
  groupKey,
  groupsKey,
  memberToolsKey,
  toolCallsAuditKey,
  toolsKey,
} from "@/lib/queryKeys";
import {
  type QueryClient,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";

export type { CreateGroupInput, MemberInput };
export {
  authKey,
  groupKey,
  groupsKey,
  toolsKey,
  memberToolsKey,
  toolCallsAuditKey,
};

/** Snapshot taken before an optimistic patch, restored on failure. */
interface GroupSnapshot {
  prev?: GroupDetail | null;
}

function takeSnapshot(queryClient: QueryClient, id: GroupId): GroupSnapshot {
  return { prev: queryClient.getQueryData<GroupDetail | null>(groupKey(id)) };
}

function restoreSnapshot(
  queryClient: QueryClient,
  id: GroupId,
  snapshot: GroupSnapshot | undefined,
) {
  if (snapshot?.prev !== undefined) {
    queryClient.setQueryData(groupKey(id), snapshot.prev);
  }
}

/** Whether the current browser session holds a valid admin cookie. */
export function useIsAdmin() {
  return useQuery<boolean>({
    queryKey: authKey,
    queryFn: () => checkAuth(),
    retry: 1,
    staleTime: 30_000,
  });
}

/** All groups, most recently updated first; backs off when nothing changes. */
export function useGroups() {
  return useQuery<GroupSummary[]>({
    queryKey: groupsKey,
    queryFn: async () => {
      const groups = await apiListGroups();
      return [...groups].sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));
    },
    refetchInterval: (query) => {
      const newest = query.state.data?.[0]?.updatedAt ?? 0;
      return Date.now() - newest > 60_000 ? 10_000 : 5000;
    },
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
}

/**
 * Group detail with delta polling and cache merging. When the live SSE stream
 * is healthy (`live`), the fallback poll slows to 15s; otherwise it keeps the
 * adaptive 2.5s/10s cadence.
 */
export function useGroupDetail(id: GroupId | null, live = false) {
  const queryClient = useQueryClient();
  return useQuery<GroupDetail | null>({
    queryKey: id === null ? ["group", "none"] : groupKey(id),
    queryFn: async () => {
      if (id === null) return null;
      const cached = queryClient.getQueryData<GroupDetail | null>(groupKey(id));
      const afterId = cached ? lastRealMessageId(cached.messages) : 0;
      const fresh = await apiGetGroupDetail(
        id,
        afterId ? { afterId, limit: 200 } : { limit: 100 },
      );
      return mergeDetail(cached ?? null, fresh);
    },
    enabled: id !== null,
    refetchInterval: (query) => {
      if (live) return 15_000;
      const data = query.state.data;
      if (!data) return 2500;
      const last = data.messages.at(-1)?.createdAt ?? data.group.updatedAt;
      return Date.now() - last > 60_000 ? 10_000 : 2500;
    },
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
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
  const queryClient = useQueryClient();
  const refresh = useRefreshGroups();
  return useMutation<
    void,
    Error,
    { groupId: GroupId; text: string },
    GroupSnapshot
  >({
    mutationFn: ({ groupId, text }) => apiSendLeaderMessage(groupId, text),
    onMutate: async ({ groupId, text }) => {
      await queryClient.cancelQueries({ queryKey: groupKey(groupId) });
      const snapshot = takeSnapshot(queryClient, groupId);
      const current = queryClient.getQueryData<GroupDetail | null>(
        groupKey(groupId),
      );
      if (current) {
        queryClient.setQueryData(
          groupKey(groupId),
          applyLeaderMessage(current, text),
        );
      }
      return snapshot;
    },
    onError: (_err, { groupId }, snapshot) => {
      restoreSnapshot(queryClient, groupId, snapshot);
    },
    onSettled: (_data, _err, { groupId }) => {
      refresh(groupId);
    },
  });
}

const groupActionFns: Record<GroupAction, (id: GroupId) => Promise<void>> = {
  skip: apiSkipCurrentTurn,
  pause: apiPauseGroup,
  resume: apiResumeGroup,
  stop: apiStopGroup,
};

function useGroupAction(action: GroupAction) {
  const queryClient = useQueryClient();
  const refresh = useRefreshGroups();
  return useMutation<void, Error, GroupId, GroupSnapshot>({
    mutationFn: (groupId) => groupActionFns[action](groupId),
    onMutate: async (groupId) => {
      await queryClient.cancelQueries({ queryKey: groupKey(groupId) });
      const snapshot = takeSnapshot(queryClient, groupId);
      const current = queryClient.getQueryData<GroupDetail | null>(
        groupKey(groupId),
      );
      if (current) {
        queryClient.setQueryData(
          groupKey(groupId),
          applyGroupAction(current, action),
        );
      }
      return snapshot;
    },
    onError: (_err, groupId, snapshot) => {
      restoreSnapshot(queryClient, groupId, snapshot);
    },
    onSettled: (_data, _err, groupId) => {
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
  const queryClient = useQueryClient();
  const refresh = useRefreshGroups();
  return useMutation<
    void,
    Error,
    { groupId: GroupId; mode: TurnMode },
    GroupSnapshot
  >({
    mutationFn: ({ groupId, mode }) => apiSetTurnMode(groupId, mode),
    onMutate: async ({ groupId, mode }) => {
      await queryClient.cancelQueries({ queryKey: groupKey(groupId) });
      const snapshot = takeSnapshot(queryClient, groupId);
      const current = queryClient.getQueryData<GroupDetail | null>(
        groupKey(groupId),
      );
      if (current) {
        queryClient.setQueryData(
          groupKey(groupId),
          applyTurnMode(current, mode),
        );
      }
      return snapshot;
    },
    onError: (_err, { groupId }, snapshot) => {
      restoreSnapshot(queryClient, groupId, snapshot);
    },
    onSettled: (_data, _err, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useSetMaxTurns() {
  const queryClient = useQueryClient();
  const refresh = useRefreshGroups();
  return useMutation<
    void,
    Error,
    { groupId: GroupId; maxTurns: number },
    GroupSnapshot
  >({
    mutationFn: ({ groupId, maxTurns }) => apiSetMaxTurns(groupId, maxTurns),
    onMutate: async ({ groupId, maxTurns }) => {
      await queryClient.cancelQueries({ queryKey: groupKey(groupId) });
      const snapshot = takeSnapshot(queryClient, groupId);
      const current = queryClient.getQueryData<GroupDetail | null>(
        groupKey(groupId),
      );
      if (current) {
        queryClient.setQueryData(
          groupKey(groupId),
          applyMaxTurns(current, maxTurns),
        );
      }
      return snapshot;
    },
    onError: (_err, { groupId }, snapshot) => {
      restoreSnapshot(queryClient, groupId, snapshot);
    },
    onSettled: (_data, _err, { groupId }) => {
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

export function useRevokeAllSessions() {
  return useMutation<void, Error, void>({
    mutationFn: () => apiRevokeAllSessions(),
  });
}

/* ------------------------------------------------------------------ */
/* Tools Management Hooks                                             */
/* ------------------------------------------------------------------ */

export function useTools() {
  return useQuery<Tool[]>({
    queryKey: toolsKey,
    queryFn: () => apiListTools(),
    staleTime: 5000,
  });
}

export function useCreateTool() {
  const queryClient = useQueryClient();
  return useMutation<{ id: number }, Error, ToolInput>({
    mutationFn: (input) => apiCreateTool(input),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: toolsKey });
    },
  });
}

export function useUpdateTool() {
  const queryClient = useQueryClient();
  return useMutation<void, Error, { id: number; input: Partial<ToolInput> }>({
    mutationFn: ({ id, input }) => apiUpdateTool(id, input),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: toolsKey });
    },
  });
}

export function useEnableTool() {
  const queryClient = useQueryClient();
  return useMutation<void, Error, { id: number; enabled: boolean }>({
    mutationFn: ({ id, enabled }) => apiEnableTool(id, enabled),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: toolsKey });
    },
  });
}

export function useDeleteTool() {
  const queryClient = useQueryClient();
  return useMutation<void, Error, number>({
    mutationFn: (id) => apiDeleteTool(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: toolsKey });
    },
  });
}

export function useTestTool() {
  return useMutation<
    import("@/lib/api").ToolTestResult,
    Error,
    { id: number; input: Record<string, unknown> }
  >({
    mutationFn: ({ id, input }) => apiTestTool(id, input),
  });
}

/* ------------------------------------------------------------------ */
/* Member Tools Assignment Hooks                                      */
/* ------------------------------------------------------------------ */

export function useGroupMemberTools(groupId: GroupId | null) {
  return useQuery<MemberTool[]>({
    queryKey:
      groupId === null ? ["member-tools", "none"] : memberToolsKey(groupId),
    queryFn: () => (groupId === null ? [] : apiListGroupMemberTools(groupId)),
    enabled: groupId !== null,
    staleTime: 5000,
  });
}

export function useSetMemberTool() {
  const queryClient = useQueryClient();
  return useMutation<
    void,
    Error,
    {
      groupId: GroupId;
      botId: string;
      toolId: number;
      enabled: boolean;
      maxCallsPerTurn?: number;
    }
  >({
    mutationFn: ({ groupId, botId, toolId, enabled, maxCallsPerTurn }) =>
      apiSetMemberTool(groupId, botId, toolId, enabled, maxCallsPerTurn ?? 3),
    onSuccess: (_data, { groupId }) => {
      queryClient.invalidateQueries({ queryKey: memberToolsKey(groupId) });
    },
  });
}

export function useRemoveMemberTool() {
  const queryClient = useQueryClient();
  return useMutation<
    void,
    Error,
    { groupId: GroupId; botId: string; toolId: number }
  >({
    mutationFn: ({ groupId, botId, toolId }) =>
      apiRemoveMemberTool(groupId, botId, toolId),
    onSuccess: (_data, { groupId }) => {
      queryClient.invalidateQueries({ queryKey: memberToolsKey(groupId) });
    },
  });
}

/* ------------------------------------------------------------------ */
/* Tool Calls Audit Log Hooks                                         */
/* ------------------------------------------------------------------ */

export function useToolCallsAudit(groupId: GroupId | null, limit = 50) {
  return useQuery<ToolCall[]>({
    queryKey:
      groupId === null
        ? ["tool-calls", "none"]
        : toolCallsAuditKey(groupId, limit),
    queryFn: () =>
      groupId === null ? [] : apiListToolCallsAudit(groupId, limit),
    enabled: groupId !== null,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  });
}
