import { Button } from "@/components/ui/button";
import { accentClass, formatNumberAr, initialOf } from "@/lib/convert";
import { Plus, Sparkles } from "lucide-react";
import BotRow from "./BotRow";
import {
  type BotDraft,
  MAX_BOTS,
  MIN_BOTS,
  blankBot,
  presetBots,
} from "./botDraft";

interface BotsBuilderProps {
  bots: BotDraft[];
  onChange: (bots: BotDraft[]) => void;
}

export default function BotsBuilder({ bots, onChange }: BotsBuilderProps) {
  const atMin = bots.length <= MIN_BOTS;
  const atMax = bots.length >= MAX_BOTS;

  const patchBot = (key: string, patch: Partial<Omit<BotDraft, "key">>) =>
    onChange(bots.map((b) => (b.key === key ? { ...b, ...patch } : b)));

  return (
    <section aria-labelledby="bots-builder-title" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <h3
            id="bots-builder-title"
            className="text-xs font-semibold text-foreground-secondary"
          >
            البوتات الأعضاء
          </h3>
          <span className="rounded-full border border-border bg-secondary px-2 py-0.5 font-mono text-2xs text-muted-foreground tabular">
            {formatNumberAr(bots.length)}/{formatNumberAr(MAX_BOTS)}
          </span>
        </div>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          data-ocid="groups.preset_button"
          onClick={() => onChange(presetBots())}
        >
          <Sparkles aria-hidden="true" className="size-3.5 text-primary" />
          إعداد سريع: ٤ بوتات
        </Button>
      </div>

      {/* Live preview of the roster as it will appear in the room. */}
      <ul
        aria-label="معاينة البوتات"
        className="flex flex-wrap items-center gap-2 rounded-lg border border-border-subtle bg-background/40 px-3 py-2.5"
      >
        {bots.map((bot, index) => (
          <li
            key={bot.key}
            data-ocid={`groups.preview.${index + 1}`}
            className={accentClass(bot.accentColor, bot.name)}
            title={bot.name || `بوت ${formatNumberAr(index + 1)}`}
          >
            <span
              aria-hidden="true"
              className="flex size-8 items-center justify-center rounded-full border border-bot/50 bg-bot-soft text-xs font-bold text-bot"
            >
              {initialOf(bot.name || "?")}
            </span>
            <span className="sr-only">
              {bot.name || `بوت ${formatNumberAr(index + 1)}`}
              {bot.isMock ? " (تجريبي)" : ""}
            </span>
          </li>
        ))}
        {bots.length === 0 ? (
          <li className="text-2xs text-muted-foreground">
            أضف بوتاً واحداً على الأقل للمتابعة.
          </li>
        ) : null}
      </ul>

      <ul className="space-y-2">
        {bots.map((bot, index) => (
          <BotRow
            key={bot.key}
            bot={bot}
            position={index + 1}
            canRemove={!atMin}
            onChange={(patch) => patchBot(bot.key, patch)}
            onRemove={() => onChange(bots.filter((b) => b.key !== bot.key))}
          />
        ))}
      </ul>

      <div className="flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="secondary"
          size="sm"
          data-ocid="groups.add_bot_button"
          disabled={atMax}
          onClick={() => onChange([...bots, blankBot(bots)])}
        >
          <Plus aria-hidden="true" className="size-3.5" />
          إضافة بوت
        </Button>
        {atMin ? (
          <p
            data-ocid="groups.min_bots_message"
            className="text-2xs text-warning"
          >
            لا يمكن أن تقل المجموعة عن {formatNumberAr(MIN_BOTS)} بوتات.
          </p>
        ) : null}
        {atMax ? (
          <p
            data-ocid="groups.max_bots_message"
            className="text-2xs text-warning"
          >
            وصلت إلى الحد الأقصى: {formatNumberAr(MAX_BOTS)} بوتات في المجموعة.
          </p>
        ) : null}
      </div>
    </section>
  );
}
