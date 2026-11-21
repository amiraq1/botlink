import { TurnMode } from "@/backend";
import { useCreateGroup } from "@/hooks/useAdminQueries";
import { numberToBigint, turnModeHintAr, turnModeLabelAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import { AlertCircle, Loader2, Plus } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import BotsBuilder from "./BotsBuilder";
import {
  DEFAULT_GROUP_DESCRIPTION,
  DEFAULT_GROUP_NAME,
  DEFAULT_MAX_TURNS,
  presetBots,
} from "./botDraft";

const fieldClass =
  "admin-input h-11 w-full min-w-0 px-4 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring";

const TURN_MODES = [TurnMode.auto_round_robin, TurnMode.leader_round] as const;

export default function CreateGroupCard() {
  const uid = useId();
  const navigate = useNavigate();
  const createGroup = useCreateGroup();

  const [name, setName] = useState(DEFAULT_GROUP_NAME);
  const [description, setDescription] = useState(DEFAULT_GROUP_DESCRIPTION);
  const [topic, setTopic] = useState("");
  const [turnMode, setTurnMode] = useState<TurnMode>(TurnMode.auto_round_robin);
  const [maxTurns, setMaxTurns] = useState(String(DEFAULT_MAX_TURNS));
  const [bots, setBots] = useState(presetBots);
  const [validation, setValidation] = useState<string | null>(null);

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const turns = Number.parseInt(maxTurns, 10);
    if (name.trim() === "") {
      setValidation("أدخل اسماً للمجموعة.");
      return;
    }
    if (!Number.isFinite(turns) || turns < 1) {
      setValidation("الحد الأقصى للأدوار يجب أن يكون ١ على الأقل.");
      return;
    }
    if (bots.some((b) => b.name.trim() === "")) {
      setValidation("لكل بوت اسم، أكمل الأسماء الفارغة أو احذف الصفوف.");
      return;
    }
    setValidation(null);
    createGroup.mutate(
      {
        name: name.trim(),
        description: description.trim(),
        topic: topic.trim(),
        turnMode,
        maxTurns: numberToBigint(turns),
        members: bots.map((b) => ({
          name: b.name.trim(),
          roleTitle: b.roleTitle.trim(),
          accentColor: b.accentColor,
          isMock: b.isMock,
        })),
      },
      {
        onSuccess: (id) => {
          void navigate({ to: "/admin/g/$id", params: { id: id.toString() } });
        },
      },
    );
  };

  const errorText =
    validation ??
    (createGroup.isError
      ? "تعذر إنشاء المجموعة. تحقق من الاتصال وحاول مرة أخرى."
      : null);

  return (
    <form
      data-ocid="groups.create_form"
      onSubmit={handleSubmit}
      className="admin-card space-y-5 p-4 sm:p-6"
    >
      <header>
        <h2 className="font-display text-xl font-bold">إنشاء مجموعة جديدة</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          حدد البوتات وطريقة تبادل الأدوار ثم ابدأ المحادثة.
        </p>
      </header>

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <label htmlFor={`${uid}-name`} className="text-sm font-medium">
            اسم المجموعة
          </label>
          <input
            id={`${uid}-name`}
            data-ocid="groups.name_input"
            value={name}
            maxLength={80}
            onChange={(e) => setName(e.target.value)}
            className={fieldClass}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={`${uid}-topic`} className="text-sm font-medium">
            الموضوع
          </label>
          <input
            id={`${uid}-topic`}
            data-ocid="groups.topic_input"
            value={topic}
            maxLength={120}
            placeholder="مثال: إطلاق النسخة الأولى"
            onChange={(e) => setTopic(e.target.value)}
            className={fieldClass}
          />
        </div>
        <div className="space-y-1.5 sm:col-span-2">
          <label htmlFor={`${uid}-desc`} className="text-sm font-medium">
            الوصف
          </label>
          <textarea
            id={`${uid}-desc`}
            data-ocid="groups.description_input"
            value={description}
            rows={2}
            maxLength={300}
            onChange={(e) => setDescription(e.target.value)}
            className="admin-input w-full resize-none rounded-2xl px-4 py-2.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>
      </div>

      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">نمط الأدوار</legend>
        <div className="grid gap-2 sm:grid-cols-2">
          {TURN_MODES.map((mode) => {
            const selected = turnMode === mode;
            return (
              <label
                key={mode}
                data-ocid={`groups.turn_mode.${mode}`}
                className={cn(
                  "flex cursor-pointer items-start gap-3 rounded-2xl border p-3 transition-colors focus-within:outline focus-within:outline-2 focus-within:outline-ring",
                  selected
                    ? "border-primary bg-primary/10"
                    : "border-border bg-background/40 hover:bg-secondary",
                )}
              >
                <input
                  type="radio"
                  name={`${uid}-turn-mode`}
                  value={mode}
                  checked={selected}
                  onChange={() => setTurnMode(mode)}
                  className="mt-1 size-4 accent-[oklch(var(--primary))]"
                />
                <span className="min-w-0">
                  <span className="block text-sm font-semibold">
                    {turnModeLabelAr(mode)}
                  </span>
                  <span className="block text-xs text-muted-foreground">
                    {turnModeHintAr(mode)}
                  </span>
                </span>
              </label>
            );
          })}
        </div>
      </fieldset>

      <div className="max-w-xs space-y-1.5">
        <label htmlFor={`${uid}-turns`} className="text-sm font-medium">
          الحد الأقصى للأدوار
        </label>
        <input
          id={`${uid}-turns`}
          data-ocid="groups.max_turns_input"
          type="number"
          inputMode="numeric"
          min={1}
          max={1000}
          value={maxTurns}
          onChange={(e) => setMaxTurns(e.target.value)}
          className={fieldClass}
          dir="ltr"
        />
      </div>

      <BotsBuilder bots={bots} onChange={setBots} />

      {errorText ? (
        <div
          role="alert"
          data-ocid="groups.create_error_state"
          className="flex items-start gap-2 rounded-2xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
        >
          <AlertCircle aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>{errorText}</span>
        </div>
      ) : null}

      <button
        type="submit"
        data-ocid="groups.submit_button"
        disabled={createGroup.isPending}
        className="inline-flex h-12 w-full items-center justify-center gap-2 rounded-full bg-primary px-6 text-sm font-semibold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-60"
      >
        {createGroup.isPending ? (
          <>
            <Loader2 aria-hidden="true" className="size-4 animate-spin" />
            <span data-ocid="groups.create_loading_state">جارٍ الإنشاء…</span>
          </>
        ) : (
          <>
            <Plus aria-hidden="true" className="size-4" />
            إنشاء المجموعة
          </>
        )}
      </button>
    </form>
  );
}
