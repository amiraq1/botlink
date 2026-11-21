import type { GroupDetail } from "@/backend";
import { useSendLeaderMessage } from "@/hooks/useAdminQueries";
import { accentClass } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { SendHorizontal } from "lucide-react";
import { type FormEvent, useRef, useState } from "react";

/** Sticky composer: @mention chips, text input and Send. Disabled once the group has ended. */
export default function Composer({
  detail,
  ended,
}: {
  detail: GroupDetail;
  ended: boolean;
}) {
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const send = useSendLeaderMessage();

  const insertMention = (name: string) => {
    setText((current) => {
      const spacer = current.length > 0 && !current.endsWith(" ") ? " " : "";
      return `${current}${spacer}@${name} `;
    });
    inputRef.current?.focus();
  };

  const handleSubmit = (event: FormEvent) => {
    event.preventDefault();
    const captured = text.trim();
    if (!captured || ended) return;
    setError(null);
    setText("");
    send.mutate(
      { groupId: detail.group.id, text: captured },
      {
        onError: () => {
          setError("تعذر إرسال الرسالة. حاول مرة أخرى.");
          setText((current) => (current === "" ? captured : current));
        },
      },
    );
  };

  return (
    <form
      onSubmit={handleSubmit}
      data-ocid="groupchat.composer"
      className="admin-composer shrink-0 px-3 pt-2.5 md:px-6"
    >
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-2">
        {detail.members.length > 0 ? (
          <div
            data-ocid="groupchat.mention_list"
            className="scrollbar-thin -mx-1 flex gap-2 overflow-x-auto px-1 pb-1"
          >
            {detail.members.map((m, i) => (
              <button
                key={m.botId}
                type="button"
                data-ocid={`groupchat.mention_chip.${i + 1}`}
                onClick={() => insertMention(m.name)}
                disabled={ended}
                className={cn(
                  accentClass(m.accentColor, m.name),
                  "mention-chip min-h-8 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-50",
                )}
              >
                @{m.name}
              </button>
            ))}
          </div>
        ) : null}
        {error ? (
          <p
            role="alert"
            data-ocid="groupchat.send_error_state"
            className="px-2 text-xs text-destructive"
          >
            {error}
          </p>
        ) : null}
        <div className="flex items-center gap-2">
          <input
            ref={inputRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            data-ocid="groupchat.message_input"
            aria-label="رسالة للفريق"
            disabled={ended}
            autoComplete="off"
            placeholder={
              ended
                ? "انتهت المحادثة ولا يمكن إرسال رسائل"
                : "اكتب رسالة للفريق أو استخدم @اسم_البوت..."
            }
            className="admin-input min-h-11 min-w-0 flex-1 px-4 text-base placeholder:text-muted-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:opacity-60 md:text-sm"
          />
          <button
            type="submit"
            data-ocid="groupchat.send_button"
            disabled={ended || text.trim().length === 0}
            className="inline-flex min-h-11 shrink-0 items-center gap-2 rounded-full bg-primary px-5 text-sm font-bold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50"
          >
            <span>إرسال</span>
            <SendHorizontal
              aria-hidden="true"
              className="size-4 rtl:-scale-x-100"
            />
          </button>
        </div>
      </div>
    </form>
  );
}
