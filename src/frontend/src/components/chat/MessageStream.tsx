import BotAvatar from "@/components/chat/BotAvatar";
import GroupIntroCard from "@/components/chat/GroupIntroCard";
import MessageBubble from "@/components/chat/MessageBubble";
import { type GroupDetail, GroupStatus, type Message } from "@/lib/api";
import { dayKey, formatDayDividerAr, isMemberOnline } from "@/lib/convert";
import { isLongGap } from "@/lib/messageGap";
import { ArrowDown, MessageCircle } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

const PIN_THRESHOLD_PX = 80;

interface StreamItem {
  key: string;
  divider?: string;
  message?: Message;
  index: number;
}

/** Interleaves date/time dividers when the day changes or a long gap passes. */
function buildItems(messages: Message[]): StreamItem[] {
  const items: StreamItem[] = [];
  let previous: Message | undefined;
  messages.forEach((message, i) => {
    const newDay =
      !previous || dayKey(previous.createdAt) !== dayKey(message.createdAt);
    const longGap = isLongGap(previous?.createdAt, message.createdAt);
    if (newDay || longGap) {
      items.push({
        key: `divider-${message.id}`,
        divider: formatDayDividerAr(message.createdAt),
        index: i,
      });
    }
    items.push({ key: `message-${message.id}`, message, index: i + 1 });
    previous = message;
  });
  return items;
}

function TypingBubble({
  name,
  accentColor,
  online,
}: {
  name: string;
  accentColor: string;
  online: boolean;
}) {
  return (
    <div className="msg-row flex animate-fade-in items-start gap-2.5">
      <BotAvatar
        name={name}
        accentColor={accentColor}
        size="md"
        state={online ? "live" : "idle"}
        className="mt-5"
      />
      <div className="flex flex-col gap-1">
        <span className="px-1 text-xs font-bold text-muted-foreground">
          <bdi>{name}</bdi>
        </span>
        <div className="bubble-bot bubble-tail flex items-center gap-1.5 px-4 py-3.5">
          <span aria-hidden="true" className="flex items-center gap-1">
            {[0, 150, 300].map((delay) => (
              <span
                key={delay}
                style={{ animationDelay: `${delay}ms` }}
                className="size-1.5 animate-typing-dot rounded-full bg-muted-foreground motion-reduce:animate-none"
              />
            ))}
          </span>
          <span className="sr-only">{name} يكتب الآن…</span>
        </div>
      </div>
    </div>
  );
}

/** Scrolling message list pinned to the newest message unless the admin scrolled up. */
export default function MessageStream({
  detail,
  onOpenInfo,
}: {
  detail: GroupDetail;
  onOpenInfo: () => void;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const [showJump, setShowJump] = useState(false);

  const scrollToEnd = useCallback((smooth: boolean) => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  }, []);

  const handleScroll = () => {
    const el = scrollRef.current;
    if (!el) return;
    const atEnd =
      el.scrollHeight - el.scrollTop - el.clientHeight < PIN_THRESHOLD_PX;
    pinned.current = atEnd;
    setShowJump(!atEnd);
  };

  // Scroll only when the list grows; only moves when pinned to the bottom.
  // Rows reserve an estimated height until they are painted, so the first
  // scroll can land short on a long log; re-pin once layout settles.
  useEffect(() => {
    if (detail.messages.length === 0 || !pinned.current) return;
    scrollToEnd(false);
    const frame = window.requestAnimationFrame(() => {
      if (pinned.current) scrollToEnd(false);
    });
    const settle = window.setTimeout(() => {
      if (pinned.current) scrollToEnd(false);
    }, 120);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(settle);
    };
  }, [detail.messages.length, scrollToEnd]);

  const items = useMemo(() => buildItems(detail.messages), [detail.messages]);

  const current =
    detail.group.status === GroupStatus.running
      ? detail.members.find((m) => m.botId === detail.group.currentTurnBotId)
      : undefined;

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        data-ocid="groupchat.stream"
        role="log"
        aria-live="polite"
        aria-relevant="additions"
        aria-label="سجل المحادثة"
        className="scrollbar-thin h-full overflow-y-auto overscroll-contain"
      >
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-3 py-4 md:px-6">
          <GroupIntroCard detail={detail} onOpenInfo={onOpenInfo} />
          {items.length === 0 ? (
            <div
              data-ocid="groupchat.empty_state"
              className="mx-auto mt-4 flex max-w-sm flex-col items-center gap-2 text-center"
            >
              <span className="flex size-12 items-center justify-center rounded-full border border-border bg-surface text-muted-foreground">
                <MessageCircle aria-hidden="true" className="size-5" />
              </span>
              <p className="font-display text-sm font-bold">
                لا توجد رسائل بعد
              </p>
              <p className="text-xs leading-relaxed text-muted-foreground">
                اكتب رسالة للفريق من الأسفل لبدء المحادثة، أو انتظر أول رد من
                البوتات.
              </p>
            </div>
          ) : (
            items.map((item) =>
              item.message ? (
                <MessageBubble
                  key={item.key}
                  message={item.message}
                  index={item.index}
                />
              ) : (
                <div key={item.key} className="flex justify-center py-1">
                  <span className="bubble-system px-3 py-1 text-2xs">
                    {item.divider}
                  </span>
                </div>
              ),
            )
          )}
          {current ? (
            <TypingBubble
              name={current.name}
              accentColor={current.accentColor}
              online={isMemberOnline(current)}
            />
          ) : null}
        </div>
      </div>
      {showJump ? (
        <button
          type="button"
          data-ocid="groupchat.scroll_latest_button"
          aria-label="الانتقال إلى أحدث رسالة"
          onClick={() => {
            pinned.current = true;
            setShowJump(false);
            scrollToEnd(true);
          }}
          className="absolute bottom-3 start-3 flex size-10 items-center justify-center rounded-full border border-border bg-elevated text-foreground shadow-lg transition-colors duration-fast hover:bg-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <ArrowDown aria-hidden="true" className="size-4" />
        </button>
      ) : null}
    </div>
  );
}
