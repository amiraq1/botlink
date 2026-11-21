import List "mo:core/List";
import Map "mo:core/Map";
import AccessControl "mo:caffeineai-authorization/access-control";

module {
  // ---- Old shape (sessions) ----
  type OldStatus = { #running; #paused; #completed; #stopped };
  type OldRole = { #bot; #admin };

  type OldConversation = {
    id : Nat;
    title : Text;
    topic : Text;
    status : OldStatus;
    maxTurns : Nat;
    turnSeq : Nat;
    currentTurnBotId : Text;
    mockDelaySec : Nat;
    turnStartedAt : Int;
    createdAt : Int;
    updatedAt : Int;
  };

  type OldParticipant = {
    conversationId : Nat;
    botId : Text;
    botName : Text;
    isMock : Bool;
    agentToken : Text;
  };

  type OldMessage = {
    id : Nat;
    conversationId : Nat;
    turnSeq : ?Nat;
    senderBotId : Text;
    senderName : Text;
    role : OldRole;
    content : Text;
    createdAt : Int;
  };

  // ---- New shape (groups) ----
  type GroupStatus = { #running; #waiting_for_leader; #paused; #completed; #stopped };
  type TurnMode = { #auto_round_robin; #leader_round };
  type MessageRole = { #bot; #leader };

  type Group = {
    id : Nat;
    name : Text;
    description : Text;
    topic : Text;
    status : GroupStatus;
    turnMode : TurnMode;
    maxTurns : Nat;
    turnSeq : Nat;
    currentTurnBotId : ?Text;
    turnQueue : [Text];
    mockDelaySec : Nat;
    turnStartedAt : Int;
    createdAt : Int;
    updatedAt : Int;
  };

  type BotMember = {
    groupId : Nat;
    botId : Text;
    name : Text;
    roleTitle : Text;
    accentColor : Text;
    isMock : Bool;
    agentToken : Text;
    lastSeenAt : Int;
  };

  type Message = {
    id : Nat;
    groupId : Nat;
    turnSeq : ?Nat;
    senderBotId : Text;
    senderName : Text;
    senderColor : Text;
    role : MessageRole;
    content : Text;
    createdAt : Int;
  };

  public type OldActor = {
    accessControlState : AccessControl.AccessControlState;
    conversations : Map.Map<Nat, OldConversation>;
    participants : Map.Map<Text, OldParticipant>;
    messages : List.List<OldMessage>;
    counters : { var nextConversationId : Nat; var nextMessageId : Nat };
  };

  public type NewActor = {
    accessControlState : AccessControl.AccessControlState;
    groups : Map.Map<Nat, Group>;
    members : Map.Map<Text, BotMember>;
    messages : List.List<Message>;
    counters : { var nextGroupId : Nat; var nextMessageId : Nat };
  };

  func mapBotId(id : Text) : Text {
    if (id == "bot_a") { "bot_1" } else if (id == "bot_b") { "bot_2" } else if (id == "admin") { "leader" } else { id };
  };

  func colorFor(botId : Text) : Text {
    if (botId == "bot_1") { "#3B82F6" } else if (botId == "bot_2") { "#A855F7" } else if (botId == "leader") { "#1D6BFF" } else { "#10B981" };
  };

  func convertGroup(c : OldConversation) : Group {
    let status : GroupStatus = switch (c.status) {
      case (#running) { #running };
      case (#paused) { #paused };
      case (#completed) { #completed };
      case (#stopped) { #stopped };
    };
    let current : ?Text = switch (c.status) {
      case (#completed) { null };
      case (#stopped) { null };
      case (_) { ?mapBotId(c.currentTurnBotId) };
    };
    {
      id = c.id;
      name = if (c.title == "") { "فريق البوتات" } else { c.title };
      description = "مجموعة خاصة لتنسيق المهام وتطوير المشاريع";
      topic = c.topic;
      status;
      turnMode = #auto_round_robin;
      maxTurns = c.maxTurns;
      turnSeq = c.turnSeq;
      currentTurnBotId = current;
      turnQueue = [];
      mockDelaySec = c.mockDelaySec;
      turnStartedAt = c.turnStartedAt;
      createdAt = c.createdAt;
      updatedAt = c.updatedAt;
    };
  };

  func convertMember(p : OldParticipant) : BotMember {
    let botId = mapBotId(p.botId);
    {
      groupId = p.conversationId;
      botId;
      name = p.botName;
      roleTitle = "";
      accentColor = colorFor(botId);
      isMock = p.isMock;
      agentToken = p.agentToken;
      lastSeenAt = 0;
    };
  };

  func convertMessage(m : OldMessage) : Message {
    let senderBotId = mapBotId(m.senderBotId);
    let role : MessageRole = switch (m.role) {
      case (#bot) { #bot };
      case (#admin) { #leader };
    };
    {
      id = m.id;
      groupId = m.conversationId;
      turnSeq = m.turnSeq;
      senderBotId;
      senderName = m.senderName;
      senderColor = colorFor(senderBotId);
      role;
      content = m.content;
      createdAt = m.createdAt;
    };
  };

  public func migration(old : OldActor) : NewActor {
    {
      accessControlState = old.accessControlState;
      groups = old.conversations.map(func(_, c) = convertGroup(c));
      members = old.participants.map(func(_, p) = convertMember(p));
      messages = old.messages.map(convertMessage);
      counters = {
        var nextGroupId = old.counters.nextConversationId;
        var nextMessageId = old.counters.nextMessageId;
      };
    };
  };
};
