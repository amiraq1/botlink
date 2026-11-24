import { ChevronDown } from "lucide-react";
import type * as React from "react";

import { cn } from "@/lib/utils";

/** Native select styled to match Input (keeps platform keyboard behaviour). */
function Select({
  className,
  children,
  ...props
}: React.ComponentProps<"select">) {
  return (
    <div className="relative">
      <select
        data-slot="select"
        className={cn(
          "h-10 w-full appearance-none rounded-md border border-border bg-elevated/60 px-3 pe-9 text-sm text-foreground shadow-xs outline-none transition-[border-color,box-shadow] duration-fast",
          "hover:border-border-strong focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/35",
          "disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute inset-y-0 end-2.5 my-auto size-4 text-muted-foreground"
      />
    </div>
  );
}

export { Select };
