import type { GroupControls } from "@/components/chat/useGroupControls";
import { useSetMaxTurns, useSetTurnMode } from "@/hooks/useAdminQueries";
import { type Group, TurnMode } from "@/lib/api";
import { bigintToNumber, numberToBigint } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Pause, Play, Power } from "lucide-react";
import { type FormEvent, type ReactNode, useState } from "react";

export function Section({
  title,
  children,
}: {
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3 border-b border-border px-4 py-5">
      <h3 className="text-sm font-bold text-muted-foreground">{title}</h3>
      {children}
    </section>
  );
}

const MODES: { mode: TurnMode; label: string; hint: string }[] = [
  {
    mode: TurnMode.auto_round_robin,
    label: "دور دائري تلقائي",
    hint: "Auto Round-Robin: يتناوب البوتات المتصلون تلقائياً",
  },
  {
    mode: TurnMode.leader_round,
    label: "جولة بعد رسالة القائد + منشن @",
    hint: "Leader + @Mention: يتحدث من تذكره بـ @ بعد رسالتك",
  },
];

export function TurnModeSection({ group }: { group: Group }) {
  const setMode = useSetTurnMode();
  return (
    <Section title="وضع الأدوار">
      <div
        className="flex flex-col gap-2"
        data-ocid="groupinfo.turn_mode_toggle"
      >
        {MODES.map(({ mode, label, hint }) => {
          const active = group.turnMode === mode;
          return (
            <button
              key={mode}
              type="button"
              aria-pressed={active}
              disabled={setMode.isPending}
              data-ocid={`groupinfo.turn_mode.${mode}`}
              onClick={() => {
                if (!active) setMode.mutate({ groupId: group.id, mode });
              }}
              className={cn(
                "flex min-h-12 flex-col items-start gap-0.5 rounded-2xl border px-4 py-2.5 text-start transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-60",
                active
                  ? "border-primary bg-primary/15"
                  : "border-border bg-secondary hover:bg-accent",
              )}
            >
              <span className="text-sm font-bold">{label}</span>
              <span className="text-xs text-muted-foreground">{hint}</span>
            </button>
          );
        })}
      </div>
      {setMode.isError ? (
        <p role="alert" className="text-xs text-destructive">
          تعذر تغيير وضع الأدوار.
        </p>
      ) : null}
    </Section>
  );
}

const CTRL =
  "inline-flex min-h-11 items-center justify-center gap-2 rounded-full border px-4 text-sm font-semibold transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50";

export function ControlsSection({ controls }: { controls: GroupControls }) {
  return (
    <Section title="التحكم في المجموعة">
      <div className="grid grid-cols-2 gap-2">
        <button
          type="button"
          data-ocid="groupinfo.pause_button"
          onClick={controls.pause}
          disabled={controls.busy || !controls.canPause}
          className={cn(CTRL, "border-border bg-secondary hover:bg-accent")}
        >
          <Pause aria-hidden="true" className="size-4" />
          إيقاف مؤقت
        </button>
        <button
          type="button"
          data-ocid="groupinfo.resume_button"
          onClick={controls.resume}
          disabled={controls.busy || !controls.canResume}
          className={cn(
            CTRL,
            "border-success/50 bg-success/15 text-success hover:bg-success/25",
          )}
        >
          <Play aria-hidden="true" className="size-4" />
          استئناف
        </button>
        <button
          type="button"
          data-ocid="groupinfo.kill_switch_button"
          onClick={controls.killSwitch}
          disabled={controls.busy || controls.ended}
          className={cn(
            CTRL,
            "col-span-2 border-destructive bg-destructive text-destructive-foreground hover:opacity-90",
          )}
        >
          <Power aria-hidden="true" className="size-4" />
          إيقاف فوري (Kill Switch)
        </button>
      </div>
      {controls.error ? (
        <p
          role="alert"
          data-ocid="groupinfo.controls_error_state"
          className="text-xs text-destructive"
        >
          {controls.error}
        </p>
      ) : null}
    </Section>
  );
}

export function MaxTurnsSection({ group }: { group: Group }) {
  const [value, setValue] = useState(() =>
    String(bigintToNumber(group.maxTurns)),
  );
  const [saved, setSaved] = useState(false);
  const save = useSetMaxTurns();
  const parsed = Number(value);
  const valid = value.trim() !== "" && Number.isInteger(parsed) && parsed >= 1;

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid) return;
    setSaved(false);
    save.mutate(
      { groupId: group.id, maxTurns: numberToBigint(parsed) },
      { onSuccess: () => setSaved(true) },
    );
  };

  return (
    <Section title="الحد الأقصى للأدوار">
      <form onSubmit={handleSubmit} className="flex items-center gap-2">
        <input
          type="number"
          inputMode="numeric"
          min={1}
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
            setSaved(false);
          }}
          aria-label="الحد الأقصى للأدوار"
          data-ocid="groupinfo.max_turns_input"
          className="admin-input min-h-11 min-w-0 flex-1 px-4 text-base focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring md:text-sm"
        />
        <button
          type="submit"
          data-ocid="groupinfo.max_turns_save_button"
          disabled={!valid || save.isPending}
          className="inline-flex min-h-11 items-center rounded-full bg-primary px-5 text-sm font-bold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50"
        >
          حفظ
        </button>
      </form>
      {!valid ? (
        <p className="text-xs text-muted-foreground">
          أدخل عدداً صحيحاً لا يقل عن ١.
        </p>
      ) : null}
      {save.isError ? (
        <p
          role="alert"
          data-ocid="groupinfo.max_turns_error_state"
          className="text-xs text-destructive"
        >
          تعذر حفظ الحد الأقصى للأدوار.
        </p>
      ) : null}
      {saved ? (
        <p
          data-ocid="groupinfo.max_turns_success_state"
          className="text-xs text-success"
        >
          تم الحفظ ✓
        </p>
      ) : null}
    </Section>
  );
}
