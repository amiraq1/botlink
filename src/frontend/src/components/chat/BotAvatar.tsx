import { accentClass } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Bot } from "lucide-react";

interface BotAvatarProps {
  name: string;
  accentColor: string;
  size?: "sm" | "md" | "lg";
  /** Online members get the gently pulsing glow; offline members get a dimmed ring. */
  state?: "live" | "idle" | "offline";
  className?: string;
}

const SIZES = {
  sm: "size-8 [&>svg]:size-4",
  md: "size-10 [&>svg]:size-5",
  lg: "size-14 [&>svg]:size-7",
} as const;

/** Circular robot avatar with a glowing ring in the member's accent colour. */
export default function BotAvatar({
  name,
  accentColor,
  size = "md",
  state = "idle",
  className,
}: BotAvatarProps) {
  return (
    <span
      role="img"
      aria-label={name}
      className={cn(
        accentClass(accentColor, name),
        state === "offline" ? "avatar-ring-offline" : "avatar-ring",
        state === "live" && "avatar-ring-live",
        "flex shrink-0 items-center justify-center bg-secondary text-bot",
        SIZES[size],
        className,
      )}
    >
      <Bot aria-hidden="true" />
    </span>
  );
}
