import { useAddMember } from "@/hooks/useAdminQueries";
import { BOT_ACCENT_PALETTE, paletteColor } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { UserPlus } from "lucide-react";
import { type FormEvent, useState } from "react";

const MAX_MEMBERS = 10;

const FIELD =
  "admin-input min-h-11 w-full px-4 text-base placeholder:text-muted-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-60 md:text-sm";

export default function AddMemberForm({
  groupId,
  memberCount,
  usedColors,
}: {
  groupId: number;
  memberCount: number;
  usedColors: string[];
}) {
  const add = useAddMember();
  const [name, setName] = useState("");
  const [roleTitle, setRoleTitle] = useState("");
  const [color, setColor] = useState<string | null>(null);
  const [isMock, setIsMock] = useState(false);

  const full = memberCount >= MAX_MEMBERS;
  const defaultColor =
    BOT_ACCENT_PALETTE.find((c) => !usedColors.includes(c.toUpperCase())) ??
    paletteColor(memberCount);
  const activeColor = color ?? defaultColor;
  const canSubmit = !full && name.trim() !== "" && roleTitle.trim() !== "";

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    const draft = {
      name: name.trim(),
      roleTitle: roleTitle.trim(),
      accentColor: activeColor,
      isMock,
    };
    setName("");
    setRoleTitle("");
    setColor(null);
    setIsMock(false);
    add.mutate(
      { groupId, ...draft },
      {
        onError: () => {
          setName((c) => (c === "" ? draft.name : c));
          setRoleTitle((c) => (c === "" ? draft.roleTitle : c));
          setIsMock(draft.isMock);
        },
      },
    );
  };

  return (
    <form
      onSubmit={handleSubmit}
      data-ocid="groupinfo.add_member_form"
      className="flex flex-col gap-3"
    >
      <label className="flex flex-col gap-1.5 text-xs font-semibold text-muted-foreground">
        الاسم
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={full}
          data-ocid="groupinfo.member_name_input"
          placeholder="مثال: Muse"
          autoComplete="off"
          className={FIELD}
        />
      </label>
      <label className="flex flex-col gap-1.5 text-xs font-semibold text-muted-foreground">
        المسمى الوظيفي
        <input
          value={roleTitle}
          onChange={(e) => setRoleTitle(e.target.value)}
          disabled={full}
          data-ocid="groupinfo.member_role_input"
          placeholder="مثال: كاتب المحتوى"
          autoComplete="off"
          className={FIELD}
        />
      </label>
      <fieldset className="flex flex-col gap-1.5" disabled={full}>
        <legend className="mb-1.5 text-xs font-semibold text-muted-foreground">
          لون التمييز
        </legend>
        <div
          className="flex flex-wrap gap-2"
          data-ocid="groupinfo.member_color_picker"
        >
          {BOT_ACCENT_PALETTE.map((c) => (
            <button
              key={c}
              type="button"
              aria-label={`اللون ${c}`}
              aria-pressed={activeColor.toUpperCase() === c}
              onClick={() => setColor(c)}
              style={{ backgroundColor: c }}
              className={cn(
                "size-8 rounded-full border-2 border-transparent transition-transform focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
                activeColor.toUpperCase() === c &&
                  "scale-110 border-foreground",
              )}
            />
          ))}
        </div>
      </fieldset>
      <label className="flex min-h-11 cursor-pointer items-center gap-2.5 text-sm">
        <input
          type="checkbox"
          checked={isMock}
          onChange={(e) => setIsMock(e.target.checked)}
          disabled={full}
          data-ocid="groupinfo.member_mock_checkbox"
          className="size-5 accent-primary"
        />
        شريك تجريبي (Mock Partner) يرد تلقائياً
      </label>
      {full ? (
        <p
          data-ocid="groupinfo.max_members_state"
          className="text-xs text-warning"
        >
          وصلت المجموعة إلى الحد الأقصى ({MAX_MEMBERS} أعضاء).
        </p>
      ) : null}
      {add.isError ? (
        <p
          role="alert"
          data-ocid="groupinfo.add_member_error_state"
          className="text-xs text-destructive"
        >
          تعذرت إضافة العضو. تحقق من البيانات وحاول مرة أخرى.
        </p>
      ) : null}
      <button
        type="submit"
        data-ocid="groupinfo.add_member_submit_button"
        disabled={!canSubmit || add.isPending}
        className="inline-flex min-h-11 items-center justify-center gap-2 rounded-full bg-primary px-5 text-sm font-bold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50"
      >
        <UserPlus aria-hidden="true" className="size-4" />
        إضافة عضو
      </button>
    </form>
  );
}
