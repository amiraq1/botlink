import { paletteColor } from "@/lib/convert";

export const MIN_BOTS = 2;
export const MAX_BOTS = 10;

export const DEFAULT_GROUP_NAME = "فريق البوتات";
export const DEFAULT_GROUP_DESCRIPTION =
  "مجموعة خاصة لتنسيق المهام وتطوير المشاريع";
export const DEFAULT_MAX_TURNS = 20;

/** Editable bot row in the create-group form. `key` is a stable client-side identity. */
export interface BotDraft {
  key: string;
  name: string;
  roleTitle: string;
  accentColor: string;
  isMock: boolean;
}

const PRESET: ReadonlyArray<Omit<BotDraft, "key" | "accentColor">> = [
  { name: "Muse", roleTitle: "المخطط الإبداعي", isMock: false },
  { name: "Grok Bot", roleTitle: "المحلل الناقد", isMock: false },
  { name: "Cue", roleTitle: "منسق المهام", isMock: false },
  { name: "MockPartner", roleTitle: "شريك تجريبي", isMock: true },
];

let counter = 0;
export function nextBotKey(): string {
  counter += 1;
  return `bot-draft-${counter}`;
}

/** Quick preset of four bots with distinct accents. */
export function presetBots(): BotDraft[] {
  return PRESET.map((bot, index) => ({
    ...bot,
    key: nextBotKey(),
    accentColor: paletteColor(index),
  }));
}

/** A fresh empty row using the first palette colour not yet in use. */
export function blankBot(existing: BotDraft[]): BotDraft {
  const used = new Set(existing.map((b) => b.accentColor.toUpperCase()));
  let index = 0;
  while (used.has(paletteColor(index).toUpperCase()) && index < MAX_BOTS) {
    index += 1;
  }
  return {
    key: nextBotKey(),
    name: "",
    roleTitle: "",
    accentColor: paletteColor(index),
    isMock: false,
  };
}
