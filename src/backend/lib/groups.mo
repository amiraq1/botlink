import List "mo:core/List";
import Map "mo:core/Map";
import Nat "mo:core/Nat";
import Result "mo:core/Result";
import Text "mo:core/Text";
import Common "../types/common";
import Groups "../types/groups";

/// Group management logic (admin side): creation, membership, status/mode changes, leader messages,
/// plus the shared turn-advance helpers used by the room engine.
module {
  public let defaultName = "فريق البوتات";
  public let defaultDescription = "مجموعة خاصة لتنسيق المهام وتطوير المشاريع";

  let palette = [
    "#3B82F6",
    "#A855F7",
    "#EC4899",
    "#10B981",
    "#F59E0B",
    "#06B6D4",
    "#EF4444",
    "#84CC16",
    "#8B5CF6",
    "#14B8A6",
  ];

  let onlineWindowNs : Int = 45_000_000_000;

  /// A member is online when it is a mock or was seen within the last 45 seconds.
  public func isOnline(member : Groups.BotMember, now : Common.Timestamp) : Bool {
    member.isMock or (now - member.lastSeenAt <= onlineWindowNs);
  };

  /// Numeric suffix of "bot_N" (0 when malformed).
  public func botNum(botId : Text) : Nat {
    switch (botId.stripStart(#text "bot_")) {
      case (?rest) { rest.toNat() ?? 0 };
      case null { 0 };
    };
  };

  public func trimText(t : Text) : Text {
    t.trim(#predicate(func c = c.isWhitespace()));
  };

  public func capText(t : Text, limit : Nat) : Text {
    if (t.size() > limit) { Text.fromIter(t.toIter().take(limit)) } else { t };
  };

  /// Members of one group in botId order (bot_1, bot_2, ...).
  public func membersOf(members : Map.Map<Text, Groups.BotMember>, groupId : Common.GroupId) : [Groups.BotMember] {
    members.values().filter(func m = m.groupId == groupId).toArray().sort(
      func(a, b) = Nat.compare(botNum(a.botId), botNum(b.botId))
    );
  };

  /// 32-char lowercase hex token from 16 random bytes starting at `offset`.
  public func hexToken(bytes : [Nat8], offset : Nat) : Text {
    let digits = "0123456789abcdef".toArray();
    let out = List.empty<Char>();
    var i = 0;
    while (i < 16) {
      let b = if (offset + i < bytes.size()) { bytes[offset + i].toNat() } else { 0 };
      out.add(digits[b / 16]);
      out.add(digits[b % 16]);
      i += 1;
    };
    Text.fromIter(out.values());
  };

  func isHexColor(t : Text) : Bool {
    let cs = t.toArray();
    let n = cs.size();
    if (n != 4 and n != 7) { return false };
    if (cs[0] != '#') { return false };
    var i = 1;
    while (i < n) {
      let code = cs[i].toNat32();
      let ok = (code >= 48 and code <= 57) or (code >= 65 and code <= 70) or (code >= 97 and code <= 102);
      if (not ok) { return false };
      i += 1;
    };
    true;
  };

  func buildMember(groupId : Common.GroupId, num : Nat, input : Groups.MemberInput, token : Text) : Groups.BotMember {
    let rawName = capText(trimText(input.name), 40);
    let color = trimText(input.accentColor);
    {
      groupId = groupId;
      botId = "bot_" # num.toText();
      name = if (rawName == "") { "Bot " # num.toText() } else { rawName };
      roleTitle = capText(trimText(input.roleTitle), 60);
      accentColor = if (isHexColor(color)) { color } else { palette[(num + 9) % 10] };
      isMock = input.isMock;
      agentToken = token;
      lastSeenAt = 0;
    };
  };

  public func memberView(m : Groups.BotMember, now : Common.Timestamp) : Groups.MemberView {
    {
      groupId = m.groupId;
      botId = m.botId;
      name = m.name;
      roleTitle = m.roleTitle;
      accentColor = m.accentColor;
      isMock = m.isMock;
      agentToken = m.agentToken;
      lastSeenAt = m.lastSeenAt;
      online = isOnline(m, now);
    };
  };

  func hasMember(ms : [Groups.BotMember], botId : ?Common.BotId) : Bool {
    switch (botId) {
      case (?b) { ms.any(func m = m.botId == b) };
      case null { false };
    };
  };

  /// Next speaker in circular order after `after` (the speaker itself is skipped): the first Online
  /// member; when nobody else is online, simply the next member in order.
  public func nextAutoSpeaker(ms : [Groups.BotMember], after : ?Common.BotId, now : Common.Timestamp) : ?Common.BotId {
    let n = ms.size();
    if (n == 0) { return null };
    let afterId = switch (after) {
      case (?a) { a };
      case null { "" };
    };
    let afterNum = botNum(afterId);
    var start = 0;
    if (afterId != "") {
      for (m in ms.values()) {
        if (botNum(m.botId) <= afterNum) { start += 1 };
      };
    };
    var k = 0;
    while (k < n) {
      let candidate = ms[(start + k) % n];
      if (candidate.botId != afterId and isOnline(candidate, now)) {
        return ?(candidate.botId);
      };
      k += 1;
    };
    ?(ms[start % n].botId);
  };

  /// Pops the first still-existing botId off the queue.
  func popQueue(queue : [Common.BotId], ms : [Groups.BotMember]) : (?Common.BotId, [Common.BotId]) {
    var i = 0;
    while (i < queue.size()) {
      let id = queue[i];
      if (ms.any(func m = m.botId == id)) {
        return (?id, queue.sliceToArray(i + 1, queue.size()));
      };
      i += 1;
    };
    (null, []);
  };

  /// Group state after the current speaker has delivered a reply (real or mock): turnSeq increments
  /// atomically and the next speaker / status is chosen according to the turn mode.
  public func advanceAfterReply(g : Groups.Group, ms : [Groups.BotMember], now : Common.Timestamp) : Groups.Group {
    let newSeq = g.turnSeq + 1;
    if (newSeq > g.maxTurns) {
      return {
        g with
        status = #completed;
        turnSeq = newSeq;
        currentTurnBotId = null;
        turnQueue = [];
        turnStartedAt = now;
        updatedAt = now;
      };
    };
    switch (g.turnMode) {
      case (#auto_round_robin) {
        {
          g with
          status = #running;
          turnSeq = newSeq;
          currentTurnBotId = nextAutoSpeaker(ms, g.currentTurnBotId, now);
          turnQueue = [];
          turnStartedAt = now;
          updatedAt = now;
        };
      };
      case (#leader_round) {
        let (next, rest) = popQueue(g.turnQueue, ms);
        switch (next) {
          case (?_) {
            {
              g with
              status = #running;
              turnSeq = newSeq;
              currentTurnBotId = next;
              turnQueue = rest;
              turnStartedAt = now;
              updatedAt = now;
            };
          };
          case null {
            {
              g with
              status = #waiting_for_leader;
              turnSeq = newSeq;
              currentTurnBotId = null;
              turnQueue = [];
              turnStartedAt = now;
              updatedAt = now;
            };
          };
        };
      };
    };
  };

  /// Validates 2..10 members and creates the group plus its members (botIds bot_1..bot_n).
  /// `tokens` holds one permanent agent token per member, in member order.
  public func createGroup(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    counters : Groups.Counters,
    input : Groups.CreateGroupInput,
    tokens : [Text],
    now : Common.Timestamp,
  ) : Result.Result<Common.GroupId, Text> {
    let n = input.members.size();
    if (n < 2 or n > 10) { return #err("A group needs between 2 and 10 members") };
    if (tokens.size() != n) { return #err("Token count does not match member count") };
    for (t in tokens.values()) {
      if (members.containsKey(t)) { return #err("Token collision, please retry") };
    };
    let id = counters.nextGroupId;
    counters.nextGroupId += 1;
    for ((i, mi) in input.members.enumerate()) {
      let m = buildMember(id, i + 1, mi, tokens[i]);
      members.add(m.agentToken, m);
    };
    let name = capText(trimText(input.name), 80);
    let description = capText(trimText(input.description), 300);
    let (status, current) : (Groups.GroupStatus, ?Common.BotId) = switch (input.turnMode) {
      case (#auto_round_robin) { (#running, ?"bot_1") };
      case (#leader_round) { (#waiting_for_leader, null) };
    };
    groups.add(
      id,
      {
        id = id;
        name = if (name == "") { defaultName } else { name };
        description = if (description == "") { defaultDescription } else { description };
        topic = capText(trimText(input.topic), 500);
        status = status;
        turnMode = input.turnMode;
        maxTurns = if (input.maxTurns == 0) { 20 } else { input.maxTurns };
        turnSeq = 1;
        currentTurnBotId = current;
        turnQueue = [];
        mockDelaySec = switch (input.mockDelaySec) {
          case (?s) { s };
          case null { 12 };
        };
        turnStartedAt = now;
        createdAt = now;
        updatedAt = now;
      },
    );
    #ok(id);
  };

  public func listGroups(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    now : Common.Timestamp,
  ) : [Groups.GroupSummary] {
    groups.values().toArray().reverse().map(
      func(g) {
        let ms = membersOf(members, g.id);
        {
          id = g.id;
          name = g.name;
          description = g.description;
          topic = g.topic;
          status = g.status;
          turnMode = g.turnMode;
          turnSeq = g.turnSeq;
          maxTurns = g.maxTurns;
          memberCount = ms.size();
          onlineCount = ms.filter(func m = isOnline(m, now)).size();
          createdAt = g.createdAt;
          updatedAt = g.updatedAt;
        }
      }
    );
  };

  public func getGroupDetail(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    id : Common.GroupId,
    now : Common.Timestamp,
  ) : ?Groups.GroupDetail {
    let ?g = groups.get(id) else return null;
    ?{
      group = g;
      members = membersOf(members, id).map(func m = memberView(m, now));
      messages = messages.values().filter(func m = m.groupId == id).toArray();
    };
  };

  /// `text` is already sanitized by the caller. Saves a leader message and, in leader_round mode,
  /// fills turnQueue (mentions, else online bots, else all) and starts the first turn.
  public func sendLeaderMessage(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    counters : Groups.Counters,
    groupId : Common.GroupId,
    text : Text,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    switch (g.status) {
      case (#paused) { return #err("Group is paused: resume it before sending messages") };
      case (#completed) { return #err("Group has completed") };
      case (#stopped) { return #err("Group is stopped") };
      case (_) {};
    };
    if (text == "") { return #err("Message is empty") };
    messages.add({
      id = counters.nextMessageId;
      groupId = groupId;
      turnSeq = null;
      senderBotId = "leader";
      senderName = "القائد";
      senderColor = "#1D6BFF";
      role = #leader;
      content = text;
      createdAt = now;
    });
    counters.nextMessageId += 1;
    switch (g.turnMode) {
      case (#auto_round_robin) {
        groups.add(groupId, { g with updatedAt = now });
      };
      case (#leader_round) {
        let ms = membersOf(members, groupId);
        let lowered = text.toLower();
        let mentioned = ms.filter(func m = m.name != "" and lowered.contains(#text("@" # m.name.toLower())));
        let online = ms.filter(func m = isOnline(m, now));
        let chosen = if (mentioned.size() > 0) { mentioned } else if (online.size() > 0) { online } else { ms };
        let (first, rest) = popQueue(chosen.map(func m = m.botId), ms);
        groups.add(
          groupId,
          {
            g with
            status = switch (first) {
              case (?_) { #running };
              case null { #waiting_for_leader };
            };
            currentTurnBotId = first;
            turnQueue = rest;
            turnStartedAt = now;
            updatedAt = now;
          },
        );
      };
    };
    #ok(());
  };

  /// Skips the current speaker and advances to the next bot in queue/rotation. turnSeq is unchanged
  /// and no message is stored.
  public func skipCurrentTurn(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    groupId : Common.GroupId,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    if (g.status != #running) { return #err("Group is not running") };
    let ms = membersOf(members, groupId);
    switch (g.turnMode) {
      case (#auto_round_robin) {
        groups.add(
          groupId,
          {
            g with
            currentTurnBotId = nextAutoSpeaker(ms, g.currentTurnBotId, now);
            turnQueue = [];
            turnStartedAt = now;
            updatedAt = now;
          },
        );
      };
      case (#leader_round) {
        let (next, rest) = popQueue(g.turnQueue, ms);
        groups.add(
          groupId,
          {
            g with
            status = switch (next) {
              case (?_) { #running };
              case null { #waiting_for_leader };
            };
            currentTurnBotId = next;
            turnQueue = rest;
            turnStartedAt = now;
            updatedAt = now;
          },
        );
      };
    };
    #ok(());
  };

  public func pauseGroup(
    groups : Map.Map<Nat, Groups.Group>,
    groupId : Common.GroupId,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    switch (g.status) {
      case (#running) { groups.add(groupId, { g with status = #paused; updatedAt = now }) };
      case (#waiting_for_leader) { groups.add(groupId, { g with status = #paused; updatedAt = now }) };
      case (#paused) {};
      case (#completed) { return #err("Group has completed") };
      case (#stopped) { return #err("Group is stopped") };
    };
    #ok(());
  };

  public func resumeGroup(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    groupId : Common.GroupId,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    switch (g.status) {
      case (#paused) {};
      case (#completed) { return #err("Group has completed") };
      case (#stopped) { return #err("Group is stopped") };
      case (_) { return #ok(()) };
    };
    let ms = membersOf(members, groupId);
    switch (g.turnMode) {
      case (#auto_round_robin) {
        let current = if (hasMember(ms, g.currentTurnBotId)) { g.currentTurnBotId } else {
          nextAutoSpeaker(ms, g.currentTurnBotId, now);
        };
        groups.add(groupId, { g with status = #running; currentTurnBotId = current; turnQueue = []; turnStartedAt = now; updatedAt = now });
      };
      case (#leader_round) {
        if (hasMember(ms, g.currentTurnBotId)) {
          groups.add(groupId, { g with status = #running; turnStartedAt = now; updatedAt = now });
        } else {
          let (next, rest) = popQueue(g.turnQueue, ms);
          groups.add(
            groupId,
            {
              g with
              status = switch (next) {
                case (?_) { #running };
                case null { #waiting_for_leader };
              };
              currentTurnBotId = next;
              turnQueue = rest;
              turnStartedAt = now;
              updatedAt = now;
            },
          );
        };
      };
    };
    #ok(());
  };

  public func stopGroup(
    groups : Map.Map<Nat, Groups.Group>,
    groupId : Common.GroupId,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    switch (g.status) {
      case (#completed) {};
      case (#stopped) {};
      case (_) {
        groups.add(groupId, { g with status = #stopped; currentTurnBotId = null; turnQueue = []; updatedAt = now });
      };
    };
    #ok(());
  };

  public func setTurnMode(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    groupId : Common.GroupId,
    mode : Groups.TurnMode,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    if (g.turnMode == mode) { return #ok(()) };
    let ms = membersOf(members, groupId);
    switch (mode) {
      case (#leader_round) {
        // The current speaker (if any) finishes, then the group waits for the leader.
        let status : Groups.GroupStatus = switch (g.status) {
          case (#running) { if (hasMember(ms, g.currentTurnBotId)) { #running } else { #waiting_for_leader } };
          case (other) { other };
        };
        groups.add(groupId, { g with turnMode = mode; status = status; turnQueue = []; updatedAt = now });
      };
      case (#auto_round_robin) {
        switch (g.status) {
          case (#running) {
            let current = if (hasMember(ms, g.currentTurnBotId)) { g.currentTurnBotId } else {
              nextAutoSpeaker(ms, null, now);
            };
            groups.add(groupId, { g with turnMode = mode; currentTurnBotId = current; turnQueue = []; updatedAt = now });
          };
          case (#waiting_for_leader) {
            groups.add(
              groupId,
              {
                g with
                turnMode = mode;
                status = #running;
                currentTurnBotId = nextAutoSpeaker(ms, null, now);
                turnQueue = [];
                turnStartedAt = now;
                updatedAt = now;
              },
            );
          };
          case (_) {
            groups.add(groupId, { g with turnMode = mode; turnQueue = []; updatedAt = now });
          };
        };
      };
    };
    #ok(());
  };

  public func setMaxTurns(
    groups : Map.Map<Nat, Groups.Group>,
    groupId : Common.GroupId,
    maxTurns : Nat,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    if (maxTurns < 1) { return #err("maxTurns must be at least 1") };
    if (g.turnSeq > maxTurns) {
      groups.add(groupId, { g with maxTurns = maxTurns; status = #completed; currentTurnBotId = null; turnQueue = []; updatedAt = now });
    } else {
      groups.add(groupId, { g with maxTurns = maxTurns; updatedAt = now });
    };
    #ok(());
  };

  /// Enforces max 10 members. `token` is the new member's permanent agent token.
  public func addMember(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    groupId : Common.GroupId,
    input : Groups.MemberInput,
    token : Text,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    let ms = membersOf(members, groupId);
    if (ms.size() >= 10) { return #err("A group can have at most 10 members") };
    if (members.containsKey(token)) { return #err("Token collision, please retry") };
    // botIds are never reused: look at existing members and at past bot messages of this group.
    var highest = 0;
    for (m in ms.values()) {
      let num = botNum(m.botId);
      if (num > highest) { highest := num };
    };
    for (msg in messages.values()) {
      if (msg.groupId == groupId) {
        let num = botNum(msg.senderBotId);
        if (num > highest) { highest := num };
      };
    };
    let member = buildMember(groupId, highest + 1, input, token);
    members.add(token, member);
    groups.add(groupId, { g with updatedAt = now });
    #ok(());
  };

  /// Enforces min 2 members; repairs current turn and queue if the removed bot held them.
  public func removeMember(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    groupId : Common.GroupId,
    botId : Common.BotId,
    now : Common.Timestamp,
  ) : Result.Result<(), Text> {
    let ?g = groups.get(groupId) else return #err("Group not found");
    let ms = membersOf(members, groupId);
    if (ms.size() <= 2) { return #err("A group needs at least 2 members") };
    let ?target = ms.find(func m = m.botId == botId) else return #err("Member not found");
    members.remove(target.agentToken);
    let remaining = ms.filter(func m = m.botId != botId);
    let queue = g.turnQueue.filter(func b = b != botId);
    let heldTurn = switch (g.currentTurnBotId) {
      case (?c) { c == botId };
      case null { false };
    };
    if (not heldTurn) {
      groups.add(groupId, { g with turnQueue = queue; updatedAt = now });
      return #ok(());
    };
    switch (g.turnMode) {
      case (#auto_round_robin) {
        groups.add(
          groupId,
          {
            g with
            currentTurnBotId = nextAutoSpeaker(remaining, ?botId, now);
            turnQueue = [];
            turnStartedAt = now;
            updatedAt = now;
          },
        );
      };
      case (#leader_round) {
        let (next, rest) = popQueue(queue, remaining);
        let status : Groups.GroupStatus = switch (g.status) {
          case (#running) {
            switch (next) {
              case (?_) { #running };
              case null { #waiting_for_leader };
            };
          };
          case (other) { other };
        };
        groups.add(
          groupId,
          {
            g with
            status = status;
            currentTurnBotId = next;
            turnQueue = rest;
            turnStartedAt = now;
            updatedAt = now;
          },
        );
      };
    };
    #ok(());
  };
};
