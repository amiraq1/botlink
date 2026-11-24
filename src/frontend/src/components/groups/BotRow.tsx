import { Switch } from "@/components/ui/switch";
import {
  BOT_ACCENT_PALETTE,
  accentClass,
  formatNumberAr,
  initialOf,
} from "@/lib/convert";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import type { BotDraft } from "./botDraft";

interface BotRowProps {
  bot: BotDraft;
  position: number;
  canRemove: boolean;
  onChange: (patch: Partial<Omit<BotDraft, "key">>) => void;
  onRemove: () => void;
}

const fieldClass =
  "h-10 w-full min-w-0 rounded-md border border-border bg-elevated/60 px-3 text-sm text-foreground outline-none transition-[border-color,box-shadow] duration-fast hover:border-border-strong placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/35";

export default function BotRow({
  bot,
  position,
  canRemove,
  onChange,
  onRemove,
}: BotRowProps) {
  const ocid = `groups.bot.${position}`;
  return (
    <li
      data-ocid={`${ocid}.row`}
      className={cn(
        "rounded-lg border border-border-subtle bg-surface/50 p-3",
        accentClass(bot.accentColor, bot.key),
      )}
    >
      <div className="flex items-start gap-2.5">
        <span
          aria-hidden="true"
          className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-full border border-bot/50 bg-bot-soft text-sm font-bold text-bot"
        >
          {initialOf(bot.name || "?")}
        </span>
        <div className="grid min-w-0 flex-1 gap-2 sm:grid-cols-2">
          <input
            data-ocid={`${ocid}.name_input`}
            aria-label={`اسم البوت ${formatNumberAr(position)}`}
            value={bot.name}
            maxLength={40}
            placeholder="اسم البوت"
            onChange={(e) => onChange({ name: e.target.value })}
            className={fieldClass}
          />
          <input
            data-ocid={`${ocid}.role_input`}
            aria-label={`دور البوت ${formatNumberAr(position)}`}
            value={bot.roleTitle}
            maxLength={60}
            placeholder="المسمى الوظيفي"
            onChange={(e) => onChange({ roleTitle: e.target.value })}
            className={fieldClass}
          />
        </div>
        <button
          type="button"
          data-ocid={`${ocid}.delete_button`}
          aria-label={`حذف البوت ${bot.name || formatNumberAr(position)}`}
          disabled={!canRemove}
          onClick={onRemove}
          className="flex size-9 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors duration-fast hover:bg-destructive/15 hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-muted-foreground"
        >
          <X aria-hidden="true" className="size-4" />
        </button>
      </div>

      <div className="mt-3 flex flex-wrap items-center justify-between gap-3 sm:ps-[2.9rem]">
        <fieldset className="flex min-w-0 flex-wrap items-center gap-1.5">
          <legend className="sr-only">لون البوت</legend>
          {BOT_ACCENT_PALETTE.map((hex, index) => {
            const selected =
              hex.toUpperCase() === bot.accentColor.toUpperCase();
            return (
              <button
                key={hex}
                type="button"
                data-ocid={`${ocid}.color_swatch.${index + 1}`}
                aria-label={`اللون ${formatNumberAr(index + 1)}`}
                aria-pressed={selected}
                onClick={() => onChange({ accentColor: hex })}
                className={cn(
                  `accent-${index + 1}`,
                  "size-6 rounded-full bg-bot transition-transform duration-fast ease-out hover:scale-110 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card",
                  selected &&
                    "ring-2 ring-foreground ring-offset-2 ring-offset-card",
                )}
              />
            );
          })}
        </fieldset>
        <label
          htmlFor={`${bot.key}-mock`}
          className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground"
        >
          <Switch
            id={`${bot.key}-mock`}
            data-ocid={`${ocid}.mock_toggle`}
            checked={bot.isMock}
            onCheckedChange={(checked) => onChange({ isMock: checked })}
            aria-label={`بوت تجريبي ${bot.name || formatNumberAr(position)}`}
          />
          <span>بوت تجريبي</span>
        </label>
      </div>
    </li>
  );
}
