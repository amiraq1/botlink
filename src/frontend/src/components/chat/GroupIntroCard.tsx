import type { GroupDetail } from "@/lib/api";
import BotAvatar from "@/components/chat/BotAvatar";
import { isMemberOnline } from "@/lib/convert";
import { Info } from "lucide-react";

/** Intro card at the top of the stream: robot cluster, title, description, info button. */
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
      className="admin-card mx-auto flex w-full max-w-md flex-col items-center gap-3 px-5 py-6 text-center"
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
      <h2 className="font-display text-xl font-bold">🤖 {group.name}</h2>
      {description ? (
        <p className="max-w-sm text-balance text-sm leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}
      <button
        type="button"
        data-ocid="groupchat.intro_info_button"
        onClick={onOpenInfo}
        className="inline-flex min-h-11 items-center gap-2 rounded-full border border-border bg-secondary px-5 text-sm font-semibold transition-colors hover:bg-accent focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
      >
        <Info aria-hidden="true" className="size-4" />
        عرض معلومات المجموعة
      </button>
    </section>
  );
}
