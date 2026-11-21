import { formatNumberAr } from "@/lib/convert";
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
        <div>
          <h3
            id="bots-builder-title"
            className="font-display text-base font-semibold"
          >
            البوتات الأعضاء
          </h3>
          <p className="text-xs text-muted-foreground">
            {formatNumberAr(bots.length)} من {formatNumberAr(MAX_BOTS)} بوتات
          </p>
        </div>
        <button
          type="button"
          data-ocid="groups.preset_button"
          onClick={() => onChange(presetBots())}
          className="inline-flex h-9 items-center gap-1.5 rounded-full border border-border px-3 text-sm text-foreground transition-colors hover:bg-secondary focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
        >
          <Sparkles aria-hidden="true" className="size-4 text-primary" />
          إعداد سريع: ٤ بوتات
        </button>
      </div>

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
        <button
          type="button"
          data-ocid="groups.add_bot_button"
          disabled={atMax}
          onClick={() => onChange([...bots, blankBot(bots)])}
          className="inline-flex h-10 items-center gap-1.5 rounded-full bg-secondary px-4 text-sm font-medium text-foreground transition-colors hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50"
        >
          <Plus aria-hidden="true" className="size-4" />
          إضافة بوت
        </button>
        {atMin ? (
          <p
            data-ocid="groups.min_bots_message"
            className="text-xs text-warning"
          >
            لا يمكن أن تقل المجموعة عن {formatNumberAr(MIN_BOTS)} بوتات.
          </p>
        ) : null}
        {atMax ? (
          <p
            data-ocid="groups.max_bots_message"
            className="text-xs text-warning"
          >
            وصلت إلى الحد الأقصى: {formatNumberAr(MAX_BOTS)} بوتات في المجموعة.
          </p>
        ) : null}
      </div>
    </section>
  );
}
