import { cn } from "@/lib/utils";
import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";

interface CopyButtonProps {
  label: string;
  text: string;
  ocid: string;
  className?: string;
  /** Icon-only rendering for dense toolbars; `label` becomes the aria-label. */
  compact?: boolean;
}

async function writeClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** Copies text to the clipboard and briefly confirms with a check mark. */
export default function CopyButton({
  label,
  text,
  ocid,
  className,
  compact = false,
}: CopyButtonProps) {
  const [status, setStatus] = useState<"idle" | "copied" | "failed">("idle");
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const handleClick = async () => {
    const ok = await writeClipboard(text);
    setStatus(ok ? "copied" : "failed");
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setStatus("idle"), 1800);
  };

  const text_ =
    status === "copied"
      ? "تم النسخ ✓"
      : status === "failed"
        ? "تعذر النسخ"
        : label;

  return (
    <button
      type="button"
      data-ocid={ocid}
      onClick={handleClick}
      aria-label={compact ? text_ : undefined}
      title={compact ? label : undefined}
      className={cn(
        "inline-flex min-h-9 items-center justify-center gap-1.5 rounded-full border border-border bg-secondary/70 font-semibold text-secondary-foreground transition-[background-color,border-color,color] duration-fast ease-out hover:border-border-strong hover:bg-elevated focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
        compact ? "size-9 px-0" : "px-3 text-xs",
        status === "copied" && "border-success/50 text-success",
        status === "failed" && "border-destructive/50 text-destructive",
        className,
      )}
    >
      {status === "copied" ? (
        <Check aria-hidden="true" className="size-3.5" />
      ) : (
        <Copy aria-hidden="true" className="size-3.5" />
      )}
      {compact ? null : <span aria-live="polite">{text_}</span>}
    </button>
  );
}
