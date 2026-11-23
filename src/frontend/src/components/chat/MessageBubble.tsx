import BotAvatar from "@/components/chat/BotAvatar";
import { type Message, MessageRole } from "@/lib/api";
import { accentClass, formatClockAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { memo } from "react";

/** One chat message: bot (start side, accent avatar) or leader (end side, blue). */
function MessageBubble({
  message,
  index,
}: {
  message: Message;
  index: number;
}) {
  const isLeader = message.role === MessageRole.leader;
  const time = (
    <time className="shrink-0 self-end pb-1 text-[11px] text-muted-foreground">
      {formatClockAr(message.createdAt)}
    </time>
  );

  if (isLeader) {
    return (
      <div
        data-ocid={`groupchat.message.${index}`}
        className="msg-row flex justify-end gap-2"
      >
        <div className="flex min-w-0 max-w-[85%] flex-col items-start gap-1 md:max-w-[65%]">
          <span className="px-1 text-xs font-bold text-primary">القائد</span>
          <div className="flex items-end gap-2">
            <div
              className="bubble-leader min-w-0 px-4 py-2.5 text-[15px] leading-relaxed"
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
        "msg-row flex items-start justify-start gap-2.5",
      )}
    >
      <BotAvatar
        name={message.senderName}
        accentColor={message.senderColor}
        size="md"
        className="mt-5"
      />
      <div className="flex min-w-0 max-w-[85%] flex-col gap-1 md:max-w-[65%]">
        <span className="px-1 text-xs font-bold text-bot">
          <bdi>{message.senderName}</bdi>
        </span>
        <div className="flex items-end gap-2">
          <div className="bubble-bot bubble-tail min-w-0 px-4 py-2.5 text-[15px] leading-relaxed">
            <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere]">
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
