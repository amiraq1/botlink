import {
  type CreateGroupInput,
  type GroupDetail,
  type GroupId,
  type GroupSummary,
  type MemberInput,
  type TurnMode,
  createActor,
} from "@/backend";
import { useActor, useInternetIdentity } from "@caffeineai/core-infrastructure";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

export type { CreateGroupInput, MemberInput };

export const groupsKey = ["groups"] as const;
export const groupKey = (id: GroupId) => ["group", id.toString()] as const;

function usePrincipalText(): string {
  const { identity } = useInternetIdentity();
  return identity?.getPrincipal().toText() ?? "anonymous";
}

/** Whether the signed-in caller is the app admin. Only enabled for a signed-in, ready actor. */
export function useIsAdmin() {
  const { actor, isFetching } = useActor(createActor);
  const { isAuthenticated } = useInternetIdentity();
  const principal = usePrincipalText();
  return useQuery<boolean>({
    queryKey: ["isCallerAdmin", principal],
    queryFn: async () => {
      if (!actor) return false;
      return actor.isCallerAdmin();
    },
    enabled: !!actor && !isFetching && isAuthenticated,
    retry: 1,
  });
}

/** All groups, most recently updated first, refreshed about every 5 seconds. */
export function useGroups() {
  const { actor, isFetching } = useActor(createActor);
  return useQuery<GroupSummary[]>({
    queryKey: groupsKey,
    queryFn: async () => {
      if (!actor) return [];
      const groups = await actor.listGroups();
      return [...groups].sort((a, b) => (a.updatedAt < b.updatedAt ? 1 : -1));
    },
    enabled: !!actor && !isFetching,
    refetchInterval: 5000,
  });
}

/** Group detail, polled every 2.5s. getGroupDetail is an update call; null means unknown id. */
export function useGroupDetail(id: GroupId | null) {
  const { actor, isFetching } = useActor(createActor);
  return useQuery<GroupDetail | null>({
    queryKey: id === null ? ["group", "none"] : groupKey(id),
    queryFn: async () => {
      if (!actor || id === null) return null;
      return actor.getGroupDetail(id);
    },
    enabled: !!actor && !isFetching && id !== null,
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
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<GroupId, Error, CreateGroupInput>({
    mutationFn: async (input) => {
      if (!actor) throw new Error("Backend is not ready");
      return actor.createGroup(input);
    },
    onSuccess: () => {
      refresh();
    },
  });
}

export function useSendLeaderMessage() {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; text: string }>({
    mutationFn: async ({ groupId, text }) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor.sendLeaderMessage(groupId, text);
    },
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

type GroupAction =
  | "skipCurrentTurn"
  | "pauseGroup"
  | "resumeGroup"
  | "stopGroup";

function useGroupAction(action: GroupAction) {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, GroupId>({
    mutationFn: async (groupId) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor[action](groupId);
    },
    onSuccess: (_data, groupId) => {
      refresh(groupId);
    },
  });
}

export function useSkipTurn() {
  return useGroupAction("skipCurrentTurn");
}

export function usePauseGroup() {
  return useGroupAction("pauseGroup");
}

export function useResumeGroup() {
  return useGroupAction("resumeGroup");
}

export function useStopGroup() {
  return useGroupAction("stopGroup");
}

export function useSetTurnMode() {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; mode: TurnMode }>({
    mutationFn: async ({ groupId, mode }) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor.setTurnMode(groupId, mode);
    },
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useSetMaxTurns() {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; maxTurns: bigint }>({
    mutationFn: async ({ groupId, maxTurns }) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor.setMaxTurns(groupId, maxTurns);
    },
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useAddMember() {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId } & MemberInput>({
    mutationFn: async ({ groupId, name, roleTitle, accentColor, isMock }) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor.addMember(groupId, name, roleTitle, accentColor, isMock);
    },
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}

export function useRemoveMember() {
  const { actor } = useActor(createActor);
  const refresh = useRefreshGroups();
  return useMutation<void, Error, { groupId: GroupId; botId: string }>({
    mutationFn: async ({ groupId, botId }) => {
      if (!actor) throw new Error("Backend is not ready");
      await actor.removeMember(groupId, botId);
    },
    onSuccess: (_data, { groupId }) => {
      refresh(groupId);
    },
  });
}
