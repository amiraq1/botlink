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
  hasMore: boolean;
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
  tools?: Array<{
    name: string;
    description: string;
    inputSchema: unknown;
    timeoutMs: number;
  }>;
}

export class ApiError extends Error {
  status: number;
  /** Seconds requested by a 429 `Retry-After` header, when the server sends one. */
  retryAfterSec: number | null;
  constructor(
    status: number,
    message: string,
    retryAfterSec: number | null = null,
  ) {
    super(message);
    this.status = status;
    this.retryAfterSec = retryAfterSec;
  }
}

async function request<T>(input: string, init?: RequestInit): Promise<T> {
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
    const retryAfter = Number.parseInt(
      res.headers.get("retry-after") ?? "",
      10,
    );
    throw new ApiError(
      res.status,
      message,
      Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter : null,
    );
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

/** Invalidate every signed-in admin session on the server. */
export async function revokeAllSessions(): Promise<void> {
  await post("/api/admin/sessions/revoke-all");
}

// ---------------------------------------------------------------------------
// Admin groups
// ---------------------------------------------------------------------------

export async function listGroups(): Promise<GroupSummary[]> {
  return request<GroupSummary[]>("/api/admin/groups");
}

export interface GroupDetailParams {
  /** Return only messages with id greater than this cursor (delta polling). */
  afterId?: number;
  /** Return at most this many older messages before the cursor. */
  beforeId?: number;
  limit?: number;
}

export async function getGroupDetail(
  id: GroupId,
  params: GroupDetailParams = {},
): Promise<GroupDetail | null> {
  const query = new URLSearchParams();
  if (params.afterId) query.set("after_id", String(params.afterId));
  if (params.beforeId) query.set("before_id", String(params.beforeId));
  if (params.limit) query.set("limit", String(params.limit));
  const suffix = query.toString();
  try {
    return await request<GroupDetail>(
      `/api/admin/groups/${id}${suffix ? `?${suffix}` : ""}`,
    );
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

// ---------------------------------------------------------------------------
// Tools & Member Tools (Admin)
// ---------------------------------------------------------------------------

export type ToolKind = "builtin" | "http_webhook";

export interface Tool {
  id: number;
  name: string;
  description: string;
  inputSchema: unknown;
  timeoutMs: number;
  enabled: boolean;
  kind: ToolKind;
  webhookUrl?: string;
  hasWebhookHeaders: boolean;
  hasWebhookSecret: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface ToolInput {
  name: string;
  description: string;
  inputSchema: unknown;
  timeoutMs: number;
  enabled: boolean;
  kind: ToolKind;
  webhookUrl?: string;
  webhookHeaders?: Record<string, string>;
  webhookSecret?: string;
}

export interface MemberTool {
  groupId: number;
  botId: string;
  toolId: number;
  toolName: string;
  enabled: boolean;
  maxCallsPerTurn: number;
}

export type ToolCallStatus =
  | "pending"
  | "success"
  | "error"
  | "cancelled"
  | "timeout";

export interface ToolCall {
  id: number;
  groupId: number;
  botId: string;
  toolId: number;
  toolName: string;
  callId: string;
  turnSeq: number;
  status: ToolCallStatus;
  arguments: unknown;
  result: string;
  errorMsg: string;
  durationMs: number;
  createdAt: number;
  completedAt: number;
}

export async function listTools(): Promise<Tool[]> {
  return request<Tool[]>("/api/admin/tools");
}

export async function getTool(id: number): Promise<Tool> {
  return request<Tool>(`/api/admin/tools/${id}`);
}

export async function createTool(input: ToolInput): Promise<{ id: number }> {
  return post<{ id: number }>("/api/admin/tools", input);
}

export async function updateTool(
  id: number,
  input: Partial<ToolInput>,
): Promise<void> {
  await request<void>(`/api/admin/tools/${id}`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}

export async function enableTool(id: number, enabled: boolean): Promise<void> {
  await post<{ ok: boolean }>(`/api/admin/tools/${id}/enable`, { enabled });
}

export async function deleteTool(id: number): Promise<void> {
  await request<void>(`/api/admin/tools/${id}`, {
    method: "DELETE",
  });
}

export interface ToolTestResult {
  ok: boolean;
  result?: unknown;
  error?: string;
  durationMs: number;
}

export async function testTool(
  id: number,
  args: Record<string, unknown>,
): Promise<ToolTestResult> {
  return post<ToolTestResult>(`/api/admin/tools/${id}/test`, {
    arguments: args,
  });
}

export async function listGroupMemberTools(
  groupId: number,
): Promise<MemberTool[]> {
  return request<MemberTool[]>(`/api/admin/groups/${groupId}/member-tools`);
}

export async function setMemberTool(
  groupId: number,
  botId: string,
  toolId: number,
  enabled: boolean,
  maxCallsPerTurn: number,
): Promise<void> {
  await post<{ ok: boolean }>(
    `/api/admin/groups/${groupId}/members/${botId}/tools/${toolId}`,
    { enabled, maxCallsPerTurn },
  );
}

export async function removeMemberTool(
  groupId: number,
  botId: string,
  toolId: number,
): Promise<void> {
  await request<void>(
    `/api/admin/groups/${groupId}/members/${botId}/tools/${toolId}`,
    { method: "DELETE" },
  );
}

export async function listToolCallsAudit(
  groupId: number,
  limit?: number,
): Promise<ToolCall[]> {
  const query = limit ? `?limit=${limit}` : "";
  return request<ToolCall[]>(`/api/admin/groups/${groupId}/tool-calls${query}`);
}

// ---------------------------------------------------------------------------
// LLM connections & managed agents (Admin) - Phase 4
// ---------------------------------------------------------------------------

export type ProviderKind = "deepseek";

export interface LLMConnection {
  id: number;
  provider: ProviderKind;
  label: string;
  modelHint: string;
  keyLast4: string;
  hasKey: boolean;
  lastCheckedAt: number;
  lastCheckStatus: string;
  lastCheckError: string;
  createdAt: number;
  updatedAt: number;
}

export interface LLMConnectionInput {
  provider: ProviderKind;
  label: string;
  apiKey: string;
  modelHint: string;
}

export interface ModelPrice {
  connectionId: number;
  model: string;
  inputMicrosPer1M: number;
  outputMicrosPer1M: number;
  currency: string;
  updatedAt: number;
}

export interface ManagedAgent {
  id: number;
  groupId: number;
  botId: string;
  botName: string;
  roleTitle: string;
  connectionId: number;
  provider: string;
  model: string;
  persona: string;
  maxOutputTokens: number;
  contextMaxMessages: number;
  contextMaxChars: number;
  dailyRunLimit: number;
  dailyBudgetMicros?: number | null;
  enabled: boolean;
  createdAt: number;
  updatedAt: number;
}

export interface ManagedAgentInput {
  connectionId: number;
  model: string;
  persona: string;
  maxOutputTokens: number;
  contextMaxMessages: number;
  contextMaxChars: number;
  dailyRunLimit: number;
  dailyBudgetMicros?: number | null;
  enabled: boolean;
}

export interface AgentRun {
  id: number;
  groupId: number;
  botId: string;
  agentId?: number | null;
  turnSeq: number;
  turnEpoch: number;
  turnStartedAt: number;
  attempt: number;
  retryOf?: number | null;
  status: string;
  provider: string;
  model: string;
  requestMsgCount: number;
  requestChars: number;
  promptTokens?: number | null;
  completionTokens?: number | null;
  totalTokens?: number | null;
  estCostMicros?: number | null;
  costMicros?: number | null;
  costBasis: string;
  costConfirmed: boolean;
  responseChars: number;
  messageId?: number | null;
  errorCode?: string;
  errorMsg?: string;
  createdAt: number;
  startedAt: number;
  finishedAt: number;
}

export interface ManagedAgentUsage {
  runsLast24h: number;
  dailyRunLimit: number;
  spentMicros: number;
  confirmedMicros: number;
  computedMicros: number;
  unknownRuns: number;
  dailyBudgetMicros?: number | null;
  costKnown: boolean;
  budgetState: string;
  budgetBlocked: boolean;
  budgetBlockReason: string;
}

export interface LLMModelProbe {
  id: string;
  name?: string;
  ownedBy?: string;
  contextWindow?: number;
  maxOutputTokens?: number;
}

export interface ConnectionCheckResult {
  status: string;
  models: LLMModelProbe[];
  error?: string;
  checkedAt: number;
  connection: LLMConnection;
}

export async function listLLMConnections(): Promise<LLMConnection[]> {
  return request<LLMConnection[]>("/api/admin/llm/connections");
}

export async function createLLMConnection(
  input: LLMConnectionInput,
): Promise<LLMConnection> {
  return post<LLMConnection>("/api/admin/llm/connections", input);
}

export async function updateLLMConnection(
  id: number,
  input: LLMConnectionInput,
): Promise<LLMConnection> {
  return request<LLMConnection>(`/api/admin/llm/connections/${id}`, {
    method: "PUT",
    body: JSON.stringify(input),
  });
}

export async function deleteLLMConnection(id: number): Promise<void> {
  await request<void>(`/api/admin/llm/connections/${id}`, { method: "DELETE" });
}

export async function checkLLMConnection(
  id: number,
): Promise<ConnectionCheckResult> {
  return post<ConnectionCheckResult>(`/api/admin/llm/connections/${id}/check`);
}

export async function listModelPrices(id: number): Promise<ModelPrice[]> {
  return request<ModelPrice[]>(`/api/admin/llm/connections/${id}/prices`);
}

export async function setModelPrice(
  connectionId: number,
  model: string,
  input: {
    inputMicrosPer1M: number;
    outputMicrosPer1M: number;
    currency?: string;
  },
): Promise<ModelPrice> {
  return request<ModelPrice>(
    `/api/admin/llm/connections/${connectionId}/prices/${encodeURIComponent(model)}`,
    {
      method: "PUT",
      body: JSON.stringify(input),
    },
  );
}

export async function deleteModelPrice(
  connectionId: number,
  model: string,
): Promise<void> {
  await request<void>(
    `/api/admin/llm/connections/${connectionId}/prices/${encodeURIComponent(model)}`,
    { method: "DELETE" },
  );
}

export async function listManagedAgents(
  groupId: number,
): Promise<ManagedAgent[]> {
  return request<ManagedAgent[]>(`/api/admin/groups/${groupId}/managed-agents`);
}

export async function createManagedAgent(
  groupId: number,
  member: { name: string; roleTitle: string; accentColor?: string },
  agent: ManagedAgentInput,
): Promise<ManagedAgent> {
  return post<ManagedAgent>(`/api/admin/groups/${groupId}/managed-agents`, {
    member,
    agent,
  });
}

export async function updateManagedAgent(
  groupId: number,
  botId: string,
  agent: ManagedAgentInput,
): Promise<ManagedAgent> {
  return request<ManagedAgent>(
    `/api/admin/groups/${groupId}/managed-agents/${botId}`,
    {
      method: "PUT",
      body: JSON.stringify(agent),
    },
  );
}

export async function deleteManagedAgent(
  groupId: number,
  botId: string,
): Promise<void> {
  await request<void>(`/api/admin/groups/${groupId}/managed-agents/${botId}`, {
    method: "DELETE",
  });
}

export async function listAgentRuns(groupId: number): Promise<AgentRun[]> {
  return request<AgentRun[]>(`/api/admin/groups/${groupId}/agent-runs`);
}

export async function retryAgentRun(
  groupId: number,
  runId: number,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/agent-runs/${runId}/retry`);
}

export async function cancelAgentRun(
  groupId: number,
  runId: number,
): Promise<void> {
  await post(`/api/admin/groups/${groupId}/agent-runs/${runId}/cancel`);
}

export async function getManagedAgentUsage(
  groupId: number,
  botId: string,
): Promise<ManagedAgentUsage> {
  return request<ManagedAgentUsage>(
    `/api/admin/groups/${groupId}/agents/${botId}/usage`,
  );
}
