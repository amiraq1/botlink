import BotAvatar from "@/components/chat/BotAvatar";
import { Badge } from "@/components/ui/badge";
import type { GroupDetail } from "@/lib/api";
import { formatNumberAr, isMemberOnline, turnModeLabelAr } from "@/lib/convert";
import { Info } from "lucide-react";

/** Intro card at the top of the stream: roster cluster, title, description, info button. */
export default function GroupIntroCard({
  detail,
  onOpenInfo,
}: {
  detail: GroupDetail;
  onOpenInfo: () => void;
}) {
  const { group, members } = detail;
  const description = group.description.trim() || group.topic.trim();

  return (
    <section
      data-ocid="groupchat.intro_card"
      className="mx-auto flex w-full max-w-md flex-col items-center gap-3 rounded-xl border border-border-subtle bg-surface/60 px-5 py-6 text-center"
    >
      <div className="flex items-center justify-center -space-x-3 rtl:space-x-reverse">
        {members.slice(0, 5).map((m) => (
          <BotAvatar
            key={m.botId}
            name={m.name}
            accentColor={m.accentColor}
            size="lg"
            state={isMemberOnline(m) ? "live" : "offline"}
          />
        ))}
      </div>
      <h2 className="font-display text-xl font-bold text-foreground">
        {group.name}
      </h2>
      <div className="flex flex-wrap items-center justify-center gap-1.5">
        <Badge tone="primary" size="sm">
          {turnModeLabelAr(group.turnMode)}
        </Badge>
        <Badge tone="neutral" size="sm">
          {formatNumberAr(members.length)} أعضاء
        </Badge>
      </div>
      {description ? (
        <p className="max-w-sm text-balance text-sm leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}
      <button
        type="button"
        data-ocid="groupchat.intro_info_button"
        onClick={onOpenInfo}
        className="inline-flex min-h-10 items-center gap-2 rounded-full border border-border bg-secondary px-4 text-sm font-semibold text-foreground transition-colors duration-fast hover:bg-elevated focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
      >
        <Info aria-hidden="true" className="size-4" />
        عرض معلومات المجموعة
      </button>
    </section>
  );
}
