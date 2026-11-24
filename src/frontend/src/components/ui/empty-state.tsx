import type * as React from "react";

import { cn } from "@/lib/utils";

interface EmptyStateProps {
  icon?: React.ReactNode;
  title: string;
  description?: string;
  action?: React.ReactNode;
  className?: string;
  ocid?: string;
}

/** Friendly empty / zero-state block with an optional call to action. */
export function EmptyState({
  icon,
  title,
  description,
  action,
  className,
  ocid,
}: EmptyStateProps) {
  return (
    <div
      data-ocid={ocid}
      data-slot="empty-state"
      className={cn(
        "flex flex-col items-center gap-3 rounded-xl border border-dashed border-border-strong bg-surface/40 px-6 py-10 text-center",
        className,
      )}
    >
      {icon ? (
        <span
          aria-hidden="true"
          className="flex size-12 items-center justify-center rounded-full border border-primary/25 bg-primary/15 text-primary [&_svg]:size-5"
        >
          {icon}
        </span>
      ) : null}
      <p className="font-display text-base font-bold text-foreground">
        {title}
      </p>
      {description ? (
        <p className="max-w-sm text-sm leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}
      {action ? <div className="pt-1">{action}</div> : null}
    </div>
  );
}
