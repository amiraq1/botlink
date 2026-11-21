import List "mo:core/List";
import Map "mo:core/Map";
import Text "mo:core/Text";
import Time "mo:core/Time";
import Timer "mo:core/Timer";
import Common "../types/common";
import Groups "../types/groups";
import Rooms "../types/rooms";
import GroupsLib "groups";

/// Turn engine and public agent-room logic.
module {
  /// True for control characters and invisible / bidi-control characters that must never reach a room.
  func isStripped(code : Nat32) : Bool {
    code < 32
    or (code >= 127 and code <= 159)
    or code == 173 // soft hyphen
    or code == 847 // combining grapheme joiner
    or code == 1564 // arabic letter mark
    or code == 4447
    or code == 4448
    or code == 6158
    or (code >= 8203 and code <= 8207) // zero-width space/joiners, LRM, RLM
    or (code >= 8232 and code <= 8238) // line/paragraph separators, bidi embeddings
    or (code >= 8288 and code <= 8303) // word joiner, invisible operators, bidi isolates
    or code == 12644
    or code == 65279 // BOM / zero-width no-break space
    or code == 65440
    or (code >= 65529 and code <= 65531)
    or (code >= 917504 and code <= 917631); // tag characters
  };

  func lowerCode(c : Char) : Nat32 {
    let code = c.toNat32();
    if (code >= 65 and code <= 90) { code + 32 } else { code };
  };

  func matchesAt(cs : [Char], i : Nat, pattern : [Nat32]) : Bool {
    if (i + pattern.size() > cs.size()) { return false };
    var j = 0;
    while (j < pattern.size()) {
      if (lowerCode(cs[i + j]) != pattern[j]) { return false };
      j += 1;
    };
    true;
  };

  /// Strips control/invisible characters, replaces http://, https:// and www. links with a single
  /// "[LINK_REMOVED]", trims and caps at 3000 characters.
  public func sanitize(raw : Text) : Text {
    // Pass 1: drop control and invisible characters (keep newlines, tabs become spaces).
    let cleaned = List.empty<Char>();
    for (c in raw.toIter().take(30000)) {
      let code = c.toNat32();
      if (code == 10) {
        cleaned.add(c);
      } else if (code == 9) {
        cleaned.add(' ');
      } else if (not isStripped(code)) {
        cleaned.add(c);
      };
    };
    // Pass 2: replace each link run (up to the next whitespace) with one marker.
    let patterns = ["http://", "https://", "www."].map(func p = p.toArray().map(func c = c.toNat32()));
    let cs = cleaned.toArray();
    let marker = "[LINK_REMOVED]".toArray();
    let result = List.empty<Char>();
    var i = 0;
    while (i < cs.size()) {
      let first = lowerCode(cs[i]);
      let isLink = (first == 104 or first == 119) and patterns.any(func p = matchesAt(cs, i, p));
      if (isLink) {
        // A link wrapped as "[http://...]" must not leave a doubled bracket behind.
        switch (result.last()) {
          case (?prev) {
            if (prev == '[') { ignore result.removeLast() };
          };
          case null {};
        };
        for (m in marker.values()) { result.add(m) };
        while (i < cs.size() and not cs[i].isWhitespace()) { i += 1 };
      } else {
        result.add(cs[i]);
        i += 1;
      };
    };
    let trimmed = GroupsLib.trimText(Text.fromIter(result.values()));
    GroupsLib.trimText(GroupsLib.capText(trimmed, 3000));
  };

  /// Sets lastSeenAt = now on the member owning `token` (no-op for an unknown token).
  public func touchMember(members : Map.Map<Text, Groups.BotMember>, token : Text, now : Common.Timestamp) {
    switch (members.get(token)) {
      case (?m) { members.add(token, { m with lastSeenAt = now }) };
      case null {};
    };
  };

  func quoteOf(content : Text) : Text {
    let flat = content.replace(#char '\n', " ");
    if (flat.size() > 80) { GroupsLib.capText(flat, 80) # "…" } else { flat };
  };

  func mockText(speaker : Groups.BotMember, last : ?Groups.Message, turn : Nat) : Text {
    let tag = if (speaker.roleTitle == "") { speaker.name } else { speaker.name # " (" # speaker.roleTitle # ")" };
    switch (last) {
      case null {
        "[رد تلقائي] " # tag # ": مرحبًا بالجميع، أنا جاهز للبدء. من يقترح الخطوة الأولى؟\n" #
        "[Auto-reply] " # tag # ": Hello team, I'm ready to start. Who proposes the first step?";
      };
      case (?m) {
        let q = quoteOf(m.content);
        let who = m.senderName;
        let v = turn % 3;
        if (v == 0) {
          "[رد تلقائي] " # tag # ": بخصوص ما قاله " # who # " \"" # q # "\" — أتفق مع هذه النقطة وأقترح أن نحدد الخطوة التالية بوضوح.\n" #
          "[Auto-reply] " # tag # ": On " # who # "'s point \"" # q # "\" — I agree, and I suggest we define the next concrete step.";
        } else if (v == 1) {
          "[رد تلقائي] " # tag # ": تعليقًا على \"" # q # "\" — فكرة جيدة، لكن ما المخاطر التي يجب أن ننتبه لها؟\n" #
          "[Auto-reply] " # tag # ": About \"" # q # "\" — good idea, but which risks should we watch for?";
        } else {
          "[رد تلقائي] " # tag # ": ردًا على " # who # " (\"" # q # "\") — سأتولى الجزء الخاص بي وأعود بالنتيجة.\n" #
          "[Auto-reply] " # tag # ": In reply to " # who # " (\"" # q # "\") — I'll take my part and report back.";
        };
      };
    };
  };

  /// Applies a due mock reply (if the current speaker is a mock past mockDelaySec) for one group.
  public func processDueMockReply(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    counters : Groups.Counters,
    groupId : Common.GroupId,
    now : Common.Timestamp,
  ) {
    let ?g = groups.get(groupId) else return;
    if (g.status != #running) { return };
    let ?current = g.currentTurnBotId else return;
    let ms = GroupsLib.membersOf(members, groupId);
    let ?speaker = ms.find(func m = m.botId == current) else return;
    if (not speaker.isMock) { return };
    if (now - g.turnStartedAt < g.mockDelaySec.toInt() * 1_000_000_000) { return };
    let last = messages.reverseValues().find(func m = m.groupId == groupId);
    messages.add({
      id = counters.nextMessageId;
      groupId = groupId;
      turnSeq = ?(g.turnSeq);
      senderBotId = speaker.botId;
      senderName = speaker.name;
      senderColor = speaker.accentColor;
      role = #bot;
      content = mockText(speaker, last, g.turnSeq);
      createdAt = now;
    });
    counters.nextMessageId += 1;
    groups.add(groupId, GroupsLib.advanceAfterReply(g, ms, now));
  };

  /// Arms a one-shot timer that applies the mock reply after mockDelaySec.
  public func scheduleMockReply<system>(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    counters : Groups.Counters,
    groupId : Common.GroupId,
  ) {
    let ?g = groups.get(groupId) else return;
    if (g.status != #running) { return };
    let ?current = g.currentTurnBotId else return;
    let mockSpeaker = members.values().any(func m = m.groupId == groupId and m.botId == current and m.isMock);
    if (not mockSpeaker) { return };
    ignore Timer.setTimer<system>(
      #seconds(g.mockDelaySec),
      func() : async () {
        processDueMockReply(groups, members, messages, counters, groupId, Time.now());
      },
    );
  };

  /// Applies every due mock reply across all groups (safety net run by the recurring timer).
  public func sweepMockReplies(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    counters : Groups.Counters,
    now : Common.Timestamp,
  ) {
    for (id in groups.keys().toArray().values()) {
      processDueMockReply(groups, members, messages, counters, id, now);
    };
  };

  /// Pure view for the member owning `token`; null for an unknown token.
  public func getRoomState(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    token : Text,
    now : Common.Timestamp,
  ) : ?Rooms.RoomState {
    let ?m = members.get(token) else return null;
    let ?g = groups.get(m.groupId) else return null;
    let ms = GroupsLib.membersOf(members, m.groupId);
    let yourTurn = g.status == #running and (switch (g.currentTurnBotId) {
      case (?c) { c == m.botId };
      case null { false };
    });
    ?{
      status = g.status;
      botId = m.botId;
      botName = m.name;
      roleTitle = m.roleTitle;
      groupName = g.name;
      onlineNames = ms.filter(func x = GroupsLib.isOnline(x, now)).map(func x = x.name);
      turnSeq = g.turnSeq;
      maxTurns = g.maxTurns;
      topic = g.topic;
      isYourTurn = yourTurn;
      messages = messages.reverseValues().filter(func x = x.groupId == m.groupId).take(15).toArray().reverse();
    };
  };

  /// Atomic turnSeq deduplication: never traps; stale/duplicate/wrong-turn/invalid → accepted=false.
  public func submitBotReply(
    groups : Map.Map<Nat, Groups.Group>,
    members : Map.Map<Text, Groups.BotMember>,
    messages : List.List<Groups.Message>,
    counters : Groups.Counters,
    token : Text,
    expectedTurnSeq : Nat,
    rawContent : Text,
    now : Common.Timestamp,
  ) : Rooms.ReplyResult {
    let rejected : Rooms.ReplyResult = { accepted = false };
    touchMember(members, token, now);
    let ?m = members.get(token) else return rejected;
    let ?g = groups.get(m.groupId) else return rejected;
    if (m.isMock) { return rejected };
    if (g.status != #running) { return rejected };
    let isSpeaker = switch (g.currentTurnBotId) {
      case (?c) { c == m.botId };
      case null { false };
    };
    if (not isSpeaker) { return rejected };
    if (g.turnSeq != expectedTurnSeq) { return rejected };
    let content = sanitize(rawContent);
    if (content == "") { return rejected };
    messages.add({
      id = counters.nextMessageId;
      groupId = m.groupId;
      turnSeq = ?(g.turnSeq);
      senderBotId = m.botId;
      senderName = m.name;
      senderColor = m.accentColor;
      role = #bot;
      content = content;
      createdAt = now;
    });
    counters.nextMessageId += 1;
    groups.add(m.groupId, GroupsLib.advanceAfterReply(g, GroupsLib.membersOf(members, m.groupId), now));
    { accepted = true };
  };
};
