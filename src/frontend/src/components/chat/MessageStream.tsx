import type { GroupDetail, Message } from "@/lib/api";
import GroupIntroCard from "@/components/chat/GroupIntroCard";
import MessageBubble from "@/components/chat/MessageBubble";
import { dayKey, formatDayDividerAr } from "@/lib/convert";
import { ArrowDown, MessageCircle } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";

const GAP_NS = 2n * 3_600n * 1_000_000_000n;
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
    const longGap =
      !!previous && message.createdAt - previous.createdAt > GAP_NS;
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

  // Runs after every render (polling updates included); only moves when pinned.
  useEffect(() => {
    if (pinned.current) scrollToEnd(false);
  });

  const items = buildItems(detail.messages);

  return (
    <div className="relative min-h-0 flex-1">
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        data-ocid="groupchat.stream"
        className="scrollbar-thin h-full overflow-y-auto overscroll-contain"
      >
        <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-3 py-4 md:px-6">
          <GroupIntroCard detail={detail} onOpenInfo={onOpenInfo} />
          {items.length === 0 ? (
            <div
              data-ocid="groupchat.empty_state"
              className="mx-auto mt-6 flex max-w-xs flex-col items-center gap-2 text-center"
            >
              <span className="flex size-12 items-center justify-center rounded-full bg-secondary text-muted-foreground">
                <MessageCircle aria-hidden="true" className="size-6" />
              </span>
              <p className="font-semibold">لا توجد رسائل بعد</p>
              <p className="text-sm text-muted-foreground">
                اكتب رسالة للفريق أدناه لبدء المحادثة، أو انتظر أول رد من
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
                  <span className="bubble-system px-3 py-1 text-xs">
                    {item.divider}
                  </span>
                </div>
              ),
            )
          )}
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
          className="absolute bottom-3 start-3 flex size-11 items-center justify-center rounded-full border border-border bg-card text-foreground shadow-lg transition-colors hover:bg-secondary focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ArrowDown aria-hidden="true" className="size-5" />
        </button>
      ) : null}
    </div>
  );
}
