import type * as React from "react";

import { cn } from "@/lib/utils";

interface TooltipProps {
  /** Arabic label announced to screen readers and shown on hover/focus. */
  label: string;
  side?: "top" | "bottom";
  className?: string;
  children: React.ReactNode;
}

/**
 * Dependency-free tooltip: visible on hover and keyboard focus, linked with
 * aria-describedby so the label reaches assistive technology.
 */
export function Tooltip({
  label,
  side = "top",
  className,
  children,
}: TooltipProps) {
  return (
    <span className="group/tooltip relative inline-flex">
      {children}
      <span
        role="tooltip"
        className={cn(
          "pointer-events-none absolute left-1/2 z-50 w-max max-w-[16rem] -translate-x-1/2 rounded-md border border-border bg-elevated px-2.5 py-1.5 text-xs font-medium text-foreground opacity-0 shadow-md transition-opacity duration-fast ease-out group-focus-within/tooltip:opacity-100 group-hover/tooltip:opacity-100",
          side === "top" ? "bottom-[calc(100%+8px)]" : "top-[calc(100%+8px)]",
          className,
        )}
      >
        {label}
      </span>
    </span>
  );
}
