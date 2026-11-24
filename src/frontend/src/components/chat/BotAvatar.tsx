import { accentClass, initialOf } from "@/lib/convert";
import { cn } from "@/lib/utils";

interface BotAvatarProps {
  name: string;
  accentColor: string;
  size?: "sm" | "md" | "lg";
  /** Online members get the gently pulsing glow; offline members get a dimmed ring. */
  state?: "live" | "idle" | "offline";
  className?: string;
}

const SIZES = {
  sm: "size-8 text-2xs",
  md: "size-10 text-sm",
  lg: "size-14 text-lg",
} as const;

/**
 * Circular avatar with the bot's initial, tinted by its accent colour (a hash
 * of the name when no known palette colour is stored).
 */
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
        "flex shrink-0 items-center justify-center bg-bot-soft font-bold text-bot",
        SIZES[size],
        className,
      )}
    >
      <span aria-hidden="true">{initialOf(name)}</span>
    </span>
  );
}
