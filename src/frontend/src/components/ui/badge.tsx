import { type VariantProps, cva } from "class-variance-authority";
import type * as React from "react";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex shrink-0 items-center gap-1.5 rounded-full border font-semibold leading-none",
  {
    variants: {
      tone: {
        neutral: "border-border bg-secondary text-secondary-foreground",
        primary: "border-primary/30 bg-primary/15 text-primary",
        success: "border-success/30 bg-success/15 text-success",
        warning: "border-warning/30 bg-warning/15 text-warning",
        danger: "border-destructive/35 bg-destructive/15 text-destructive",
        info: "border-info/30 bg-info/15 text-info",
        muted: "border-border-subtle bg-muted text-muted-foreground",
      },
      size: {
        sm: "px-2 py-0.5 text-2xs",
        default: "px-2.5 py-1 text-xs",
        lg: "px-3 py-1.5 text-sm",
      },
    },
    defaultVariants: {
      tone: "neutral",
      size: "default",
    },
  },
);

const dotTone: Record<string, string> = {
  neutral: "bg-muted-foreground",
  primary: "bg-primary",
  success: "bg-success",
  warning: "bg-warning",
  danger: "bg-destructive",
  info: "bg-info",
  muted: "bg-muted-foreground",
};

export interface BadgeProps
  extends React.ComponentProps<"span">,
    VariantProps<typeof badgeVariants> {
  dot?: boolean;
  /** Adds a soft pulse to the dot (live states). */
  pulse?: boolean;
}

/** Compact status label. Purely presentational — pair with visible text. */
export function Badge({
  className,
  tone = "neutral",
  size,
  dot = false,
  pulse = false,
  children,
  ...props
}: BadgeProps) {
  return (
    <span
      data-slot="badge"
      className={cn(badgeVariants({ tone, size }), className)}
      {...props}
    >
      {dot ? (
        <span
          aria-hidden="true"
          className={cn(
            "size-1.5 rounded-full",
            dotTone[tone ?? "neutral"],
            pulse && "animate-pulse",
          )}
        />
      ) : null}
      {children}
    </span>
  );
}

export { badgeVariants };
