import { useGroupDetail, useToolCallsAudit } from "@/hooks/useAdminQueries";
import { formatClockAr } from "@/lib/convert";
import { cn } from "@/lib/utils";
import { ChevronDown, ChevronUp, Clock, Terminal } from "lucide-react";
import { useState } from "react";

interface ToolAuditLogProps {
  groupId: number;
}

export default function ToolAuditLog({ groupId }: ToolAuditLogProps) {
  const { data: calls = [], isLoading } = useToolCallsAudit(groupId, 50);
  const { data: detail } = useGroupDetail(groupId);
  const [expandedCallId, setExpandedCallId] = useState<number | null>(null);

  const getBotName = (botId: string) => {
    const member = detail?.members.find((m) => m.botId === botId);
    return member?.name || botId;
  };

  const getStatusBadge = (status: string) => {
    switch (status) {
      case "success":
        return (
          <span className="rounded-full bg-success/15 px-2 py-0.5 text-[10px] font-semibold text-success">
            ناجح
          </span>
        );
      case "error":
        return (
          <span className="rounded-full bg-destructive/15 px-2 py-0.5 text-[10px] font-semibold text-destructive">
            خطأ
          </span>
        );
      case "cancelled":
        return (
          <span className="rounded-full bg-muted px-2 py-0.5 text-[10px] font-semibold text-muted-foreground">
            ملغى
          </span>
        );
      default:
        return (
          <span className="rounded-full bg-warning/15 px-2 py-0.5 text-[10px] font-semibold text-warning">
            قيد التنفيذ
          </span>
        );
    }
  };

  if (isLoading) {
    return (
      <div className="p-3 text-center text-xs text-muted-foreground">
        جارٍ تحميل سجل التدقيق...
      </div>
    );
  }

  if (calls.length === 0) {
    return (
      <div className="rounded-xl border border-border/50 bg-secondary/20 p-4 text-center text-xs text-muted-foreground">
        لا توجد استدعاءات أدوات مسجلة لهذه المجموعة بعد.
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="max-h-80 overflow-y-auto scrollbar-thin flex flex-col gap-2 pr-1">
        {calls.map((c) => {
          const isExpanded = expandedCallId === c.id;
          const botName = getBotName(c.botId);

          return (
            <div
              key={c.id}
              className="flex flex-col rounded-xl border border-border bg-card p-2.5 transition-colors"
            >
              <button
                type="button"
                onClick={() => setExpandedCallId(isExpanded ? null : c.id)}
                className="flex w-full cursor-pointer items-center justify-between gap-2 text-start"
              >
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-xs font-bold text-foreground">
                    <bdi>{botName}</bdi>
                  </span>
                  <span className="text-[11px] text-muted-foreground">←</span>
                  <span
                    dir="ltr"
                    className="font-mono text-xs font-semibold text-primary"
                  >
                    {c.toolName}
                  </span>
                  {getStatusBadge(c.status)}
                </div>

                <div className="flex items-center gap-2">
                  <span className="flex items-center gap-0.5 font-mono text-[10px] text-muted-foreground">
                    <Clock className="size-2.5" />
                    {c.durationMs}ms
                  </span>
                  <span className="text-[10px] text-muted-foreground">
                    {formatClockAr(c.createdAt)}
                  </span>
                  {isExpanded ? (
                    <ChevronUp className="size-3.5 text-muted-foreground" />
                  ) : (
                    <ChevronDown className="size-3.5 text-muted-foreground" />
                  )}
                </div>
              </button>

              {isExpanded && (
                <div className="mt-2.5 flex flex-col gap-2 border-t border-border/60 pt-2">
                  <div>
                    <span className="mb-0.5 block text-[10px] font-semibold text-muted-foreground">
                      معرف الاستدعاء (call_id)
                    </span>
                    <p
                      dir="ltr"
                      className="truncate font-mono text-[10px] text-foreground select-all"
                    >
                      {c.callId}
                    </p>
                  </div>

                  <div>
                    <span className="mb-0.5 block text-[10px] font-semibold text-muted-foreground">
                      المدخلات (Input)
                    </span>
                    <pre
                      dir="ltr"
                      className="max-h-36 overflow-y-auto rounded-lg border border-border bg-secondary/50 p-2 font-mono text-[10px] leading-relaxed text-foreground"
                    >
                      {JSON.stringify(c.arguments, null, 2)}
                    </pre>
                  </div>

                  {c.result && (
                    <div>
                      <span className="mb-0.5 block text-[10px] font-semibold text-muted-foreground">
                        المخرجات (Output)
                      </span>
                      <pre
                        dir="ltr"
                        className="max-h-36 overflow-y-auto rounded-lg border border-border bg-secondary/50 p-2 font-mono text-[10px] leading-relaxed text-foreground"
                      >
                        {(() => {
                          try {
                            return JSON.stringify(
                              JSON.parse(c.result),
                              null,
                              2,
                            );
                          } catch {
                            return c.result;
                          }
                        })()}
                      </pre>
                    </div>
                  )}

                  {c.errorMsg && (
                    <div>
                      <span className="mb-0.5 block text-[10px] font-semibold text-destructive">
                        الخطأ (Error)
                      </span>
                      <pre
                        dir="ltr"
                        className="max-h-24 overflow-y-auto rounded-lg border border-destructive/30 bg-destructive/10 p-2 font-mono text-[10px] text-destructive"
                      >
                        {c.errorMsg}
                      </pre>
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
