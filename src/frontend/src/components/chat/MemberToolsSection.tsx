import { Switch } from "@/components/ui/switch";
import {
  useGroupMemberTools,
  useSetMemberTool,
  useTools,
} from "@/hooks/useAdminQueries";
import { cn } from "@/lib/utils";
import { Wrench } from "lucide-react";

interface MemberToolsSectionProps {
  groupId: number;
  botId: string;
}

export default function MemberToolsSection({
  groupId,
  botId,
}: MemberToolsSectionProps) {
  const { data: allTools = [] } = useTools();
  const { data: memberTools = [] } = useGroupMemberTools(groupId);
  const setMemberToolMutation = useSetMemberTool();

  // Find tools configured for this bot
  const botMemberTools = memberTools.filter((mt) => mt.botId === botId);

  const getToolAssignment = (toolId: number) => {
    return botMemberTools.find((mt) => mt.toolId === toolId);
  };

  const handleToggle = (
    toolId: number,
    currentEnabled: boolean,
    maxCalls = 3,
  ) => {
    setMemberToolMutation.mutate({
      groupId,
      botId,
      toolId,
      enabled: !currentEnabled,
      maxCallsPerTurn: maxCalls,
    });
  };

  const handleMaxCallsChange = (
    toolId: number,
    enabled: boolean,
    newMaxCalls: number,
  ) => {
    const clamped = Math.max(1, Math.min(10, newMaxCalls));
    setMemberToolMutation.mutate({
      groupId,
      botId,
      toolId,
      enabled,
      maxCallsPerTurn: clamped,
    });
  };

  if (allTools.length === 0) {
    return (
      <p className="text-xs text-muted-foreground">
        لا توجد أدوات معرفة في النظام حالياً. يمكنك إضافتها من تبويب الأدوات في
        لوحة التحكم.
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2 rounded-xl border border-border/60 bg-secondary/30 p-2.5">
      <div className="flex items-center gap-1.5 text-xs font-semibold text-foreground">
        <Wrench className="size-3.5 text-primary" />
        <span>الأدوات المتاحة للبوت</span>
      </div>

      <div className="flex flex-col gap-2 divide-y divide-border/40">
        {allTools.map((tool) => {
          const assignment = getToolAssignment(tool.id);
          const isAssigned = assignment ? assignment.enabled : false;
          const maxCalls = assignment ? assignment.maxCallsPerTurn : 3;

          return (
            <div
              key={tool.id}
              className="flex items-center justify-between gap-2 pt-2 first:pt-0"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span
                    dir="ltr"
                    className="font-mono text-xs font-semibold text-foreground"
                  >
                    {tool.name}
                  </span>
                  {!tool.enabled && (
                    <span className="rounded bg-muted px-1.5 py-0.2 text-[9px] text-muted-foreground">
                      معطلة بالنظام
                    </span>
                  )}
                </div>
                <p className="line-clamp-1 text-[11px] text-muted-foreground">
                  {tool.description}
                </p>
              </div>

              <div className="flex items-center gap-2 shrink-0">
                {isAssigned && (
                  <div className="flex items-center gap-1 text-[11px] text-muted-foreground">
                    <span>السقف:</span>
                    <input
                      type="number"
                      min={1}
                      max={10}
                      value={maxCalls}
                      onChange={(e) =>
                        handleMaxCallsChange(
                          tool.id,
                          isAssigned,
                          Number(e.target.value),
                        )
                      }
                      className="admin-input h-6 w-12 px-1 text-center font-mono text-xs"
                      title="الحد الأقصى للاستدعاءات في كل دور"
                    />
                  </div>
                )}

                <Switch
                  checked={isAssigned}
                  disabled={!tool.enabled || setMemberToolMutation.isPending}
                  onCheckedChange={() =>
                    handleToggle(tool.id, isAssigned, maxCalls)
                  }
                  title={
                    isAssigned
                      ? "إلغاء تفعيل الأداة للبوت"
                      : "تفعيل الأداة للبوت"
                  }
                />
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
