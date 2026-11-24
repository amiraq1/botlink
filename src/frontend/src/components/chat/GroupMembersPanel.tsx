import BotAvatar from "@/components/chat/BotAvatar";
import { Badge } from "@/components/ui/badge";
import type { GroupDetail } from "@/lib/api";
import { formatNumberAr, isMemberOnline } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { Settings2, Users } from "lucide-react";

/** Compact roster sidebar: presence, roles and a pulsing "their turn now" marker. */
export default function GroupMembersPanel({
  detail,
  onOpenInfo,
}: {
  detail: GroupDetail;
  onOpenInfo: () => void;
}) {
  const { group, members } = detail;
  const online = members.filter(isMemberOnline).length;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex items-center justify-between gap-2 border-b border-border-subtle px-4 py-3">
        <h2 className="flex items-center gap-1.5 text-xs font-bold text-foreground-secondary">
          <Users aria-hidden="true" className="size-3.5" />
          الأعضاء
          <span className="font-mono text-2xs font-normal text-muted-foreground">
            {formatNumberAr(members.length)}
          </span>
        </h2>
        <span className="flex items-center gap-1.5 text-2xs text-success">
          <span className="size-1.5 rounded-full bg-success" />
          {formatNumberAr(online)} متصل
        </span>
      </header>

      <ul
        data-ocid="groupinfo.members_sidebar"
        className="scrollbar-thin flex min-h-0 flex-1 flex-col gap-1 overflow-y-auto p-2"
      >
        {members.map((member, index) => {
          const isCurrent = group.currentTurnBotId === member.botId;
          const onlineMember = isMemberOnline(member);
          return (
            <li
              key={member.botId}
              data-ocid={`groupinfo.member_chip.${index + 1}`}
              className={cn(
                "flex items-center gap-2.5 rounded-md px-2 py-2 transition-colors duration-fast",
                isCurrent
                  ? "border border-primary/35 bg-primary/10"
                  : "border border-transparent hover:bg-secondary/60",
              )}
            >
              <BotAvatar
                name={member.name}
                accentColor={member.accentColor}
                size="sm"
                state={isCurrent ? "live" : onlineMember ? "idle" : "offline"}
              />
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-1 truncate text-xs font-bold text-foreground">
                  <bdi>{member.name}</bdi>
                  {member.isMock ? (
                    <span className="rounded-sm bg-secondary px-1 text-[9px] font-semibold text-muted-foreground">
                      تجريبي
                    </span>
                  ) : null}
                </p>
                <p className="truncate text-2xs text-muted-foreground">
                  <bdi>{member.roleTitle || "بدون دور محدد"}</bdi>
                </p>
              </div>
              {isCurrent ? (
                <Badge tone="primary" size="sm" dot pulse>
                  دوره الآن
                </Badge>
              ) : (
                <span
                  aria-label={onlineMember ? "متصل" : "غير متصل"}
                  className={cn(
                    "size-1.5 shrink-0 rounded-full",
                    onlineMember ? "bg-success" : "bg-muted-foreground/40",
                  )}
                />
              )}
            </li>
          );
        })}
      </ul>

      <div className="border-t border-border-subtle p-2">
        <button
          type="button"
          data-ocid="groupinfo.open_manage_button"
          onClick={onOpenInfo}
          className="flex w-full items-center justify-center gap-1.5 rounded-md px-3 py-2 text-xs font-semibold text-muted-foreground transition-colors duration-fast hover:bg-secondary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <Settings2 aria-hidden="true" className="size-3.5" />
          إدارة الأعضاء والروابط
        </button>
      </div>
    </div>
  );
}
