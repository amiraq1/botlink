import List "mo:core/List";
import Map "mo:core/Map";
import Time "mo:core/Time";
import Groups "../types/groups";
import Rooms "../types/rooms";
import RoomsLib "../lib/rooms";

/// Public agent-room API: NO authorization, callable anonymously.
mixin (
  groups : Map.Map<Nat, Groups.Group>,
  members : Map.Map<Text, Groups.BotMember>,
  messages : List.List<Groups.Message>,
  counters : Groups.Counters,
) {
  /// Update call (not query): refreshes the member's lastSeenAt and applies a due mock reply.
  /// Returns null for an unknown token. Never exposes tokens.
  public shared func getRoomState(token : Text) : async ?Rooms.RoomState {
    let now = Time.now();
    RoomsLib.touchMember(members, token, now);
    switch (members.get(token)) {
      case null { null };
      case (?m) {
        RoomsLib.processDueMockReply(groups, members, messages, counters, m.groupId, now);
        RoomsLib.getRoomState(groups, members, messages, token, now);
      };
    };
  };

  /// Never traps for stale/duplicate/wrong-turn/non-running: returns { accepted = false }.
  /// Also refreshes lastSeenAt.
  public shared func submitBotReply(token : Text, expectedTurnSeq : Nat, rawContent : Text) : async Rooms.ReplyResult {
    let result = RoomsLib.submitBotReply(groups, members, messages, counters, token, expectedTurnSeq, rawContent, Time.now());
    if (result.accepted) {
      switch (members.get(token)) {
        case (?m) { RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, m.groupId) };
        case null {};
      };
    };
    result;
  };
};
