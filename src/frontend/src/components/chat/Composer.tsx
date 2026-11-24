import { useSendLeaderMessage } from "@/hooks/useAdminQueries";
import type { GroupDetail } from "@/lib/api";
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

  const canSend = !ended && text.trim().length > 0 && !send.isPending;

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
            className="scrollbar-thin -mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1"
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
                  "mention-chip min-h-8 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:opacity-50",
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
            className="px-2 text-xs font-medium text-destructive"
          >
            {error}
          </p>
        ) : null}
        <div className="flex items-end gap-1.5 rounded-xl border border-border bg-elevated/50 p-1.5 transition-[border-color,box-shadow] duration-fast focus-within:border-ring/60 focus-within:ring-2 focus-within:ring-ring/20">
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
                : "اكتب رسالة للفريق أو استخدم @اسم_البوت…"
            }
            className="h-10 min-w-0 flex-1 bg-transparent px-2.5 text-base text-foreground outline-none placeholder:text-muted-foreground disabled:opacity-60 md:text-sm"
          />
          <button
            type="submit"
            data-ocid="groupchat.send_button"
            disabled={!canSend}
            aria-label="إرسال الرسالة"
            className="inline-flex h-10 shrink-0 items-center gap-2 rounded-lg bg-primary px-4 text-sm font-bold text-primary-foreground transition-[background-color,opacity] duration-fast hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-40"
          >
            <span className="hidden sm:inline">إرسال</span>
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
