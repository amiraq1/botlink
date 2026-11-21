import type { Principal } from "@icp-sdk/core/principal";
export interface Some<T> {
    __kind__: "Some";
    value: T;
}
export interface None {
    __kind__: "None";
}
export type Option<T> = Some<T> | None;
export type BotId = string;
export interface Cell {
    value: Value;
    name: string;
}
export interface CreateGroupInput {
    turnMode: TurnMode;
    topic: string;
    members: Array<MemberInput>;
    name: string;
    mockDelaySec?: bigint;
    description: string;
    maxTurns: bigint;
}
export type Error_ = {
    __kind__: "FrontendOriginsNotConfigured";
    FrontendOriginsNotConfigured: null;
} | {
    __kind__: "MixedSsoSources";
    MixedSsoSources: {
        otherKeys: Array<string>;
        ssoKeys: Array<string>;
    };
} | {
    __kind__: "Stale";
    Stale: {
        ageNs: bigint;
    };
} | {
    __kind__: "MalformedCandid";
    MalformedCandid: null;
} | {
    __kind__: "AmbiguousAttribute";
    AmbiguousAttribute: {
        field: string;
        sources: Array<string>;
    };
} | {
    __kind__: "NoAttributes";
    NoAttributes: null;
} | {
    __kind__: "UnknownNonce";
    UnknownNonce: null;
} | {
    __kind__: "UntrustedSsoSource";
    UntrustedSsoSource: {
        domain: string;
    };
} | {
    __kind__: "MissingField";
    MissingField: string;
} | {
    __kind__: "FrontendOriginMismatch";
    FrontendOriginMismatch: {
        got: string;
        expected: Array<string>;
    };
};
export interface Group {
    id: GroupId;
    status: GroupStatus;
    turnMode: TurnMode;
    topic: string;
    turnQueue: Array<BotId>;
    name: string;
    createdAt: Timestamp;
    mockDelaySec: bigint;
    turnStartedAt: Timestamp;
    description: string;
    currentTurnBotId?: BotId;
    updatedAt: Timestamp;
    turnSeq: bigint;
    maxTurns: bigint;
}
export interface GroupDetail {
    members: Array<MemberView>;
    messages: Array<Message>;
    group: Group;
}
export type GroupId = bigint;
export interface GroupSummary {
    id: GroupId;
    status: GroupStatus;
    turnMode: TurnMode;
    onlineCount: bigint;
    topic: string;
    name: string;
    createdAt: Timestamp;
    memberCount: bigint;
    description: string;
    updatedAt: Timestamp;
    turnSeq: bigint;
    maxTurns: bigint;
}
export interface MemberInput {
    name: string;
    roleTitle: string;
    isMock: boolean;
    accentColor: string;
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
    turnSeq?: bigint;
    senderColor: string;
    senderName: string;
}
export type MessageId = bigint;
export interface ReplyResult {
    accepted: boolean;
}
export interface Result {
    hasMore: boolean;
    rows: Array<Array<Cell>>;
}
export type Result__1 = {
    __kind__: "ok";
    ok: null;
} | {
    __kind__: "err";
    err: Error_;
};
export interface RoomState {
    status: GroupStatus;
    topic: string;
    isYourTurn: boolean;
    messages: Array<Message>;
    botName: string;
    roleTitle: string;
    onlineNames: Array<string>;
    turnSeq: bigint;
    botId: string;
    maxTurns: bigint;
    groupName: string;
}
export type Timestamp = bigint;
export type Value = {
    __kind__: "int";
    int: bigint;
} | {
    __kind__: "nat";
    nat: bigint;
} | {
    __kind__: "float";
    float: number;
} | {
    __kind__: "bool";
    bool: boolean;
} | {
    __kind__: "null";
    null: null;
} | {
    __kind__: "text";
    text: string;
};
export enum GroupStatus {
    waiting_for_leader = "waiting_for_leader",
    stopped = "stopped",
    completed = "completed",
    running = "running",
    paused = "paused"
}
export enum MessageRole {
    bot = "bot",
    leader = "leader"
}
export enum TurnMode {
    auto_round_robin = "auto_round_robin",
    leader_round = "leader_round"
}
export enum UserRole {
    admin = "admin",
    user = "user",
    guest = "guest"
}
export interface backendInterface {
    /**
     * / Enforces max 10 members.
     */
    addMember(groupId: GroupId, name: string, roleTitle: string, accentColor: string, isMock: boolean): Promise<void>;
    assignCallerUserRole(user: Principal, role: UserRole): Promise<void>;
    /**
     * / 2 to 10 members enforced. Returns the new group id.
     */
    createGroup(input: CreateGroupInput): Promise<GroupId>;
    execute(qJson: string): Promise<Result>;
    getApiDoc(): Promise<string>;
    getCallerUserRole(): Promise<UserRole>;
    /**
     * / Update call (not query) so it can apply a due mock reply before returning.
     */
    getGroupDetail(id: GroupId): Promise<GroupDetail | null>;
    /**
     * / Update call (not query): refreshes the member's lastSeenAt and applies a due mock reply.
     * / Returns null for an unknown token. Never exposes tokens.
     */
    getRoomState(token: string): Promise<RoomState | null>;
    isCallerAdmin(): Promise<boolean>;
    listGroups(): Promise<Array<GroupSummary>>;
    pauseGroup(groupId: GroupId): Promise<void>;
    /**
     * / Enforces min 2 members; fixes the current turn and queue if the removed bot held them.
     */
    removeMember(groupId: GroupId, botId: BotId): Promise<void>;
    resumeGroup(groupId: GroupId): Promise<void>;
    schema(): Promise<string>;
    sendLeaderMessage(groupId: GroupId, text: string): Promise<void>;
    setMaxTurns(groupId: GroupId, maxTurns: bigint): Promise<void>;
    setTurnMode(groupId: GroupId, mode: TurnMode): Promise<void>;
    skipCurrentTurn(groupId: GroupId): Promise<void>;
    stopGroup(groupId: GroupId): Promise<void>;
    /**
     * / Never traps for stale/duplicate/wrong-turn/non-running: returns { accepted = false }.
     * / Also refreshes lastSeenAt.
     */
    submitBotReply(token: string, expectedTurnSeq: bigint, rawContent: string): Promise<ReplyResult>;
}
