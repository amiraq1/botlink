import BotAvatar from "@/components/chat/BotAvatar";
import type { GroupControls } from "@/components/chat/useGroupControls";
import { Badge } from "@/components/ui/badge";
import { Tooltip } from "@/components/ui/tooltip";
import { type GroupDetail, GroupStatus } from "@/lib/api";
import {
  clampTurn,
  formatNumberAr,
  groupStatusLabelAr,
  groupStatusTone,
  isMemberOnline,
  turnModeLabelAr,
} from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Info,
  Pause,
  Play,
  Power,
  SlidersHorizontal,
} from "lucide-react";

interface ChatTopBarProps {
  detail: GroupDetail;
  controls: GroupControls;
  connected: boolean;
  onOpenInfo: () => void;
  onOpenControls: () => void;
}

const STATUS_TONE = {
  live: "success",
  waiting: "warning",
  paused: "primary",
  ended: "muted",
} as const;

const PILL =
  "inline-flex min-h-10 items-center justify-center gap-1.5 rounded-full border px-3 text-xs font-semibold transition-colors duration-fast focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-40";

/** Sticky header: back, roster cluster, name, live status and group actions. */
export default function ChatTopBar({
  detail,
  controls,
  connected,
  onOpenInfo,
  onOpenControls,
}: ChatTopBarProps) {
  const { group, members } = detail;
  const onlineCount = members.filter(isMemberOnline).length;
  const turn = clampTurn(group.turnSeq, group.maxTurns);
  const statusTone = STATUS_TONE[groupStatusTone(group.status)];
  const running = group.status === GroupStatus.running;

  return (
    <header
      data-ocid="groupchat.topbar"
      className="admin-topbar z-20 flex shrink-0 flex-col gap-2 px-2 py-2 md:px-4"
    >
      <div className="flex items-center gap-2 md:gap-3">
        <Tooltip label="العودة إلى المجموعات">
          <Link
            to="/admin"
            data-ocid="groupchat.back_button"
            aria-label="العودة إلى المجموعات"
            className="flex size-10 shrink-0 items-center justify-center rounded-full text-foreground transition-colors duration-fast hover:bg-secondary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <ArrowRight aria-hidden="true" className="size-5" />
          </Link>
        </Tooltip>

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
          <h1 className="truncate font-display text-base font-bold leading-tight text-foreground md:text-lg">
            {group.name}
          </h1>
          <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-2xs text-muted-foreground">
            <span className="inline-flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className={cn(
                  "size-1.5 rounded-full",
                  running
                    ? "animate-pulse bg-success"
                    : "bg-muted-foreground/60",
                )}
              />
              {groupStatusLabelAr(group.status)}
            </span>
            <span aria-hidden="true" className="text-border-strong">
              ·
            </span>
            <span className="font-mono tabular">
              الدور {formatNumberAr(turn)}/{formatNumberAr(group.maxTurns)}
            </span>
            <span aria-hidden="true" className="text-border-strong">
              ·
            </span>
            <span className="hidden sm:inline">
              {turnModeLabelAr(group.turnMode)}
            </span>
            <span className="hidden sm:inline" aria-hidden="true">
              ·
            </span>
            <span className="inline-flex items-center gap-1.5">
              <span className="size-1.5 rounded-full bg-success" />
              {formatNumberAr(onlineCount)} متصل
            </span>
          </p>
        </div>

        <div className="flex shrink-0 items-center gap-1.5">
          <span data-ocid="groupchat.sse_status" className="hidden md:block">
            <Badge
              tone={connected ? "success" : "warning"}
              dot
              pulse={connected}
              size="sm"
            >
              {connected ? "متصل مباشر" : "إعادة الاتصال…"}
            </Badge>
          </span>
          <span className="hidden md:block">
            <Badge tone={statusTone} size="sm">
              {turnModeLabelAr(group.turnMode)}
            </Badge>
          </span>

          <Tooltip label="لوحة التحكم">
            <button
              type="button"
              data-ocid="groupchat.controls_button"
              onClick={onOpenControls}
              aria-label="فتح لوحة التحكم"
              className={cn(
                PILL,
                "border-border bg-secondary hover:bg-elevated xl:hidden",
              )}
            >
              <SlidersHorizontal aria-hidden="true" className="size-4" />
            </button>
          </Tooltip>

          <Tooltip label="معلومات المجموعة">
            <button
              type="button"
              data-ocid="groupchat.info_button"
              onClick={onOpenInfo}
              aria-label="عرض معلومات المجموعة"
              className={cn(
                PILL,
                "border-border bg-secondary hover:bg-elevated",
              )}
            >
              <Info aria-hidden="true" className="size-4" />
            </button>
          </Tooltip>

          {controls.canResume ? (
            <button
              type="button"
              data-ocid="groupchat.resume_button"
              onClick={controls.resume}
              disabled={controls.busy}
              aria-label="استئناف"
              className={cn(
                PILL,
                "border-success/40 bg-success/15 text-success hover:bg-success/20",
              )}
            >
              <Play aria-hidden="true" className="size-4" />
              <span className="hidden lg:inline">استئناف</span>
            </button>
          ) : (
            <button
              type="button"
              data-ocid="groupchat.pause_button"
              onClick={controls.pause}
              disabled={controls.busy || !controls.canPause}
              aria-label="إيقاف مؤقت"
              className={cn(
                PILL,
                "border-border bg-secondary hover:bg-elevated",
              )}
            >
              <Pause aria-hidden="true" className="size-4" />
              <span className="hidden lg:inline">إيقاف مؤقت</span>
            </button>
          )}

          <button
            type="button"
            data-ocid="groupchat.stop_button"
            onClick={controls.killSwitch}
            disabled={controls.busy || controls.ended}
            aria-label="إيقاف فوري"
            className={cn(
              PILL,
              "border-destructive/45 bg-destructive/15 text-destructive hover:bg-destructive/20",
            )}
          >
            <Power aria-hidden="true" className="size-4" />
            <span className="hidden lg:inline">إيقاف</span>
          </button>
        </div>
      </div>
    </header>
  );
}
