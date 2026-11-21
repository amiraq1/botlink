import List "mo:core/List";
import Map "mo:core/Map";
import Random "mo:core/Random";
import Result "mo:core/Result";
import Runtime "mo:core/Runtime";
import Time "mo:core/Time";
import AccessControl "mo:caffeineai-authorization/access-control";
import Common "../types/common";
import Groups "../types/groups";
import GroupsLib "../lib/groups";
import RoomsLib "../lib/rooms";

/// Admin-only API. Every method rejects anonymous / non-admin callers with
/// "Unauthorized: Only admins can perform this action".
mixin (
  accessControlState : AccessControl.AccessControlState,
  groups : Map.Map<Nat, Groups.Group>,
  members : Map.Map<Text, Groups.BotMember>,
  messages : List.List<Groups.Message>,
  counters : Groups.Counters,
) {
  func requireAdmin(caller : Principal) {
    if (not AccessControl.isAdmin(accessControlState, caller)) {
      Runtime.trap("Unauthorized: Only admins can perform this action");
    };
  };

  func unwrap(r : Result.Result<(), Text>) {
    switch (r) {
      case (#ok(_)) {};
      case (#err(e)) { Runtime.trap(e) };
    };
  };

  /// 2 to 10 members enforced. Returns the new group id.
  public shared ({ caller }) func createGroup(input : Groups.CreateGroupInput) : async Common.GroupId {
    requireAdmin(caller);
    let count = input.members.size();
    if (count < 2 or count > 10) { Runtime.trap("A group needs between 2 and 10 members") };
    // One permanent 16-byte random token (32 hex chars) per member; each random blob holds 32 bytes.
    let tokens = List.empty<Text>();
    var bytes : [Nat8] = [];
    var i = 0;
    while (i < count) {
      if (i % 2 == 0) { bytes := (await Random.blob()).toArray() };
      tokens.add(GroupsLib.hexToken(bytes, (i % 2) * 16));
      i += 1;
    };
    switch (GroupsLib.createGroup(groups, members, counters, input, tokens.toArray(), Time.now())) {
      case (#ok(id)) {
        RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, id);
        id;
      };
      case (#err(e)) { Runtime.trap(e) };
    };
  };

  public shared ({ caller }) func listGroups() : async [Groups.GroupSummary] {
    requireAdmin(caller);
    GroupsLib.listGroups(groups, members, Time.now());
  };

  /// Update call (not query) so it can apply a due mock reply before returning.
  public shared ({ caller }) func getGroupDetail(id : Common.GroupId) : async ?Groups.GroupDetail {
    requireAdmin(caller);
    let now = Time.now();
    RoomsLib.processDueMockReply(groups, members, messages, counters, id, now);
    GroupsLib.getGroupDetail(groups, members, messages, id, now);
  };

  public shared ({ caller }) func sendLeaderMessage(groupId : Common.GroupId, text : Text) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.sendLeaderMessage(groups, members, messages, counters, groupId, RoomsLib.sanitize(text), Time.now()));
    RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, groupId);
  };

  public shared ({ caller }) func skipCurrentTurn(groupId : Common.GroupId) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.skipCurrentTurn(groups, members, groupId, Time.now()));
    RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, groupId);
  };

  public shared ({ caller }) func pauseGroup(groupId : Common.GroupId) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.pauseGroup(groups, groupId, Time.now()));
  };

  public shared ({ caller }) func resumeGroup(groupId : Common.GroupId) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.resumeGroup(groups, members, groupId, Time.now()));
    RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, groupId);
  };

  public shared ({ caller }) func stopGroup(groupId : Common.GroupId) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.stopGroup(groups, groupId, Time.now()));
  };

  public shared ({ caller }) func setTurnMode(groupId : Common.GroupId, mode : Groups.TurnMode) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.setTurnMode(groups, members, groupId, mode, Time.now()));
    RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, groupId);
  };

  public shared ({ caller }) func setMaxTurns(groupId : Common.GroupId, maxTurns : Nat) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.setMaxTurns(groups, groupId, maxTurns, Time.now()));
  };

  /// Enforces max 10 members.
  public shared ({ caller }) func addMember(
    groupId : Common.GroupId,
    name : Text,
    roleTitle : Text,
    accentColor : Text,
    isMock : Bool,
  ) : async () {
    requireAdmin(caller);
    let token = GroupsLib.hexToken((await Random.blob()).toArray(), 0);
    unwrap(GroupsLib.addMember(groups, members, messages, groupId, { name; roleTitle; accentColor; isMock }, token, Time.now()));
  };

  /// Enforces min 2 members; fixes the current turn and queue if the removed bot held them.
  public shared ({ caller }) func removeMember(groupId : Common.GroupId, botId : Common.BotId) : async () {
    requireAdmin(caller);
    unwrap(GroupsLib.removeMember(groups, members, groupId, botId, Time.now()));
    RoomsLib.scheduleMockReply<system>(groups, members, messages, counters, groupId);
  };
};
