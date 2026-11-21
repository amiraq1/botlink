import Common "common";

module {
  public type GroupStatus = {
    #running;
    #waiting_for_leader;
    #paused;
    #completed;
    #stopped;
  };

  public type TurnMode = { #auto_round_robin; #leader_round };

  public type MessageRole = { #bot; #leader };

  public type Group = {
    id : Common.GroupId;
    name : Text; // default "فريق البوتات"
    description : Text; // default "مجموعة خاصة لتنسيق المهام وتطوير المشاريع"
    topic : Text;
    status : GroupStatus;
    turnMode : TurnMode;
    maxTurns : Nat; // default 20
    turnSeq : Nat; // starts at 1; completed groups store maxTurns + 1
    currentTurnBotId : ?Common.BotId; // null when waiting_for_leader, completed or stopped
    turnQueue : [Common.BotId]; // bots still to speak in the current leader round
    mockDelaySec : Nat; // default 12
    turnStartedAt : Common.Timestamp; // when the current turn began (mock reply delay)
    createdAt : Common.Timestamp;
    updatedAt : Common.Timestamp;
  };

  public type BotMember = {
    groupId : Common.GroupId;
    botId : Common.BotId; // "bot_1", "bot_2", ...
    name : Text;
    roleTitle : Text;
    accentColor : Text; // hex color, e.g. "#3B82F6"
    isMock : Bool;
    agentToken : Text; // permanent unique 32-char lowercase hex
    lastSeenAt : Common.Timestamp; // ns; 0 = never seen
  };

  public type Message = {
    id : Common.MessageId;
    groupId : Common.GroupId;
    turnSeq : ?Nat; // null for leader messages
    senderBotId : Common.BotId; // "leader" or a member botId
    senderName : Text;
    senderColor : Text;
    role : MessageRole;
    content : Text;
    createdAt : Common.Timestamp;
  };

  public type Counters = {
    var nextGroupId : Nat;
    var nextMessageId : Nat;
  };

  public type MemberInput = {
    name : Text;
    roleTitle : Text;
    accentColor : Text;
    isMock : Bool;
  };

  /// 2 to 10 members are enforced.
  public type CreateGroupInput = {
    name : Text;
    description : Text;
    topic : Text;
    turnMode : TurnMode;
    maxTurns : Nat;
    mockDelaySec : ?Nat;
    members : [MemberInput];
  };

  public type GroupSummary = {
    id : Common.GroupId;
    name : Text;
    description : Text;
    topic : Text;
    status : GroupStatus;
    turnMode : TurnMode;
    turnSeq : Nat;
    maxTurns : Nat;
    memberCount : Nat;
    onlineCount : Nat;
    createdAt : Common.Timestamp;
    updatedAt : Common.Timestamp;
  };

  /// Admin view of a member: includes the agent token and live presence.
  public type MemberView = {
    groupId : Common.GroupId;
    botId : Common.BotId;
    name : Text;
    roleTitle : Text;
    accentColor : Text;
    isMock : Bool;
    agentToken : Text;
    lastSeenAt : Common.Timestamp;
    online : Bool;
  };

  public type GroupDetail = {
    group : Group;
    members : [MemberView];
    messages : [Message];
  };
};
