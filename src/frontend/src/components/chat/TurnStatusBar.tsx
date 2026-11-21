import { type GroupDetail, GroupStatus } from "@/lib/api";
import type { GroupControls } from "@/components/chat/useGroupControls";
import { useSkipTurn } from "@/hooks/useAdminQueries";
import { accentClass, clampTurn, formatNumberAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import {
  CheckCircle2,
  Hourglass,
  PauseCircle,
  Power,
  SkipForward,
} from "lucide-react";
import type { ReactNode } from "react";

function TypingDots() {
  return (
    <span aria-hidden="true" className="inline-flex items-center gap-0.5">
      {[0, 150, 300].map((delay) => (
        <span
          key={delay}
          style={{ animationDelay: `${delay}ms` }}
          className="size-1 animate-bounce rounded-full bg-current motion-reduce:animate-none"
        />
      ))}
    </span>
  );
}

function Row({
  icon,
  children,
  action,
  ocid,
}: {
  icon: ReactNode;
  children: ReactNode;
  action?: ReactNode;
  ocid: string;
}) {
  return (
    <div
      data-ocid={ocid}
      aria-live="polite"
      className="mx-auto flex min-h-12 w-full max-w-3xl items-center gap-2.5 px-4 py-2 text-sm"
    >
      {icon}
      <div className="min-w-0 flex-1 truncate">{children}</div>
      {action}
    </div>
  );
}

/** Live status strip above the composer: whose turn it is, or the paused/ended state. */
export default function TurnStatusBar({
  detail,
  controls,
}: {
  detail: GroupDetail;
  controls: GroupControls;
}) {
  const skip = useSkipTurn();
  const { group, members } = detail;
  const current = members.find((m) => m.botId === group.currentTurnBotId);

  switch (group.status) {
    case GroupStatus.running:
      return (
        <div
          className="admin-statusbar shrink-0"
          data-ocid="groupchat.status_bar"
        >
          <Row
            ocid="groupchat.turn_status"
            icon={
              <span className="size-2 shrink-0 animate-pulse rounded-full bg-success motion-reduce:animate-none" />
            }
            action={
              current ? (
                <button
                  type="button"
                  data-ocid="groupchat.skip_turn_button"
                  onClick={() => skip.mutate(group.id)}
                  disabled={skip.isPending}
                  className="inline-flex min-h-9 shrink-0 items-center gap-1.5 rounded-full px-3 text-xs font-medium text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50"
                >
                  <SkipForward
                    aria-hidden="true"
                    className="size-3.5 rtl:-scale-x-100"
                  />
                  تخطي الدور
                </button>
              ) : null
            }
          >
            {current ? (
              <span
                className={cn(
                  accentClass(current.accentColor, current.name),
                  "inline-flex items-center gap-1.5",
                )}
              >
                <span className="font-bold text-bot">{current.name}</span>
                <span className="text-muted-foreground">يكتب الآن...</span>
                <span className="text-bot">
                  <TypingDots />
                </span>
              </span>
            ) : (
              <span className="text-muted-foreground">
                جارٍ اختيار الدور التالي...
              </span>
            )}
          </Row>
          {skip.isError ? (
            <p
              role="alert"
              data-ocid="groupchat.skip_error_state"
              className="px-4 pb-2 text-center text-xs text-destructive"
            >
              تعذر تخطي الدور. حاول مرة أخرى.
            </p>
          ) : null}
        </div>
      );
    case GroupStatus.waiting_for_leader:
      return (
        <div
          className="admin-statusbar shrink-0"
          data-ocid="groupchat.status_bar"
        >
          <Row
            ocid="groupchat.turn_status"
            icon={
              <Hourglass
                aria-hidden="true"
                className="size-4 shrink-0 text-warning"
              />
            }
          >
            <span className="text-muted-foreground">
              بانتظار توجيهك للمجموعة...
            </span>
          </Row>
        </div>
      );
    case GroupStatus.paused:
      return (
        <div
          className="admin-statusbar shrink-0"
          data-ocid="groupchat.status_bar"
        >
          <Row
            ocid="groupchat.turn_status"
            icon={
              <PauseCircle
                aria-hidden="true"
                className="size-4 shrink-0 text-warning"
              />
            }
            action={
              <button
                type="button"
                data-ocid="groupchat.status_resume_button"
                onClick={controls.resume}
                disabled={controls.busy}
                className="inline-flex min-h-9 shrink-0 items-center rounded-full bg-primary px-4 text-xs font-semibold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50"
              >
                استئناف
              </button>
            }
          >
            <span className="text-muted-foreground">المجموعة متوقفة مؤقتاً</span>
          </Row>
        </div>
      );
    case GroupStatus.completed:
      return (
        <div
          className="admin-statusbar shrink-0"
          data-ocid="groupchat.status_bar"
        >
          <Row
            ocid="groupchat.turn_status"
            icon={
              <CheckCircle2
                aria-hidden="true"
                className="size-4 shrink-0 text-success"
              />
            }
          >
            <span className="text-muted-foreground">
              اكتملت المحادثة (
              {formatNumberAr(clampTurn(group.turnSeq, group.maxTurns))}/
              {formatNumberAr(group.maxTurns)} دور)
            </span>
          </Row>
        </div>
      );
    case GroupStatus.stopped:
      return (
        <div
          className="admin-statusbar shrink-0"
          data-ocid="groupchat.status_bar"
        >
          <Row
            ocid="groupchat.turn_status"
            icon={
              <Power
                aria-hidden="true"
                className="size-4 shrink-0 text-destructive"
              />
            }
          >
            <span className="text-muted-foreground">
              تم إيقاف المجموعة نهائياً
            </span>
          </Row>
        </div>
      );
  }
}
