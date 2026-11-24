import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useCreateTool, useUpdateTool } from "@/hooks/useAdminQueries";
import type { Tool, ToolInput } from "@/lib/api";
import { cn } from "@/lib/utils";
import { AlertCircle, Key, Lock, ShieldCheck } from "lucide-react";
import { type FormEvent, useEffect, useState } from "react";

const fieldClass =
  "admin-input h-10 w-full min-w-0 px-3.5 text-sm outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-ring";

const DEFAULT_SCHEMA = JSON.stringify(
  {
    type: "object",
    properties: {},
  },
  null,
  2,
);

interface ToolFormDialogProps {
  tool: Tool | null; // null = create new, Tool = edit
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function ToolFormDialog({
  tool,
  open,
  onOpenChange,
}: ToolFormDialogProps) {
  const createMutation = useCreateTool();
  const updateMutation = useUpdateTool();
  const isEditing = tool !== null;

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [kind, setKind] = useState<"builtin" | "http_webhook">("http_webhook");
  const [timeoutMs, setTimeoutMs] = useState(10000);
  const [enabled, setEnabled] = useState(true);
  const [inputSchema, setInputSchema] = useState(DEFAULT_SCHEMA);

  // Webhook fields
  const [webhookUrl, setWebhookUrl] = useState("");
  const [webhookHeadersText, setWebhookHeadersText] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");

  // Modification trackers to protect existing encrypted secrets
  const [headersModified, setHeadersModified] = useState(false);
  const [secretModified, setSecretModified] = useState(false);

  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (tool) {
      setName(tool.name);
      setDescription(tool.description);
      setKind(tool.kind);
      setTimeoutMs(tool.timeoutMs);
      setEnabled(tool.enabled);
      setInputSchema(
        typeof tool.inputSchema === "string"
          ? tool.inputSchema
          : JSON.stringify(tool.inputSchema, null, 2),
      );
      setWebhookUrl(tool.webhookUrl || "");
      setWebhookHeadersText("");
      setWebhookSecret("");
      setHeadersModified(false);
      setSecretModified(false);
    } else {
      setName("");
      setDescription("");
      setKind("http_webhook");
      setTimeoutMs(10000);
      setEnabled(true);
      setInputSchema(DEFAULT_SCHEMA);
      setWebhookUrl("");
      setWebhookHeadersText("");
      setWebhookSecret("");
      setHeadersModified(false);
      setSecretModified(false);
    }
    setError(null);
  }, [tool]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setError(null);

    const trimmedName = name.trim();
    if (!/^[a-z0-9_]{1,64}$/.test(trimmedName)) {
      setError(
        "اسم الأداة يجب أن يكون بأحرف إنجليزية صغيرة وأرقام و_ فقط (snake_case).",
      );
      return;
    }

    if (timeoutMs < 500 || timeoutMs > 30000) {
      setError("مهلة التنفيذ يجب أن تكون بين 500 و 30000 ميلي ثانية.");
      return;
    }

    let parsedSchema: Record<string, unknown>;
    try {
      parsedSchema = JSON.parse(inputSchema);
      if (
        typeof parsedSchema !== "object" ||
        parsedSchema === null ||
        Array.isArray(parsedSchema)
      ) {
        setError("مخطط المدخلات (Schema) يجب أن يكون كائن JSON صحيح.");
        return;
      }
    } catch {
      setError("صيغة مخطط المدخلات (JSON Schema) غير صحيحة.");
      return;
    }

    let parsedHeaders: Record<string, string> | undefined;
    if (
      kind === "http_webhook" &&
      headersModified &&
      webhookHeadersText.trim()
    ) {
      try {
        const obj = JSON.parse(webhookHeadersText);
        if (typeof obj !== "object" || obj === null || Array.isArray(obj)) {
          setError("الترويسات (Headers) يجب أن تكون كائن JSON صالح.");
          return;
        }
        parsedHeaders = {};
        for (const [k, v] of Object.entries(obj)) {
          parsedHeaders[k] = String(v);
        }
      } catch {
        setError("صيغة الترويسات (JSON Headers) غير صحيحة.");
        return;
      }
    }

    if (kind === "http_webhook" && !webhookUrl.trim()) {
      setError("رابط الـ Webhook مطلوب لأدوات نوع http_webhook.");
      return;
    }

    if (isEditing && tool) {
      const payload: Partial<ToolInput> = {
        name: trimmedName,
        description: description.trim(),
        inputSchema: parsedSchema,
        timeoutMs,
        enabled,
      };

      if (kind === "http_webhook") {
        payload.webhookUrl = webhookUrl.trim();
        // NEVER send webhookHeaders or webhookSecret if unmodified
        if (headersModified) {
          payload.webhookHeaders = parsedHeaders || {};
        }
        if (secretModified) {
          payload.webhookSecret = webhookSecret;
        }
      }

      updateMutation.mutate(
        { id: tool.id, input: payload },
        {
          onSuccess: () => {
            onOpenChange(false);
          },
          onError: (err) => {
            setError(err.message || "تعذر حفظ تعديلات الأداة.");
          },
        },
      );
    } else {
      const payload: ToolInput = {
        name: trimmedName,
        description: description.trim(),
        kind,
        inputSchema: parsedSchema,
        timeoutMs,
        enabled,
        webhookUrl: kind === "http_webhook" ? webhookUrl.trim() : undefined,
        webhookHeaders: parsedHeaders,
        webhookSecret:
          secretModified && webhookSecret ? webhookSecret : undefined,
      };

      createMutation.mutate(payload, {
        onSuccess: () => {
          onOpenChange(false);
        },
        onError: (err) => {
          setError(err.message || "تعذر إنشاء الأداة.");
        },
      });
    }
  };

  const isSaving = createMutation.isPending || updateMutation.isPending;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        dir="rtl"
        className="max-h-[90vh] overflow-y-auto sm:max-w-xl"
      >
        <DialogHeader>
          <DialogTitle className="font-display text-lg">
            {isEditing ? `تعديل الأداة: ${tool.name}` : "إضافة أداة جديدة"}
          </DialogTitle>
          <DialogDescription>
            {isEditing
              ? "قم بتعديل إعدادات الأداة أو روابط الـ webhook."
              : "عرّف أداة جديدة يمكن للبوتات استدعاؤها أثناء دورها."}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          {error && (
            <div className="flex items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive">
              <AlertCircle className="size-4 shrink-0" />
              <span>{error}</span>
            </div>
          )}

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label className="block">
                <span className="mb-1 block text-xs font-semibold text-foreground">
                  اسم الأداة (snake_case)
                </span>
                <input
                  type="text"
                  dir="ltr"
                  disabled={isEditing && tool.kind === "builtin"}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="calculate"
                  className={cn(fieldClass, "font-mono")}
                  required
                />
              </label>
            </div>

            <div>
              <label className="block" htmlFor="tool-kind">
                <span className="mb-1 block text-xs font-semibold text-foreground">
                  نوع الأداة
                </span>
                <Select
                  id="tool-kind"
                  disabled={isEditing}
                  value={kind}
                  onChange={(e) =>
                    setKind(e.target.value as "builtin" | "http_webhook")
                  }
                  className={cn(fieldClass, "cursor-pointer")}
                >
                  <option value="http_webhook">HTTP Webhook (خارجي)</option>
                  <option
                    value="builtin"
                    disabled={!isEditing || tool?.kind !== "builtin"}
                  >
                    مدمجة في النظام (Built-in)
                  </option>
                </Select>
              </label>
            </div>
          </div>

          <div>
            <label className="block">
              <span className="mb-1 block text-xs font-semibold text-foreground">
                الوصف (يظهر للبوتات في التوجيه)
              </span>
              <input
                type="text"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="وصف مختصر لمهمة الأداة وكيفية استخدامها"
                className={fieldClass}
                required
              />
            </label>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div>
              <label className="block">
                <span className="mb-1 block text-xs font-semibold text-foreground">
                  مهلة التنفيذ (ميلي ثانية)
                </span>
                <input
                  type="number"
                  min={500}
                  max={30000}
                  step={500}
                  dir="ltr"
                  value={timeoutMs}
                  onChange={(e) => setTimeoutMs(Number(e.target.value))}
                  className={cn(fieldClass, "font-mono")}
                  required
                />
              </label>
            </div>

            <div className="flex items-center justify-between rounded-xl border border-border bg-secondary/50 px-3.5 py-2">
              <span className="text-xs font-semibold">تفعيل الأداة</span>
              <Switch checked={enabled} onCheckedChange={setEnabled} />
            </div>
          </div>

          {kind === "http_webhook" && (
            <div className="flex flex-col gap-3 rounded-xl border border-border bg-secondary/30 p-3.5">
              <h4 className="flex items-center gap-1.5 text-xs font-bold text-foreground">
                <ShieldCheck className="size-4 text-primary" />
                إعدادات الـ Webhook
              </h4>

              <div>
                <label className="block">
                  <span className="mb-1 block text-xs font-medium text-foreground">
                    رابط الـ Webhook (HTTPS)
                  </span>
                  <input
                    type="url"
                    dir="ltr"
                    value={webhookUrl}
                    onChange={(e) => setWebhookUrl(e.target.value)}
                    placeholder="https://example.com/api/tool"
                    className={cn(fieldClass, "font-mono")}
                    required={kind === "http_webhook"}
                  />
                </label>
              </div>

              <div>
                <label className="block">
                  <div className="mb-1 flex items-center justify-between">
                    <span className="text-xs font-medium text-foreground">
                      الترويسات الإضافية (JSON Headers)
                    </span>
                    {isEditing &&
                      tool?.hasWebhookHeaders &&
                      !headersModified && (
                        <span className="inline-flex items-center gap-1 rounded-full bg-primary/15 px-2 py-0.5 text-[10px] font-semibold text-primary">
                          <Lock className="size-3" />
                          توجد ترويسات مشفرة محفوظة
                        </span>
                      )}
                  </div>
                  <textarea
                    dir="ltr"
                    rows={2}
                    value={webhookHeadersText}
                    onChange={(e) => {
                      setWebhookHeadersText(e.target.value);
                      setHeadersModified(true);
                    }}
                    placeholder={
                      isEditing && tool?.hasWebhookHeaders && !headersModified
                        ? '{"Authorization": "Bearer ***"} (اتركه فارغاً للحفاظ على القيم المحفوظة)'
                        : '{"Authorization": "Bearer secret_token"}'
                    }
                    className="admin-input min-h-[60px] w-full p-2.5 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  />
                </label>
              </div>

              <div>
                <label className="block">
                  <div className="mb-1 flex items-center justify-between">
                    <span className="text-xs font-medium text-foreground">
                      سر توقيع الطلبات (HMAC Secret)
                    </span>
                    {isEditing && tool?.hasWebhookSecret && !secretModified && (
                      <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-[10px] font-semibold text-success">
                        <Key className="size-3" />
                        يوجد سر محفوظ ومشفّر
                      </span>
                    )}
                  </div>
                  <input
                    type="password"
                    dir="ltr"
                    value={webhookSecret}
                    onChange={(e) => {
                      setWebhookSecret(e.target.value);
                      setSecretModified(true);
                    }}
                    placeholder={
                      isEditing && tool?.hasWebhookSecret && !secretModified
                        ? "•••••••••••••••• (اتركه فارغاً للحفاظ على السر الحالي)"
                        : "أدخل سر HMAC جديد لتوقيع الـ payload"
                    }
                    className={cn(fieldClass, "font-mono")}
                  />
                </label>
              </div>
            </div>
          )}

          <div>
            <label className="block">
              <span className="mb-1 block text-xs font-semibold text-foreground">
                مخطط المدخلات (JSON Schema)
              </span>
              <textarea
                dir="ltr"
                rows={5}
                value={inputSchema}
                onChange={(e) => setInputSchema(e.target.value)}
                className="admin-input w-full p-3 font-mono text-xs leading-relaxed outline-none focus-visible:ring-2 focus-visible:ring-ring"
                required
              />
            </label>
          </div>

          <DialogFooter className="mt-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={isSaving}
            >
              إلغاء
            </Button>
            <Button type="submit" disabled={isSaving}>
              {isSaving
                ? "جارٍ الحفظ..."
                : isEditing
                  ? "حفظ التعديلات"
                  : "إنشاء الأداة"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
