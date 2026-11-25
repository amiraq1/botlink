import BotAvatar from "@/components/chat/BotAvatar";
import { type Message, MessageRole } from "@/lib/api";
import { accentClass, formatClockAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { memo } from "react";

/** One chat message: bot (start side, accent avatar) or leader (end side, violet). */
function MessageBubble({
  message,
  index,
}: {
  message: Message;
  index: number;
}) {
  const isLeader = message.role === MessageRole.leader;
  const time = (
    <time
      dateTime={new Date(message.createdAt).toISOString()}
      className="shrink-0 self-end pb-1 font-mono text-2xs text-muted-foreground tabular"
    >
      {formatClockAr(message.createdAt)}
    </time>
  );

  if (isLeader) {
    return (
      <div
        data-ocid={`groupchat.message.${index}`}
        className="msg-row flex animate-message-in justify-end gap-2"
      >
        <div className="flex min-w-0 max-w-[85%] flex-col items-start gap-1 md:max-w-[65%]">
          <span className="px-1 text-2xs font-bold text-primary">القائد</span>
          <div className="flex items-end gap-2">
            <div
              className="bubble-leader min-w-0 px-4 py-2.5 text-base leading-relaxed"
              style={{ borderStartEndRadius: 6 }}
            >
              <p
                dir="auto"
                className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
              >
                {message.content}
              </p>
            </div>
            {time}
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      data-ocid={`groupchat.message.${index}`}
      className={cn(
        accentClass(message.senderColor, message.senderBotId),
        "msg-row flex animate-message-in items-start justify-start gap-2.5",
      )}
    >
      <BotAvatar
        name={message.senderName}
        accentColor={message.senderColor}
        size="md"
        className="mt-5"
      />
      <div className="flex min-w-0 max-w-[85%] flex-col gap-1 md:max-w-[65%]">
        <span className="px-1 text-2xs font-bold text-bot">
          <bdi>{message.senderName}</bdi>
        </span>
        <div className="flex items-end gap-2">
          <div className="bubble-bot bubble-tail min-w-0 px-4 py-2.5 text-base leading-relaxed">
            <p
              dir="auto"
              className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]"
            >
              {message.content}
            </p>
          </div>
          {time}
        </div>
      </div>
    </div>
  );
}

export default memo(MessageBubble);
