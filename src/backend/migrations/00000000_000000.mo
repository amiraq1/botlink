import List "mo:core/List";
import Map "mo:core/Map";
import AccessControl "mo:caffeineai-authorization/access-control";

module {
  type Status = { #running; #paused; #completed; #stopped };
  type Role = { #bot; #admin };

  type Conversation = {
    id : Nat;
    title : Text;
    topic : Text;
    status : Status;
    maxTurns : Nat;
    turnSeq : Nat;
    currentTurnBotId : Text;
    mockDelaySec : Nat;
    turnStartedAt : Int;
    createdAt : Int;
    updatedAt : Int;
  };

  type Participant = {
    conversationId : Nat;
    botId : Text;
    botName : Text;
    isMock : Bool;
    agentToken : Text;
  };

  type Message = {
    id : Nat;
    conversationId : Nat;
    turnSeq : ?Nat;
    senderBotId : Text;
    senderName : Text;
    role : Role;
    content : Text;
    createdAt : Int;
  };

  public type OldActor = {};
  public type NewActor = {
    accessControlState : AccessControl.AccessControlState;
    conversations : Map.Map<Nat, Conversation>;
    participants : Map.Map<Text, Participant>;
    messages : List.List<Message>;
    counters : { var nextConversationId : Nat; var nextMessageId : Nat };
  };

  public func migration(_ : OldActor) : NewActor {
    {
      accessControlState = AccessControl.initState();
      conversations = Map.empty();
      participants = Map.empty();
      messages = List.empty();
      counters = { var nextConversationId = 1; var nextMessageId = 1 };
    };
  };
};
