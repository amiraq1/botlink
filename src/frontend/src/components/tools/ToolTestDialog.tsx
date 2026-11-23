import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useTestTool } from "@/hooks/useAdminQueries";
import type { Tool, ToolTestResult } from "@/lib/api";
import { cn } from "@/lib/utils";
import { AlertCircle, CheckCircle2, Clock, Play } from "lucide-react";
import { useEffect, useState } from "react";

interface ToolTestDialogProps {
  tool: Tool | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export default function ToolTestDialog({
  tool,
  open,
  onOpenChange,
}: ToolTestDialogProps) {
  const testMutation = useTestTool();
  const [inputText, setInputText] = useState("{}");
  const [response, setResponse] = useState<ToolTestResult | null>(null);

  useEffect(() => {
    if (tool) {
      // Set reasonable default based on tool name
      if (tool.name === "calculate") {
        setInputText(JSON.stringify({ expression: "12 * (3 + 4)" }, null, 2));
      } else if (tool.name === "get_time") {
        setInputText(JSON.stringify({ timezone: "Africa/Cairo" }, null, 2));
      } else {
        setInputText("{}");
      }
      setResponse(null);
    }
  }, [tool]);

  if (!tool) return null;

  const handleTest = async () => {
    let parsedInput: Record<string, unknown> = {};
    if (inputText.trim()) {
      try {
        const obj = JSON.parse(inputText);
        if (typeof obj !== "object" || obj === null || Array.isArray(obj)) {
          setResponse({
            ok: false,
            error: "مدخلات الاختبار يجب أن تكون كائن JSON صالح.",
            durationMs: 0,
          });
          return;
        }
        parsedInput = obj as Record<string, unknown>;
      } catch {
        setResponse({
          ok: false,
          error: "صيغة المدخلات (JSON) غير صحيحة.",
          durationMs: 0,
        });
        return;
      }
    }

    testMutation.mutate(
      { id: tool.id, input: parsedInput },
      {
        onSuccess: (data) => {
          setResponse(data);
        },
        onError: (err) => {
          setResponse({
            ok: false,
            error: err.message || "حدث خطأ أثناء فحص الأداة.",
            durationMs: 0,
          });
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        dir="rtl"
        className="max-h-[90vh] overflow-y-auto sm:max-w-xl"
      >
        <DialogHeader>
          <DialogTitle className="font-display text-lg">
            اختبار الأداة:{" "}
            <span className="font-mono text-primary">{tool.name}</span>
          </DialogTitle>
          <DialogDescription>
            أرسل مدخلات تجريبية للأداة للتحقق من سلامة استجابتها وسرعة التنفيذ.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-4">
          <div>
            <label className="block">
              <span className="mb-1 block text-xs font-semibold text-foreground">
                مدخلات الاختبار (JSON Input)
              </span>
              <textarea
                dir="ltr"
                rows={4}
                value={inputText}
                onChange={(e) => setInputText(e.target.value)}
                className="admin-input w-full p-3 font-mono text-xs leading-relaxed outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </label>
          </div>

          <div className="flex justify-end">
            <Button
              type="button"
              onClick={handleTest}
              disabled={testMutation.isPending}
              className="gap-2"
            >
              <Play className="size-3.5" />
              {testMutation.isPending ? "جارٍ الاختبار..." : "تنفيذ الاختبار"}
            </Button>
          </div>

          {response && (
            <div className="flex flex-col gap-2 rounded-xl border border-border bg-secondary/30 p-3.5">
              <div className="flex items-center justify-between">
                <span className="flex items-center gap-1.5 text-xs font-bold">
                  {response.ok ? (
                    <>
                      <CheckCircle2 className="size-4 text-success" />
                      <span className="text-success">نجح الاستدعاء</span>
                    </>
                  ) : (
                    <>
                      <AlertCircle className="size-4 text-destructive" />
                      <span className="text-destructive">فشل الاستدعاء</span>
                    </>
                  )}
                </span>
                <span className="flex items-center gap-1 font-mono text-xs text-muted-foreground">
                  <Clock className="size-3.5" />
                  {response.durationMs} ms
                </span>
              </div>

              <div>
                <span className="mb-1 block text-[11px] font-semibold text-muted-foreground">
                  النتيجة (Output)
                </span>
                <pre
                  dir="ltr"
                  className={cn(
                    "max-h-60 overflow-y-auto rounded-lg border p-3 font-mono text-xs leading-relaxed",
                    response.ok
                      ? "border-border bg-background text-foreground"
                      : "border-destructive/30 bg-destructive/10 text-destructive",
                  )}
                >
                  {response.ok
                    ? JSON.stringify(response.result, null, 2)
                    : response.error}
                </pre>
              </div>
            </div>
          )}
        </div>

        <DialogFooter className="mt-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => onOpenChange(false)}
          >
            إغلاق
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
