import List "mo:core/List";
import Map "mo:core/Map";
import Text "mo:core/Text";
import Time "mo:core/Time";
import Timer "mo:core/Timer";
import AccessControl "mo:caffeineai-authorization/access-control";
import MixinAuthorization "mo:caffeineai-authorization/MixinAuthorization";
import Expose "mo:caffeineai-oql/Expose";
import Entity "mo:caffeineai-oql/Entity";
import MapEntity "mo:caffeineai-oql/MapEntity";
import ListEntity "mo:caffeineai-oql/ListEntity";
import NatValue "mo:caffeineai-oql/NatValue";
import IntValue "mo:caffeineai-oql/IntValue";
import TextValue "mo:caffeineai-oql/TextValue";
import BoolValue "mo:caffeineai-oql/BoolValue";
import Groups "types/groups";
import RoomsLib "lib/rooms";
import GroupsApi "mixins/groups-api";
import RoomsApi "mixins/rooms-api";
import ApiDocMixin "mixins/api-doc";

actor {
  let accessControlState : AccessControl.AccessControlState;
  include MixinAuthorization(accessControlState, null);

  let groups : Map.Map<Nat, Groups.Group>;
  let members : Map.Map<Text, Groups.BotMember>; // keyed by agentToken
  let messages : List.List<Groups.Message>;
  let counters : Groups.Counters;

  func statusText(s : Groups.GroupStatus) : Text {
    switch (s) {
      case (#running) { "running" };
      case (#waiting_for_leader) { "waiting_for_leader" };
      case (#paused) { "paused" };
      case (#completed) { "completed" };
      case (#stopped) { "stopped" };
    };
  };

  func modeText(m : Groups.TurnMode) : Text {
    switch (m) {
      case (#auto_round_robin) { "auto_round_robin" };
      case (#leader_round) { "leader_round" };
    };
  };

  func roleText(r : Groups.MessageRole) : Text {
    switch (r) {
      case (#bot) { "bot" };
      case (#leader) { "leader" };
    };
  };

  // Group data is admin-only and has no per-user owner, so every table is controller-only.
  // Agent tokens are never exposed through OQL.
  include Expose({
    entities = [
      groups.toEntityManual("group", "Group", "id")
        .sample({
          id = 0;
          name = "";
          description = "";
          topic = "";
          status = #running;
          turnMode = #auto_round_robin;
          maxTurns = 0;
          turnSeq = 0;
          currentTurnBotId = ?"";
          turnQueue = [];
          mockDelaySec = 0;
          turnStartedAt = 0;
          createdAt = 0;
          updatedAt = 0;
        })
        .payload("id", func g = g.id)
        .payload("name", func g = g.name)
        .payload("description", func g = g.description)
        .payload("topic", func g = g.topic)
        .payload("status", func g = statusText(g.status))
        .payload("turnMode", func g = modeText(g.turnMode))
        .payload("maxTurns", func g = g.maxTurns)
        .payload("turnSeq", func g = g.turnSeq)
        .payload("currentTurnBotId", func g = g.currentTurnBotId)
        .payload("turnQueue", func g = g.turnQueue.values().join(","))
        .payload("mockDelaySec", func g = g.mockDelaySec)
        .payload("createdAt", func g = g.createdAt)
        .payload("updatedAt", func g = g.updatedAt)
        .controllerOnly()
        .build(),
      members.toEntityManual("member", "Member", "key")
        .sample({
          groupId = 0;
          botId = "";
          name = "";
          roleTitle = "";
          accentColor = "";
          isMock = false;
          agentToken = "";
          lastSeenAt = 0;
        })
        .payload("key", func m = m.groupId.toText() # ":" # m.botId)
        .payload("groupId", func m = m.groupId)
        .edge("groupId", "group")
        .payload("botId", func m = m.botId)
        .payload("name", func m = m.name)
        .payload("roleTitle", func m = m.roleTitle)
        .payload("accentColor", func m = m.accentColor)
        .payload("isMock", func m = m.isMock)
        .payload("lastSeenAt", func m = m.lastSeenAt)
        .controllerOnly()
        .build(),
      messages.toEntityManual("message", "Message", "id")
        .sample({
          id = 0;
          groupId = 0;
          turnSeq = ?0;
          senderBotId = "";
          senderName = "";
          senderColor = "";
          role = #bot;
          content = "";
          createdAt = 0;
        })
        .payload("id", func m = m.id)
        .payload("groupId", func m = m.groupId)
        .edge("groupId", "group")
        .payload("turnSeq", func m = m.turnSeq)
        .payload("senderBotId", func m = m.senderBotId)
        .payload("senderName", func m = m.senderName)
        .payload("senderColor", func m = m.senderColor)
        .payload("role", func m = roleText(m.role))
        .payload("content", func m = m.content)
        .payload("createdAt", func m = m.createdAt)
        .controllerOnly()
        .build(),
    ];
  });

  include GroupsApi(accessControlState, groups, members, messages, counters);
  include RoomsApi(groups, members, messages, counters);
  include ApiDocMixin();

  // Safety net for mock members: re-armed on every (re)start, so replies still happen after
  // upgrades even though per-turn timers are lost.
  ignore Timer.recurringTimer<system>(
    #seconds(15),
    func() : async () {
      RoomsLib.sweepMockReplies(groups, members, messages, counters, Time.now());
    },
  );
};
