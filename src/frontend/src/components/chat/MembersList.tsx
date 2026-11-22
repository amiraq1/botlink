import type { MemberView } from "@/lib/api";
import BotAvatar from "@/components/chat/BotAvatar";
import CopyButton from "@/components/chat/CopyButton";
import { useRemoveMember, useRotateMemberToken } from "@/hooks/useAdminQueries";
import { buildAgentPrompt, buildAgentUrl, isMemberOnline } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";

const MIN_MEMBERS = 2;

export default function MembersList({
  groupId,
  members,
}: {
  groupId: number;
  members: MemberView[];
}) {
  const remove = useRemoveMember();
  const rotate = useRotateMemberToken();
  const [confirmingBotId, setConfirmingBotId] = useState<string | null>(null);
  const canRemove = members.length > MIN_MEMBERS;

  return (
    <div className="flex flex-col gap-3">
      <ul data-ocid="groupinfo.members_list" className="flex flex-col gap-3">
        {members.map((m, i) => {
          const online = isMemberOnline(m);
          const url = buildAgentUrl(m.agentToken);
          return (
            <li
              key={m.botId}
              data-ocid={`groupinfo.member.${i + 1}`}
              className="admin-card flex flex-col gap-3 p-3"
            >
              <div className="flex items-center gap-3">
                <BotAvatar
                  name={m.name}
                  accentColor={m.accentColor}
                  state={online ? "live" : "offline"}
                />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-bold">
                    <bdi>{m.name}</bdi>
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    <bdi>{m.roleTitle}</bdi>
                    {m.isMock ? " · تجريبي" : ""}
                  </p>
                  <p
                    className={cn(
                      "mt-0.5 flex items-center gap-1.5 text-xs",
                      online ? "text-success" : "text-muted-foreground",
                    )}
                  >
                    <span
                      className={cn(
                        "size-2 rounded-full",
                        online ? "bg-success" : "bg-muted-foreground",
                      )}
                    />
                    {online ? "متصل الآن" : "غير متصل"}
                  </p>
                </div>
                <button
                  type="button"
                  data-ocid={`groupinfo.remove_member_button.${i + 1}`}
                  onClick={() => remove.mutate({ groupId, botId: m.botId })}
                  disabled={!canRemove || remove.isPending}
                  title={
                    canRemove
                      ? undefined
                      : "يجب أن تضم المجموعة عضوين على الأقل"
                  }
                  aria-label={`إزالة ${m.name}`}
                  className="inline-flex min-h-9 shrink-0 items-center gap-1.5 rounded-full border border-destructive/40 px-3 text-xs font-semibold text-destructive transition-colors hover:bg-destructive/15 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-40"
                >
                  <Trash2 aria-hidden="true" className="size-3.5" />
                  إزالة
                </button>
              </div>
              <div className="flex flex-wrap gap-2">
                <CopyButton
                  label="نسخ الرابط"
                  text={url}
                  ocid={`groupinfo.copy_url_button.${i + 1}`}
                />
                <CopyButton
                  label="نسخ التوجيه الجاهز"
                  text={buildAgentPrompt(url)}
                  ocid={`groupinfo.copy_prompt_button.${i + 1}`}
                />
                {confirmingBotId === m.botId ? (
                  <>
                    <button
                      type="button"
                      data-ocid={`groupinfo.confirm_rotate_button.${i + 1}`}
                      disabled={rotate.isPending}
                      onClick={() =>
                        rotate.mutate(
                          { groupId, botId: m.botId },
                          { onSuccess: () => setConfirmingBotId(null) },
                        )
                      }
                      className="inline-flex min-h-9 items-center gap-1.5 rounded-full bg-destructive px-3 text-xs font-semibold text-destructive-foreground transition-colors hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50"
                    >
                      تأكيد التدوير — الرابط القديم يتوقف
                    </button>
                    <button
                      type="button"
                      data-ocid={`groupinfo.cancel_rotate_button.${i + 1}`}
                      disabled={rotate.isPending}
                      onClick={() => setConfirmingBotId(null)}
                      className="inline-flex min-h-9 items-center rounded-full border border-border bg-secondary px-3 text-xs font-semibold transition-colors hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50"
                    >
                      إلغاء
                    </button>
                  </>
                ) : (
                  <button
                    type="button"
                    data-ocid={`groupinfo.rotate_token_button.${i + 1}`}
                    onClick={() => setConfirmingBotId(m.botId)}
                    className="inline-flex min-h-9 items-center gap-1.5 rounded-full border border-border bg-secondary px-3 text-xs font-semibold transition-colors hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                  >
                    <RefreshCw aria-hidden="true" className="size-3.5" />
                    تدوير الرابط
                  </button>
                )}
              </div>
              {rotate.isError && confirmingBotId === m.botId ? (
                <p
                  role="alert"
                  data-ocid={`groupinfo.rotate_error_state.${i + 1}`}
                  className="text-xs text-destructive"
                >
                  تعذر تدوير الرابط. حاول مرة أخرى.
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
      {!canRemove ? (
        <p className="text-xs text-muted-foreground">
          لا يمكن إزالة الأعضاء: يجب أن تضم المجموعة عضوين على الأقل.
        </p>
      ) : null}
      {remove.isError ? (
        <p
          role="alert"
          data-ocid="groupinfo.remove_error_state"
          className="text-xs text-destructive"
        >
          تعذرت إزالة العضو. حاول مرة أخرى.
        </p>
      ) : null}
    </div>
  );
}
