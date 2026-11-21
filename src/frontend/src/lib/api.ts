// REST client for the Go + SQLite backend. All requests send the admin
// session cookie (credentials: 'include'); the public room endpoints need
// no authentication at all.
//
// Types mirror the Go JSON shapes: ids and counters are plain numbers,
// timestamps are Unix milliseconds.

export type BotId = string;
export type GroupId = number;
export type MessageId = number;
/** Unix milliseconds (the Go API returns ms, not Motoko nanoseconds). */
export type Timestamp = number;

export enum GroupStatus {
  waiting_for_leader = "waiting_for_leader",
  stopped = "stopped",
  completed = "completed",
  running = "running",
  paused = "paused",
}

export enum MessageRole {
  bot = "bot",
  leader = "leader",
}

export enum TurnMode {
  auto_round_robin = "auto_round_robin",
  leader_round = "leader_round",
}

export interface Group {
  id: GroupId;
  status: GroupStatus;
  turnMode: TurnMode;
  topic: string;
  turnQueue: Array<BotId>;
  name: string;
  createdAt: Timestamp;
  mockDelaySec: number;
  turnStartedAt: Timestamp;
  description: string;
  currentTurnBotId?: BotId;
  updatedAt: Timestamp;
  turnSeq: number;
  maxTurns: number;
}

export interface GroupDetail {
  members: Array<MemberView>;
  messages: Array<Message>;
  group: Group;
}

export interface GroupSummary {
  id: GroupId;
  status: GroupStatus;
  turnMode: TurnMode;
  onlineCount: number;
  topic: string;
  name: string;
  createdAt: Timestamp;
  memberCount: number;
  description: string;
  updatedAt: Timestamp;
  turnSeq: number;
  maxTurns: number;
}

export interface MemberInput {
  name: string;
  roleTitle: string;
  isMock: boolean;
  accentColor: string;
}

export interface CreateGroupInput {
  turnMode: TurnMode;
  topic: string;
  members: Array<MemberInput>;
  name: string;
  mockDelaySec?: number;
  description: string;
  maxTurns: number;
}

export interface MemberView {
  lastSeenAt: Timestamp;
  agentToken: string;
  name: string;
  roleTitle: string;
  isMock: boolean;
  accentColor: string;
  groupId: GroupId;
  botId: BotId;
  online: boolean;
}

export interface Message {
  id: MessageId;
  senderBotId: BotId;
  content: string;
  createdAt: Timestamp;
  role: MessageRole;
  groupId: GroupId;
  turnSeq?: number;
  senderColor: string;
  senderName: string;
}

export interface ReplyResult {
  accepted: boolean;
}

export interface RoomState {
  status: GroupStatus;
  topic: string;
  isYourTurn: boolean;
  messages: Array<Message>;
  botName: string;
  roleTitle: string;
  onlineNames: Array<string>;
  turnSeq: number;
  botId: string;
  maxTurns: number;
  groupName: string;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function request<T>(
  input: string,
  init?: RequestInit,
): Promise<T> {
  const res = await fetch(input, {
    credentials: "include",
    headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    ...init,
  });
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const message =
      typeof data === "object" && data !== null && "error" in data
        ? String((data as { error: unknown }).error)
        : `Request failed (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return data as T;
}

function post<T>(url: string, body?: unknown): Promise<T> {
  return request<T>(url, {
    method: "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

export async function login(password: string): Promise<void> {
  await post<{ ok: boolean }>("/api/auth/login", { password });
}

export async function logout(): Promise<void> {
  await post<{ ok: boolean }>("/api/auth/logout");
}

export async function checkAuth(): Promise<boolean> {
  try {
    const res = await request<{ authenticated: boolean }>("/api/auth/me");
    return res.authenticated;
  } catch {
    return false;
  }
}

// ---------------------------------------------------------------------------
// Admin groups
// ---------------------------------------------------------------------------

export async function listGroups(): Promise<GroupSummary[]> {
  return request<GroupSummary[]>("/api/admin/groups");
}

export async function getGroupDetail(id: GroupId): Promise<GroupDetail | null> {
  try {
    return await request<GroupDetail>(`/api/admin/groups/${id}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

export async function createGroup(input: CreateGroupInput): Promise<GroupId> {
  const res = await post<{ id: GroupId }>("/api/admin/groups", input);
  return res.id;
}

export async function sendLeaderMessage(
  groupId: GroupId,
  text: string,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/leader-message`, { text });
}

export async function skipCurrentTurn(groupId: GroupId): Promise<void> {
  await post(`/api/admin/groups/${groupId}/skip`);
}

export async function pauseGroup(groupId: GroupId): Promise<void> {
  await post(`/api/admin/groups/${groupId}/pause`);
}

export async function resumeGroup(groupId: GroupId): Promise<void> {
  await post(`/api/admin/groups/${groupId}/resume`);
}

export async function stopGroup(groupId: GroupId): Promise<void> {
  await post(`/api/admin/groups/${groupId}/stop`);
}

export async function setTurnMode(
  groupId: GroupId,
  mode: TurnMode,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/turn-mode`, { mode });
}

export async function setMaxTurns(
  groupId: GroupId,
  maxTurns: number,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/max-turns`, { maxTurns });
}

export async function addMember(
  groupId: GroupId,
  input: MemberInput,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/members`, input);
}

export async function removeMember(
  groupId: GroupId,
  botId: string,
): Promise<void> {
  const res = await fetch(`/api/admin/groups/${groupId}/members/${botId}`, {
    method: "DELETE",
    credentials: "include",
  });
  if (!res.ok) {
    const text = await res.text();
    let message = `Request failed (${res.status})`;
    try {
      const data = JSON.parse(text) as { error?: unknown };
      if (typeof data.error === "string") message = data.error;
    } catch {
      // keep the generic message
    }
    throw new ApiError(res.status, message);
  }
}

export async function rotateMemberToken(
  groupId: GroupId,
  botId: string,
): Promise<string> {
  const res = await post<{ agentToken: string }>(
    `/api/admin/groups/${groupId}/members/${botId}/rotate-token`,
  );
  return res.agentToken;
}

// ---------------------------------------------------------------------------
// Public room (no auth)
// ---------------------------------------------------------------------------

export async function getRoomState(token: string): Promise<RoomState | null> {
  return request<RoomState | null>(`/api/room/${token}`);
}

export async function submitBotReply(
  token: string,
  expectedTurnSeq: number,
  content: string,
): Promise<ReplyResult> {
  return post<ReplyResult>(`/api/room/${token}/reply`, {
    expectedTurnSeq,
    content,
  });
}
