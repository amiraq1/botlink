import { Button } from "@/components/ui/button";
import { useCreateGroup } from "@/hooks/useAdminQueries";
import { TurnMode } from "@/lib/api";
import { numberToBigint, turnModeHintAr, turnModeLabelAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { useNavigate } from "@tanstack/react-router";
import { AlertCircle, AtSign, Plus, Repeat, Users } from "lucide-react";
import { type FormEvent, useId, useState } from "react";
import BotsBuilder from "./BotsBuilder";
import {
  DEFAULT_GROUP_DESCRIPTION,
  DEFAULT_GROUP_NAME,
  DEFAULT_MAX_TURNS,
  presetBots,
} from "./botDraft";

const fieldClass =
  "admin-input h-11 w-full min-w-0 px-4 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50";

const TURN_MODES = [
  { mode: TurnMode.auto_round_robin, icon: Repeat },
  { mode: TurnMode.leader_round, icon: AtSign },
] as const;

function SectionHeading({
  step,
  title,
  hint,
}: {
  step: number;
  title: string;
  hint?: string;
}) {
  return (
    <div className="flex items-start gap-3">
      <span className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full border border-primary/30 bg-primary/15 font-mono text-2xs font-bold text-primary">
        {step}
      </span>
      <div className="min-w-0">
        <h3 className="font-display text-sm font-bold text-foreground">
          {title}
        </h3>
        {hint ? (
          <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>
        ) : null}
      </div>
    </div>
  );
}

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
      id="create-group"
      data-ocid="groups.create_form"
      onSubmit={handleSubmit}
      className="admin-card scroll-mt-6 overflow-hidden"
    >
      <header className="flex items-start justify-between gap-3 border-b border-border-subtle bg-surface/40 px-5 py-4">
        <div>
          <h2 className="font-display text-base font-bold text-foreground">
            إنشاء مجموعة جديدة
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            حدد البوتات وطريقة تبادل الأدوار ثم ابدأ المحادثة.
          </p>
        </div>
        <span className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-primary/25 bg-primary/15 text-primary">
          <Users aria-hidden="true" className="size-4" />
        </span>
      </header>

      <div className="flex flex-col gap-5 p-5">
        <div className="space-y-3">
          <SectionHeading
            step={1}
            title="تفاصيل المجموعة"
            hint="اسم واضح وموضوع قصير يساعد البوتات على البقاء في السياق."
          />
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <label
                htmlFor={`${uid}-name`}
                className="text-xs font-semibold text-foreground-secondary"
              >
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
              <label
                htmlFor={`${uid}-topic`}
                className="text-xs font-semibold text-foreground-secondary"
              >
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
              <label
                htmlFor={`${uid}-desc`}
                className="text-xs font-semibold text-foreground-secondary"
              >
                الوصف
              </label>
              <textarea
                id={`${uid}-desc`}
                data-ocid="groups.description_input"
                value={description}
                rows={2}
                maxLength={300}
                onChange={(e) => setDescription(e.target.value)}
                className="admin-input w-full resize-none rounded-md px-4 py-2.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
              />
            </div>
          </div>
        </div>

        <div className="h-px bg-border-subtle" />

        <fieldset className="space-y-3">
          <legend className="sr-only">نمط الأدوار</legend>
          <SectionHeading
            step={2}
            title="نمط الأدوار"
            hint="اختر كيف يتناوب البوتات على الكتابة."
          />
          <div className="grid gap-2 sm:grid-cols-2">
            {TURN_MODES.map(({ mode, icon: Icon }) => {
              const selected = turnMode === mode;
              return (
                <label
                  key={mode}
                  data-ocid={`groups.turn_mode.${mode}`}
                  className={cn(
                    "group relative flex cursor-pointer flex-col gap-2 rounded-lg border p-3 transition-[border-color,background-color,box-shadow] duration-fast ease-out",
                    selected
                      ? "border-primary/60 bg-primary/10 shadow-glow"
                      : "border-border bg-surface/50 hover:border-border-strong hover:bg-secondary",
                  )}
                >
                  <span className="flex items-center justify-between gap-2">
                    <Icon
                      aria-hidden="true"
                      className={cn(
                        "size-4",
                        selected ? "text-primary" : "text-muted-foreground",
                      )}
                    />
                    <input
                      type="radio"
                      name={`${uid}-turn-mode`}
                      value={mode}
                      checked={selected}
                      onChange={() => setTurnMode(mode)}
                      className="size-4 accent-[oklch(var(--primary))]"
                    />
                  </span>
                  <span className="block text-sm font-bold text-foreground">
                    {turnModeLabelAr(mode)}
                  </span>
                  <span className="block text-2xs leading-relaxed text-muted-foreground">
                    {turnModeHintAr(mode)}
                  </span>
                </label>
              );
            })}
          </div>
        </fieldset>

        <div className="space-y-1.5">
          <label
            htmlFor={`${uid}-turns`}
            className="text-xs font-semibold text-foreground-secondary"
          >
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
            className={cn(fieldClass, "max-w-32 text-center")}
            dir="ltr"
          />
          <p className="text-2xs text-muted-foreground">
            تتوقف المجموعة تلقائياً عند بلوغ هذا العدد من الأدوار.
          </p>
        </div>

        <div className="h-px bg-border-subtle" />

        <SectionHeading
          step={3}
          title="أعضاء المجموعة"
          hint="من ٢ إلى ١٠ بوتات، لكل بوت اسم ودور ولون مميز."
        />
        <BotsBuilder bots={bots} onChange={setBots} />

        {errorText ? (
          <div
            role="alert"
            data-ocid="groups.create_error_state"
            className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs font-medium text-destructive"
          >
            <AlertCircle
              aria-hidden="true"
              className="mt-0.5 size-4 shrink-0"
            />
            <span>{errorText}</span>
          </div>
        ) : null}
      </div>

      <footer className="border-t border-border-subtle bg-surface/40 p-4">
        <Button
          type="submit"
          size="lg"
          data-ocid="groups.submit_button"
          loading={createGroup.isPending}
          className="w-full"
        >
          {createGroup.isPending ? (
            <span data-ocid="groups.create_loading_state">جارٍ الإنشاء…</span>
          ) : (
            <>
              <Plus aria-hidden="true" className="size-4" />
              إنشاء المجموعة
            </>
          )}
        </Button>
      </footer>
    </form>
  );
}
