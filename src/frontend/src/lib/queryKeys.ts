import type { GroupId } from "@/lib/api";

export const groupsKey = ["groups"] as const;
export const groupKey = (id: GroupId) => ["group", id.toString()] as const;
export const authKey = ["auth"] as const;
export const roomKey = (token: string) => ["room", token] as const;

export const toolsKey = ["admin", "tools"] as const;
export const memberToolsKey = (groupId: GroupId) =>
  ["admin", "groups", groupId.toString(), "member-tools"] as const;
export const toolCallsAuditKey = (groupId: GroupId, limit?: number) =>
  [
    "admin",
    "groups",
    groupId.toString(),
    "tool-calls",
    limit?.toString() ?? "50",
  ] as const;
