import { cn } from "@/lib/utils";

/** Loading placeholder with a subtle shimmer (static under reduced motion). */
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="skeleton"
      aria-hidden="true"
      className={cn(
        "relative overflow-hidden rounded-md bg-elevated/70",
        "after:absolute after:inset-0 after:-translate-x-full after:bg-gradient-to-r after:from-transparent after:via-foreground/[0.06] after:to-transparent after:animate-shimmer after:content-['']",
        className,
      )}
      {...props}
    />
  );
}

export { Skeleton };
