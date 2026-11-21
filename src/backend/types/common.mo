module {
  /// Nanoseconds since epoch, as returned by Time.now().
  public type Timestamp = Int;
  public type GroupId = Nat;
  public type MessageId = Nat;
  /// "bot_1", "bot_2", ... for members; "leader" as sender of leader messages.
  public type BotId = Text;
};
