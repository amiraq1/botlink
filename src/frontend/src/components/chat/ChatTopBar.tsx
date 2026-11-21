import type { GroupDetail } from "@/backend";
import BotAvatar from "@/components/chat/BotAvatar";
import type { GroupControls } from "@/components/chat/useGroupControls";
import { formatNumberAr, isMemberOnline } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Info, Pause, Play, Power } from "lucide-react";

interface ChatTopBarProps {
  detail: GroupDetail;
  controls: GroupControls;
  onOpenInfo: () => void;
}

const PILL =
  "inline-flex min-h-11 items-center justify-center gap-1.5 rounded-full border px-3 text-sm font-semibold transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50 md:min-h-9";

/** Sticky header: back, avatar cluster, name, live presence and group actions. */
export default function ChatTopBar({
  detail,
  controls,
  onOpenInfo,
}: ChatTopBarProps) {
  const { group, members } = detail;
  const onlineCount = members.filter(isMemberOnline).length;

  return (
    <header
      data-ocid="groupchat.topbar"
      className="admin-topbar z-20 flex shrink-0 items-center gap-2 px-2 py-2 md:gap-3 md:px-4"
    >
      <Link
        to="/admin"
        data-ocid="groupchat.back_button"
        aria-label="العودة إلى المجموعات"
        className="flex size-11 shrink-0 items-center justify-center rounded-full text-foreground transition-colors hover:bg-secondary focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ArrowRight aria-hidden="true" className="size-5" />
      </Link>

      <div className="hidden shrink-0 items-center -space-x-3 sm:flex rtl:space-x-reverse">
        {members.slice(0, 3).map((m) => (
          <BotAvatar
            key={m.botId}
            name={m.name}
            accentColor={m.accentColor}
            size="sm"
            state={isMemberOnline(m) ? "idle" : "offline"}
          />
        ))}
      </div>

      <div className="min-w-0 flex-1">
        <h1 className="truncate font-display text-base font-bold leading-tight md:text-lg">
          {group.name}
        </h1>
        <p
          data-ocid="groupchat.online_count"
          className="flex items-center gap-1.5 text-xs text-muted-foreground"
        >
          <span className="relative flex size-2 shrink-0">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-70 motion-reduce:animate-none" />
            <span className="relative inline-flex size-2 rounded-full bg-success" />
          </span>
          <span className="truncate">
            {formatNumberAr(onlineCount)} أعضاء متصلون الآن
          </span>
        </p>
      </div>

      <div className="flex shrink-0 items-center gap-1.5">
        <button
          type="button"
          data-ocid="groupchat.info_button"
          onClick={onOpenInfo}
          aria-label="عرض معلومات المجموعة"
          className={cn(PILL, "border-border bg-secondary hover:bg-accent")}
        >
          <Info aria-hidden="true" className="size-4" />
          <span className="hidden lg:inline">عرض معلومات المجموعة</span>
        </button>
        {controls.canResume ? (
          <button
            type="button"
            data-ocid="groupchat.resume_button"
            onClick={controls.resume}
            disabled={controls.busy}
            aria-label="استئناف"
            className={cn(
              PILL,
              "border-success/50 bg-success/15 text-success hover:bg-success/25",
            )}
          >
            <Play aria-hidden="true" className="size-4" />
            <span className="hidden md:inline">استئناف</span>
          </button>
        ) : (
          <button
            type="button"
            data-ocid="groupchat.pause_button"
            onClick={controls.pause}
            disabled={controls.busy || !controls.canPause}
            aria-label="إيقاف مؤقت"
            className={cn(PILL, "border-border bg-secondary hover:bg-accent")}
          >
            <Pause aria-hidden="true" className="size-4" />
            <span className="hidden md:inline">إيقاف مؤقت</span>
          </button>
        )}
        <button
          type="button"
          data-ocid="groupchat.stop_button"
          onClick={controls.killSwitch}
          disabled={controls.busy || controls.ended}
          aria-label="إيقاف"
          className={cn(
            PILL,
            "border-destructive/50 bg-destructive/15 text-destructive hover:bg-destructive/25",
          )}
        >
          <Power aria-hidden="true" className="size-4" />
          <span className="hidden md:inline">إيقاف</span>
        </button>
      </div>
    </header>
  );
}
