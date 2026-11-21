import { cn } from "@/lib/utils";
import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";

interface CopyButtonProps {
  label: string;
  text: string;
  ocid: string;
  className?: string;
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

  return (
    <button
      type="button"
      data-ocid={ocid}
      onClick={handleClick}
      className={cn(
        "inline-flex min-h-9 items-center gap-1.5 rounded-full border border-border bg-secondary px-3 text-xs font-semibold text-foreground transition-colors hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
        status === "copied" && "border-success text-success",
        status === "failed" && "border-destructive text-destructive",
        className,
      )}
    >
      {status === "copied" ? (
        <Check aria-hidden="true" className="size-3.5" />
      ) : (
        <Copy aria-hidden="true" className="size-3.5" />
      )}
      <span aria-live="polite">
        {status === "copied"
          ? "تم النسخ ✓"
          : status === "failed"
            ? "تعذر النسخ"
            : label}
      </span>
    </button>
  );
}
