import BotAvatar from "@/components/chat/BotAvatar";
import CopyButton from "@/components/chat/CopyButton";
import MemberToolsSection from "@/components/chat/MemberToolsSection";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import {
  useGroupMemberTools,
  useRemoveMember,
  useRotateMemberToken,
  useTools,
} from "@/hooks/useAdminQueries";
import type { MemberView } from "@/lib/api";
import { buildAgentPrompt, buildAgentUrl, isMemberOnline } from "@/lib/convert";
import { cn } from "@/lib/utils";
import {
  AlertTriangle,
  Link2,
  RefreshCw,
  ScrollText,
  Trash2,
  Wrench,
} from "lucide-react";
import { useState } from "react";

const MIN_MEMBERS = 2;

function CodeRow({
  label,
  icon,
  value,
  copyLabel,
  ocid,
  maxHeight,
}: {
  label: string;
  icon: React.ReactNode;
  value: string;
  copyLabel: string;
  ocid: string;
  maxHeight?: string;
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-1 text-2xs font-semibold text-muted-foreground">
          {icon}
          {label}
        </span>
        <CopyButton label={copyLabel} text={value} ocid={ocid} compact />
      </div>
      <p
        dir="ltr"
        className={cn(
          "code-block scrollbar-thin overflow-y-auto text-start",
          maxHeight,
        )}
      >
        {value}
      </p>
    </div>
  );
}

export default function MembersList({
  groupId,
  members,
}: {
  groupId: number;
  members: MemberView[];
}) {
  const remove = useRemoveMember();
  const rotate = useRotateMemberToken();
  const { toast } = useToast();
  const { data: allTools = [] } = useTools();
  const { data: memberTools = [] } = useGroupMemberTools(groupId);

  const [confirmingBotId, setConfirmingBotId] = useState<string | null>(null);
  const [expandedToolsBotId, setExpandedToolsBotId] = useState<string | null>(
    null,
  );
  const canRemove = members.length > MIN_MEMBERS;

  return (
    <div className="flex flex-col gap-3">
      <ul data-ocid="groupinfo.members_list" className="flex flex-col gap-3">
        {members.map((m, i) => {
          const online = isMemberOnline(m);
          const url = buildAgentUrl(m.agentToken);

          const botAssignedTools = memberTools.filter(
            (mt) => mt.botId === m.botId && mt.enabled,
          );
          const activeTools = botAssignedTools
            .map((mt) => {
              const t = allTools.find((x) => x.id === mt.toolId);
              return t?.enabled
                ? { name: t.name, description: t.description }
                : null;
            })
            .filter(
              (x): x is { name: string; description: string } => x !== null,
            );

          const confirming = confirmingBotId === m.botId;

          return (
            <li
              key={m.botId}
              data-ocid={`groupinfo.member.${i + 1}`}
              className={cn(
                "rounded-lg border bg-surface/50 p-3",
                confirming ? "border-destructive/50" : "border-border-subtle",
              )}
            >
              <div className="flex items-start gap-2.5">
                <BotAvatar
                  name={m.name}
                  accentColor={m.accentColor}
                  state={online ? "live" : "offline"}
                />
                <div className="min-w-0 flex-1">
                  <p className="flex items-center gap-1.5 truncate text-sm font-bold text-foreground">
                    <bdi>{m.name}</bdi>
                    {m.isMock ? (
                      <Badge tone="muted" size="sm">
                        تجريبي
                      </Badge>
                    ) : null}
                  </p>
                  <p className="truncate text-2xs text-muted-foreground">
                    <bdi>{m.roleTitle || "بدون دور محدد"}</bdi>
                  </p>
                  <p
                    className={cn(
                      "mt-1 flex items-center gap-1.5 text-2xs",
                      online ? "text-success" : "text-muted-foreground",
                    )}
                  >
                    <span
                      aria-hidden="true"
                      className={cn(
                        "size-1.5 rounded-full",
                        online ? "bg-success" : "bg-muted-foreground/50",
                      )}
                    />
                    {online ? "متصل الآن" : "غير متصل"}
                  </p>
                </div>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  data-ocid={`groupinfo.remove_member_button.${i + 1}`}
                  onClick={() => remove.mutate({ groupId, botId: m.botId })}
                  disabled={!canRemove || remove.isPending}
                  title={
                    canRemove
                      ? undefined
                      : "يجب أن تضم المجموعة عضوين على الأقل"
                  }
                  aria-label={`إزالة ${m.name}`}
                  className="text-muted-foreground hover:bg-destructive/15 hover:text-destructive"
                >
                  <Trash2 aria-hidden="true" className="size-4" />
                </Button>
              </div>

              <div className="mt-3 space-y-2.5">
                <CodeRow
                  label="رابط الوكيل"
                  icon={<Link2 aria-hidden="true" className="size-3" />}
                  value={url}
                  copyLabel="نسخ الرابط"
                  ocid={`groupinfo.copy_url_button.${i + 1}`}
                />
                <CodeRow
                  label="التوجيه الجاهز"
                  icon={<ScrollText aria-hidden="true" className="size-3" />}
                  value={buildAgentPrompt(url, activeTools)}
                  copyLabel="نسخ التوجيه الجاهز"
                  ocid={`groupinfo.copy_prompt_button.${i + 1}`}
                  maxHeight="max-h-24"
                />
              </div>

              <div className="mt-3 flex flex-wrap items-center gap-1.5">
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  data-ocid={`groupinfo.toggle_tools_button.${i + 1}`}
                  aria-expanded={expandedToolsBotId === m.botId}
                  onClick={() =>
                    setExpandedToolsBotId(
                      expandedToolsBotId === m.botId ? null : m.botId,
                    )
                  }
                  className={cn(
                    "border",
                    expandedToolsBotId === m.botId &&
                      "border-primary/40 bg-primary/10 text-primary",
                  )}
                >
                  <Wrench aria-hidden="true" className="size-3.5" />
                  الأدوات ({activeTools.length})
                </Button>
                {confirming ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    data-ocid={`groupinfo.cancel_rotate_button.${i + 1}`}
                    disabled={rotate.isPending}
                    onClick={() => setConfirmingBotId(null)}
                  >
                    إلغاء
                  </Button>
                ) : (
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    data-ocid={`groupinfo.rotate_token_button.${i + 1}`}
                    onClick={() => setConfirmingBotId(m.botId)}
                  >
                    <RefreshCw aria-hidden="true" className="size-3.5" />
                    تدوير الرابط
                  </Button>
                )}
              </div>

              {confirming ? (
                <div
                  data-ocid={`groupinfo.rotate_warning.${i + 1}`}
                  className="mt-2.5 space-y-2 rounded-md border border-destructive/35 bg-destructive/10 p-3"
                >
                  <p className="flex items-start gap-1.5 text-2xs font-medium leading-relaxed text-destructive">
                    <AlertTriangle
                      aria-hidden="true"
                      className="mt-0.5 size-3.5 shrink-0"
                    />
                    سيتم إبطال الرابط الحالي فوراً، وسيتوقف أي وكيل يستخدمه عن
                    العمل حتى تحصل على الرابط الجديد.
                  </p>
                  <Button
                    type="button"
                    variant="danger"
                    size="sm"
                    data-ocid={`groupinfo.confirm_rotate_button.${i + 1}`}
                    loading={rotate.isPending}
                    onClick={() =>
                      rotate.mutate(
                        { groupId, botId: m.botId },
                        {
                          onSuccess: () => {
                            setConfirmingBotId(null);
                            toast({
                              tone: "success",
                              title: "تم تدوير الرابط",
                              description: `أصبح رابط ${m.name} الجديد جاهزاً للنسخ.`,
                            });
                          },
                        },
                      )
                    }
                  >
                    تأكيد التدوير
                  </Button>
                </div>
              ) : null}

              {expandedToolsBotId === m.botId ? (
                <div className="mt-3 border-t border-border-subtle pt-3">
                  <MemberToolsSection groupId={groupId} botId={m.botId} />
                </div>
              ) : null}

              {rotate.isError && confirming ? (
                <p
                  role="alert"
                  data-ocid={`groupinfo.rotate_error_state.${i + 1}`}
                  className="mt-2 text-2xs text-destructive"
                >
                  تعذر تدوير الرابط. حاول مرة أخرى.
                </p>
              ) : null}
            </li>
          );
        })}
      </ul>
      {!canRemove ? (
        <p className="text-2xs text-muted-foreground">
          لا يمكن إزالة الأعضاء: يجب أن تضم المجموعة عضوين على الأقل.
        </p>
      ) : null}
      {remove.isError ? (
        <p
          role="alert"
          data-ocid="groupinfo.remove_error_state"
          className="text-2xs text-destructive"
        >
          تعذرت إزالة العضو. حاول مرة أخرى.
        </p>
      ) : null}
    </div>
  );
}
