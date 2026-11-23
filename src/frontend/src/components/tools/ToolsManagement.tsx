import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  useDeleteTool,
  useEnableTool,
  useTools,
} from "@/hooks/useAdminQueries";
import type { Tool } from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  Clock,
  Code2,
  Edit2,
  Globe,
  Key,
  Lock,
  Play,
  Plus,
  Trash2,
  Wrench,
} from "lucide-react";
import { useState } from "react";
import ToolFormDialog from "./ToolFormDialog";
import ToolTestDialog from "./ToolTestDialog";

export default function ToolsManagement() {
  const { data: tools = [], isLoading } = useTools();
  const enableMutation = useEnableTool();
  const deleteMutation = useDeleteTool();

  const [formDialogOpen, setFormDialogOpen] = useState(false);
  const [selectedToolForEdit, setSelectedToolForEdit] = useState<Tool | null>(
    null,
  );

  const [testDialogOpen, setTestDialogOpen] = useState(false);
  const [selectedToolForTest, setSelectedToolForTest] = useState<Tool | null>(
    null,
  );

  const [toolToDelete, setToolToDelete] = useState<Tool | null>(null);

  const handleCreateNew = () => {
    setSelectedToolForEdit(null);
    setFormDialogOpen(true);
  };

  const handleEdit = (tool: Tool) => {
    setSelectedToolForEdit(tool);
    setFormDialogOpen(true);
  };

  const handleTest = (tool: Tool) => {
    setSelectedToolForTest(tool);
    setTestDialogOpen(true);
  };

  const handleToggleEnable = (tool: Tool) => {
    enableMutation.mutate({ id: tool.id, enabled: !tool.enabled });
  };

  const handleDelete = () => {
    if (toolToDelete) {
      deleteMutation.mutate(toolToDelete.id, {
        onSuccess: () => setToolToDelete(null),
      });
    }
  };

  return (
    <div dir="rtl" className="flex flex-col gap-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <h2 className="flex items-center gap-2 font-display text-xl font-bold">
            <Wrench className="size-5 text-primary" />
            سجل الأدوات (Tool Registry)
          </h2>
          <p className="text-xs text-muted-foreground">
            إدارة الأدوات المدمجة والـ Webhooks التي يمكن للبوتات استدعاؤها
            أثناء دورها.
          </p>
        </div>

        <Button onClick={handleCreateNew} className="gap-2">
          <Plus className="size-4" />
          إضافة أداة جديدة
        </Button>
      </div>

      {isLoading ? (
        <div className="admin-card flex items-center justify-center p-12 text-sm text-muted-foreground">
          جارٍ تحميل الأدوات...
        </div>
      ) : tools.length === 0 ? (
        <div className="admin-card flex flex-col items-center justify-center gap-3 p-12 text-center">
          <Wrench className="size-8 text-muted-foreground/50" />
          <p className="text-sm font-medium text-foreground">
            لا توجد أدوات مسجلة حالياً
          </p>
          <p className="max-w-sm text-xs text-muted-foreground">
            أضف أدوات جديدة لتمكين البوتات من حساب الرياضيات، جلب الوقت، أو
            الاستعلام من خدمات خارجية.
          </p>
          <Button onClick={handleCreateNew} size="sm" className="mt-2 gap-2">
            <Plus className="size-3.5" />
            إضافة أداة
          </Button>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
          {tools.map((tool) => (
            <div
              key={tool.id}
              className={cn(
                "admin-card flex flex-col justify-between p-4 transition-colors hover:border-border/80",
                !tool.enabled && "opacity-75 bg-muted/20",
              )}
            >
              <div className="flex flex-col gap-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        dir="ltr"
                        className="font-mono text-sm font-bold text-foreground"
                      >
                        {tool.name}
                      </span>
                      <span
                        className={cn(
                          "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-semibold",
                          tool.kind === "builtin"
                            ? "bg-primary/15 text-primary"
                            : "bg-warning/15 text-warning",
                        )}
                      >
                        {tool.kind === "builtin" ? (
                          <>
                            <Code2 className="size-3" />
                            مدمجة (Built-in)
                          </>
                        ) : (
                          <>
                            <Globe className="size-3" />
                            Webhook
                          </>
                        )}
                      </span>
                      {tool.hasWebhookHeaders && (
                        <span
                          title="ترويسات مخصصة مشفرة"
                          className="inline-flex items-center gap-0.5 rounded-full bg-secondary px-1.5 py-0.5 text-[10px] text-muted-foreground"
                        >
                          <Lock className="size-2.5" />
                          ترويسات
                        </span>
                      )}
                      {tool.hasWebhookSecret && (
                        <span
                          title="سر HMAC مشفر"
                          className="inline-flex items-center gap-0.5 rounded-full bg-secondary px-1.5 py-0.5 text-[10px] text-muted-foreground"
                        >
                          <Key className="size-2.5" />
                          HMAC
                        </span>
                      )}
                    </div>
                    <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
                      {tool.description}
                    </p>
                  </div>

                  <div className="flex items-center gap-2">
                    <Switch
                      checked={tool.enabled}
                      onCheckedChange={() => handleToggleEnable(tool)}
                      title={tool.enabled ? "تعطيل الأداة" : "تفعيل الأداة"}
                    />
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-4 text-[11px] text-muted-foreground">
                  <span className="flex items-center gap-1 font-mono">
                    <Clock className="size-3" />
                    {tool.timeoutMs}ms
                  </span>
                  {tool.kind === "http_webhook" && tool.webhookUrl && (
                    <span
                      dir="ltr"
                      className="max-w-[200px] truncate font-mono text-[10px]"
                      title={tool.webhookUrl}
                    >
                      {tool.webhookUrl}
                    </span>
                  )}
                </div>
              </div>

              <div className="mt-4 flex items-center justify-between border-t border-border/50 pt-3">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => handleTest(tool)}
                  className="h-8 gap-1.5 text-xs"
                >
                  <Play className="size-3" />
                  اختبار
                </Button>

                <div className="flex items-center gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => handleEdit(tool)}
                    className="h-8 gap-1 text-xs"
                  >
                    <Edit2 className="size-3" />
                    تعديل
                  </Button>

                  {tool.kind !== "builtin" && (
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => setToolToDelete(tool)}
                      className="h-8 gap-1 text-xs text-destructive hover:bg-destructive/10 hover:text-destructive"
                    >
                      <Trash2 className="size-3" />
                      حذف
                    </Button>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Edit/Create Dialog */}
      <ToolFormDialog
        tool={selectedToolForEdit}
        open={formDialogOpen}
        onOpenChange={setFormDialogOpen}
      />

      {/* Test Dialog */}
      <ToolTestDialog
        tool={selectedToolForTest}
        open={testDialogOpen}
        onOpenChange={setTestDialogOpen}
      />

      {/* Delete Confirmation Alert Dialog */}
      <AlertDialog
        open={toolToDelete !== null}
        onOpenChange={(open) => !open && setToolToDelete(null)}
      >
        <AlertDialogContent dir="rtl">
          <AlertDialogHeader>
            <AlertDialogTitle>حذف الأداة</AlertDialogTitle>
            <AlertDialogDescription>
              هل أنت متأكد من حذف الأداة{" "}
              <strong className="font-mono text-foreground">
                {toolToDelete?.name}
              </strong>
              ؟ لن تتمكن البوتات من استدعائها بعد الآن.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>إلغاء</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              تأكيد الحذف
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
