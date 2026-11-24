import { Badge } from "@/components/ui/badge";
import { type GroupSummary, TurnMode } from "@/lib/api";
import {
  type StatusTone,
  clampTurn,
  formatListTimeAr,
  formatNumberAr,
  groupStatusLabelAr,
  groupStatusTone,
  turnModeLabelAr,
} from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, AtSign, Repeat, Users } from "lucide-react";

const TONE: Record<
  StatusTone,
  { tone: "success" | "warning" | "primary" | "muted"; pulse: boolean }
> = {
  live: { tone: "success", pulse: true },
  waiting: { tone: "warning", pulse: false },
  paused: { tone: "primary", pulse: false },
  ended: { tone: "muted", pulse: false },
};

const BAR: Record<StatusTone, string> = {
  live: "bg-success",
  waiting: "bg-warning",
  paused: "bg-primary",
  ended: "bg-muted-foreground/60",
};

export default function GroupCard({
  group,
  position,
}: {
  group: GroupSummary;
  position: number;
}) {
  const statusTone = groupStatusTone(group.status);
  const meta = TONE[statusTone];
  const ocid = `groups.item.${position}`;
  const turn = clampTurn(group.turnSeq, group.maxTurns);
  const total = Number(group.maxTurns);
  const pct = total > 0 ? Math.min(100, Math.round((turn / total) * 100)) : 0;
  const ModeIcon = group.turnMode === TurnMode.leader_round ? AtSign : Repeat;

  return (
    <li
      data-ocid={ocid}
      className={cn(
        "admin-card group relative flex flex-col gap-3 p-4",
        "transition-[border-color,box-shadow,transform] duration-base ease-out",
        "hover:-translate-y-0.5 hover:border-border-strong hover:shadow-md",
        "focus-within:border-ring/60",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-display text-base font-bold leading-snug text-foreground">
            <Link
              to="/admin/g/$id"
              params={{ id: group.id.toString() }}
              data-ocid={`${ocid}.open_link`}
              className="rounded-sm outline-none after:absolute after:inset-0 after:rounded-lg after:content-[''] focus-visible:after:ring-2 focus-visible:after:ring-ring/60"
            >
              {group.name}
            </Link>
          </h3>
          <p className="mt-0.5 truncate text-xs text-muted-foreground">
            {group.topic || group.description || "بدون موضوع"}
          </p>
        </div>
        <Badge
          data-ocid={`${ocid}.status_badge`}
          tone={meta.tone}
          dot
          pulse={meta.pulse}
        >
          {groupStatusLabelAr(group.status)}
        </Badge>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-baseline justify-between gap-2 text-2xs">
          <span className="font-medium text-muted-foreground">
            تقدم الأدوار
          </span>
          <span className="tabular font-mono text-foreground-secondary">
            {formatNumberAr(turn)}
            <span className="text-muted-foreground">
              {" / "}
              {formatNumberAr(total)}
            </span>
          </span>
        </div>
        <div
          role="progressbar"
          tabIndex={0}
          aria-valuemin={0}
          aria-valuemax={total}
          aria-valuenow={turn}
          aria-label={`تقدم الأدوار ${turnProgressText(turn, total)}`}
          className="h-1.5 overflow-hidden rounded-full bg-elevated focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <div
            className={cn(
              "h-full rounded-full transition-[width] duration-slow ease-out",
              BAR[statusTone],
            )}
            style={{ width: `${pct}%` }}
          />
        </div>
      </div>

      <dl className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-2xs text-muted-foreground">
        <div className="flex items-center gap-1.5">
          <Users aria-hidden="true" className="size-3.5" />
          <dt className="sr-only">الأعضاء</dt>
          <dd>
            {formatNumberAr(group.memberCount)} أعضاء ·{" "}
            <span className="text-success">
              {formatNumberAr(group.onlineCount)} متصل
            </span>
          </dd>
        </div>
        <span aria-hidden="true" className="text-border-strong">
          |
        </span>
        <div className="flex items-center gap-1.5">
          <ModeIcon aria-hidden="true" className="size-3.5" />
          <dt className="sr-only">نمط الأدوار</dt>
          <dd>{turnModeLabelAr(group.turnMode)}</dd>
        </div>
      </dl>

      <div className="mt-auto flex items-center justify-between gap-3 border-t border-border-subtle pt-3">
        <span className="text-2xs text-muted-foreground">
          آخر نشاط {formatListTimeAr(group.updatedAt)}
        </span>
        <span
          data-ocid={`${ocid}.open_button`}
          aria-hidden="true"
          className="inline-flex h-9 items-center gap-1.5 rounded-full bg-secondary px-3.5 text-xs font-semibold text-foreground transition-colors group-hover:bg-primary group-hover:text-primary-foreground"
        >
          فتح المحادثة
          <ArrowLeft aria-hidden="true" className="size-3.5" />
        </span>
      </div>
    </li>
  );
}

function turnProgressText(turn: number, total: number): string {
  return `الدور ${formatNumberAr(turn)} من ${formatNumberAr(total)}`;
}
