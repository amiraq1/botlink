import type { GroupSummary } from "@/backend";
import {
  type StatusTone,
  clampTurn,
  formatListTimeAr,
  formatNumberAr,
  groupStatusLabelAr,
  groupStatusTone,
  onlineCountLabelAr,
  turnModeLabelAr,
  turnProgressAr,
} from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, Repeat, Users } from "lucide-react";

const TONE_CLASS: Record<StatusTone, string> = {
  live: "bg-success/15 text-success",
  waiting: "bg-warning/15 text-warning",
  paused: "bg-primary/15 text-primary",
  ended: "bg-muted text-muted-foreground",
};

const DOT_CLASS: Record<StatusTone, string> = {
  live: "bg-success animate-pulse",
  waiting: "bg-warning",
  paused: "bg-primary",
  ended: "bg-muted-foreground",
};

export default function GroupCard({
  group,
  position,
}: {
  group: GroupSummary;
  position: number;
}) {
  const tone = groupStatusTone(group.status);
  const ocid = `groups.item.${position}`;
  const turn = clampTurn(group.turnSeq, group.maxTurns);
  const pct =
    group.maxTurns > 0n ? Math.round((turn / Number(group.maxTurns)) * 100) : 0;

  return (
    <li
      data-ocid={ocid}
      className="admin-card p-4 transition-colors hover:bg-secondary/40"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-display text-base font-semibold">
            {group.name}
          </h3>
          {group.topic ? (
            <p className="truncate text-xs text-muted-foreground">
              {group.topic}
            </p>
          ) : null}
        </div>
        <span
          data-ocid={`${ocid}.status_badge`}
          className={cn(
            "inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium",
            TONE_CLASS[tone],
          )}
        >
          <span
            aria-hidden="true"
            className={cn("size-1.5 rounded-full", DOT_CLASS[tone])}
          />
          {groupStatusLabelAr(group.status)}
        </span>
      </div>

      <div
        aria-hidden="true"
        className="mt-3 h-1.5 overflow-hidden rounded-full bg-secondary"
      >
        <div
          className="h-full rounded-full bg-primary transition-[width]"
          style={{ width: `${Math.min(100, pct)}%` }}
        />
      </div>

      <dl className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-muted-foreground">
        <div className="flex items-center gap-1.5">
          <Users aria-hidden="true" className="size-3.5" />
          <dt className="sr-only">الأعضاء</dt>
          <dd>
            {onlineCountLabelAr(group.onlineCount)} من{" "}
            {formatNumberAr(group.memberCount)}
          </dd>
        </div>
        <div>
          <dt className="sr-only">التقدم</dt>
          <dd>{turnProgressAr(group.turnSeq, group.maxTurns)}</dd>
        </div>
        <div className="flex items-center gap-1.5">
          <Repeat aria-hidden="true" className="size-3.5" />
          <dt className="sr-only">نمط الأدوار</dt>
          <dd>{turnModeLabelAr(group.turnMode)}</dd>
        </div>
      </dl>

      <div className="mt-4 flex items-center justify-between gap-3">
        <span className="text-xs text-muted-foreground">
          {formatListTimeAr(group.updatedAt)}
        </span>
        <Link
          to="/admin/g/$id"
          params={{ id: group.id.toString() }}
          data-ocid={`${ocid}.open_button`}
          className="inline-flex h-10 items-center gap-1 rounded-full bg-primary px-5 text-sm font-semibold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        >
          فتح
          <ChevronLeft aria-hidden="true" className="size-4" />
        </Link>
      </div>
    </li>
  );
}
