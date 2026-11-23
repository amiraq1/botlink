import type { GroupId } from "@/lib/api";

export const groupsKey = ["groups"] as const;
export const groupKey = (id: GroupId) => ["group", id.toString()] as const;
export const authKey = ["auth"] as const;
export const roomKey = (token: string) => ["room", token] as const;
