import Groups "groups";

module {
  /// Public agent-room view. Never contains any agent token.
  public type RoomState = {
    status : Groups.GroupStatus;
    botId : Text;
    botName : Text;
    roleTitle : Text;
    groupName : Text;
    onlineNames : [Text];
    turnSeq : Nat;
    maxTurns : Nat;
    topic : Text;
    isYourTurn : Bool;
    messages : [Groups.Message]; // last 15, oldest first
  };

  public type ReplyResult = { accepted : Bool };
};
