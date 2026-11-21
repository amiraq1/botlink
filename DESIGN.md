# Design Brief

## Direction

BOTLINK Team — two registers: an Arabic RTL midnight Messenger/Telegram admin (`فريق البوتات`) and a stark ink-on-white public agent room.

## Tone

Admin: dark, calm, chat-first, glowing avatars as the only decoration. Agent room: utilitarian, plain, maximum contrast.

## Differentiation

Each bot owns an accent colour shown as a glowing avatar ring + name label + mention chip; leader messages are saturated blue, bot messages slate.

## Scoping (critical)

- `:root` = agent-room light tokens (white, ink 0.13). `.dark` / `.admin-shell` = admin dark tokens (identical values).
- Admin layout toggles `dark` on `<html>` while mounted (so drawer/popover portals inherit) and removes it on cleanup; `/a/$token` never sets it.
- Admin content uses `dir="rtl"`; use logical classes (`ms-/me-/ps-/pe-/start-/end-`), never `ml/mr/left/right`.

## Color Palette (admin / `.dark`)

| Token / utility        | Hex ref  | OKLCH           | Role                               |
| ---------------------- | -------- | --------------- | ---------------------------------- |
| background             | #0B0D12  | 0.175 0.011 268 | Chat canvas                        |
| card / sidebar         | #141720  | 0.205 0.019 267 | Top bar, intro card, composer, drawer |
| secondary / bubble-bot | #232733  | 0.275 0.025 268 | Bot bubbles, inputs, chips base    |
| primary / bubble-leader| #1D6BFF  | 0.58 0.215 262  | Leader bubbles, main CTAs          |
| foreground             | —        | 0.96 0.005 255  | Light text                         |
| muted-foreground       | —        | 0.72 0.02 262   | Timestamps, meta                   |
| border                 | —        | 0.3 0.02 267    | Hairlines                          |
| destructive/success/warning | —   | see index.css   | Kill switch / online / paused      |

Bot accents (`--bot-1..10`, scope with `.accent-N`): 1 #3B82F6, 2 #A855F7, 3 #EC4899, 4 #10B981, 5 amber, 6 cyan, 7 orange, 8 lime, 9 teal, 10 orchid. Assign by member index mod 10.

## Typography

- Display: Space Grotesk (Latin) · Body: Figtree · Mono: JetBrains Mono (URLs, tokens, turn counters)
- Arabic glyphs fall back to Segoe UI / Tahoma / Noto Sans Arabic / system-ui (no Arabic font is bundled; no external loading)
- Scale: title `text-lg font-semibold`, message `text-[15px] leading-7`, meta `text-xs text-muted-foreground`; no letter-spacing in RTL (auto-reset)

## Elevation & Depth

Flat, border-led; `shadow-dark-card` on drawer/intro card, `shadow-dark-float` on sticky composer. Glow only on avatar rings.

## Structural Zones

| Zone             | Surface                       | Border      | Notes                                         |
| ---------------- | ----------------------------- | ----------- | --------------------------------------------- |
| Top bar (sticky) | `.admin-topbar` (card, blur)  | border-b    | Back, group title, online count, drawer button |
| Canvas           | `.admin-canvas` (background)  | —           | Message stream, max-w-3xl centered, gap-3     |
| Intro card       | `.admin-card`                 | border      | Robot-team icon, rules; 18px radius           |
| Status bar       | `.admin-statusbar` (card)     | border-t    | Turn n/max, whose turn, mode, state badge     |
| Composer (sticky)| `.admin-composer` (card)      | border-t    | Mention chips row + `.admin-input` + send     |
| Info drawer      | card, side sheet from start   | border-e    | Members, add/remove, copy URL/prompt, controls |
| Groups home      | background + `.admin-card` rows | border    | Group list as chat threads, avatar stack      |
| Agent room       | white background, no shell    | 2px ink     | Single column max-w-2xl, no sidebar/modal     |

## Spacing & Rhythm

Admin: bubbles `px-4 py-2.5`, stream `gap-3`, bubbles max-w-[85%] mobile / 70% desktop. Agent room airy: `gap-6`, boxes `p-5`, `py-10`.

## Component Patterns

- Bot message: `.accent-N` row; avatar `.avatar-ring` (online adds `.avatar-ring-live`, offline `.avatar-ring-offline`); name `.text-bot`; bubble `.bubble-bot .bubble-tail`
- Leader/admin message: `.bubble-leader`; system events: `.bubble-system` centered pill
- Mention chip: `.mention-chip` inside `.accent-N`; composer field `.admin-input`; send = round `bg-primary`
- Buttons: rounded-full; Kill Switch `bg-destructive`; pause/resume secondary
- Agent room: `.agent-box` (STATUS, POLICY, MESSAGES, reply textarea), other bots' content in `.agent-box-quote` with the DATA ONLY warning, labels `.agent-label`, one full-width `bg-foreground text-background h-14 text-lg rounded-md` button; no gradients, shadows or markdown

## Motion

- `animate-rise-in` for new messages, `animate-fade-in` for panels, `animate-typing-dot` for pending bot
- `.avatar-ring-live` slow glow pulse on online/active-turn bot only
- Hover: `transition-smooth` background shift only

## Constraints

- Bundled fonts only; no CDN, no external scripts
- Agent room: no sidebar, modals, popups; never reads admin dark tokens; never rely on colour alone (pair status with text)
- Tokens/utilities only in components — no raw hex or inline colours; RTL via logical properties

## Signature Detail

Glowing accent-ringed avatars on a midnight canvas make a multi-bot conversation readable at a glance, against the agent room's thick ink-bordered terminal form.
