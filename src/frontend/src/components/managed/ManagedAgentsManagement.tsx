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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";

import {
  useAgentRuns,
  useCancelAgentRun,
  useCheckLLMConnection,
  useCreateLLMConnection,
  useCreateManagedAgent,
  useDeleteLLMConnection,
  useDeleteManagedAgent,
  useDeleteModelPrice,
  useGroups,
  useLLMConnections,
  useManagedAgentUsage,
  useManagedAgents,
  useModelPrices,
  useRetryAgentRun,
  useSetModelPrice,
  useUpdateLLMConnection,
  useUpdateManagedAgent,
} from "@/hooks/useAdminQueries";
import type {
  AgentRun,
  ConnectionCheckResult,
  LLMConnection,
  LLMConnectionInput,
  ManagedAgent,
  ManagedAgentInput,
  ManagedAgentUsage,
  ModelPrice,
} from "@/lib/api";
import { cn } from "@/lib/utils";
import {
  Bot,
  CheckCircle2,
  CircleDollarSign,
  Key,
  Loader2,
  Play,
  Plus,
  RefreshCw,
  Trash2,
  Wifi,
  XCircle,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";

function microsToText(value: number | null | undefined) {
  if (value == null) return "—";
  return new Intl.NumberFormat("ar-IQ").format(value);
}

/** Micro-USD per 1M tokens to an exact decimal USD string (no rounding). */
function microsToUsd(micros: number): string {
  const whole = Math.floor(micros / 1_000_000);
  const frac = String(micros % 1_000_000)
    .padStart(6, "0")
    .replace(/0+$/, "");
  return frac ? `${whole}.${frac}` : String(whole);
}

const MAX_PRICE_MICROS = 1_000_000_000_000; // $1,000,000 per 1M tokens

/**
 * Parses USD-per-1M-tokens into micro-USD. Returns null when the text is not an
 * exact, non-negative amount with at most 6 decimals, so we never round a price
 * silently. Exponents, signs, commas and more than 6 decimals are rejected.
 */
function parseUsdToMicros(raw: string): number | null {
  const text = raw.trim();
  if (!/^\d+(\.\d{1,6})?$/.test(text)) return null;
  const [whole, frac = ""] = text.split(".");
  const micros = Number(whole) * 1_000_000 + Number(frac.padEnd(6, "0"));
  if (!Number.isSafeInteger(micros) || micros > MAX_PRICE_MICROS) return null;
  return micros;
}

const MODEL_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$/;

export default function ManagedAgentsManagement() {
  const {
    data: connections = [],
    isLoading: connectionsLoading,
    isError: connectionsError,
    refetch: refetchConnections,
  } = useLLMConnections();
  const { data: groups = [] } = useGroups();
  const createConn = useCreateLLMConnection();
  const updateConn = useUpdateLLMConnection();
  const deleteConn = useDeleteLLMConnection();
  const checkConn = useCheckLLMConnection();

  const [selectedGroupId, setSelectedGroupId] = useState<number | null>(null);
  const [connDialogOpen, setConnDialogOpen] = useState(false);
  const [editingConn, setEditingConn] = useState<LLMConnection | null>(null);
  const [connForm, setConnForm] = useState<LLMConnectionInput>({
    provider: "deepseek",
    label: "",
    apiKey: "",
    modelHint: "deepseek-flash",
  });
  const [connToDelete, setConnToDelete] = useState<LLMConnection | null>(null);
  const [pricesConn, setPricesConn] = useState<LLMConnection | null>(null);
  const pricesTriggerRef = useRef<HTMLButtonElement | null>(null);
  const [checkResults, setCheckResults] = useState<
    Record<number, ConnectionCheckResult>
  >({});
  const [checkingId, setCheckingId] = useState<number | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const [agentDialogOpen, setAgentDialogOpen] = useState(false);
  const [editingAgent, setEditingAgent] = useState<ManagedAgent | null>(null);
  const [agentForm, setAgentForm] = useState<ManagedAgentInput>({
    connectionId: 0,
    model: "deepseek-flash",
    persona: "",
    maxOutputTokens: 1024,
    contextMaxMessages: 30,
    contextMaxChars: 12000,
    dailyRunLimit: 50,
    dailyBudgetMicros: null,
    enabled: true,
  });
  const [agentToDelete, setAgentToDelete] = useState<ManagedAgent | null>(null);
  const [agentForRuns, setAgentForRuns] = useState<ManagedAgent | null>(null);

  const {
    data: agents = [],
    isLoading: agentsLoading,
    isError: agentsError,
    refetch: refetchAgents,
  } = useManagedAgents(selectedGroupId);
  const createAgent = useCreateManagedAgent();
  const updateAgent = useUpdateManagedAgent();
  const deleteAgent = useDeleteManagedAgent();

  useEffect(() => {
    if (selectedGroupId === null && groups.length > 0) {
      setSelectedGroupId(groups[0].id);
    }
  }, [groups, selectedGroupId]);

  function openCreateConn() {
    setEditingConn(null);
    setConnForm({
      provider: "deepseek",
      label: "",
      apiKey: "",
      modelHint: "deepseek-flash",
    });
    setConnDialogOpen(true);
  }

  function openEditConn(conn: LLMConnection) {
    setEditingConn(conn);
    setConnForm({
      provider: conn.provider as "deepseek",
      label: conn.label,
      apiKey: "",
      modelHint: conn.modelHint,
    });
    setConnDialogOpen(true);
  }

  async function saveConn() {
    setActionError(null);
    try {
      if (editingConn) {
        await updateConn.mutateAsync({ id: editingConn.id, input: connForm });
      } else {
        await createConn.mutateAsync(connForm);
      }
      // Drop the key from component state as soon as it is stored server-side.
      setConnForm((prev) => ({ ...prev, apiKey: "" }));
      setConnDialogOpen(false);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "تعذّر حفظ الاتصال.");
    }
  }

  async function runConnectionCheck(id: number) {
    setActionError(null);
    setCheckingId(id);
    try {
      const result = await checkConn.mutateAsync(id);
      setCheckResults((prev) => ({ ...prev, [id]: result }));
    } catch (err) {
      setActionError(
        err instanceof Error ? err.message : "تعذّر اختبار الاتصال.",
      );
    } finally {
      setCheckingId(null);
    }
  }

  function openCreateAgent() {
    setEditingAgent(null);
    setAgentForm({
      connectionId: connections[0]?.id ?? 0,
      model: "deepseek-flash",
      persona: "",
      maxOutputTokens: 1024,
      contextMaxMessages: 30,
      contextMaxChars: 12000,
      dailyRunLimit: 50,
      dailyBudgetMicros: null,
      enabled: true,
    });
    setAgentDialogOpen(true);
  }

  function openEditAgent(agent: ManagedAgent) {
    setEditingAgent(agent);
    setAgentForm({
      connectionId: agent.connectionId,
      model: agent.model,
      persona: agent.persona,
      maxOutputTokens: agent.maxOutputTokens,
      contextMaxMessages: agent.contextMaxMessages,
      contextMaxChars: agent.contextMaxChars,
      dailyRunLimit: agent.dailyRunLimit,
      dailyBudgetMicros: agent.dailyBudgetMicros ?? null,
      enabled: agent.enabled,
    });
    setAgentDialogOpen(true);
  }

  async function saveAgent() {
    if (selectedGroupId === null) return;
    setActionError(null);
    try {
      if (editingAgent) {
        await updateAgent.mutateAsync({
          groupId: selectedGroupId,
          botId: editingAgent.botId,
          agent: agentForm,
        });
      } else {
        const name =
          (
            document.getElementById("agent-name") as HTMLInputElement | null
          )?.value?.trim() || "وكيل مُدار";
        const roleTitle =
          (
            document.getElementById("agent-role") as HTMLInputElement | null
          )?.value?.trim() || "مساعد";
        await createAgent.mutateAsync({
          groupId: selectedGroupId,
          member: { name, roleTitle },
          agent: agentForm,
        });
      }
      setAgentDialogOpen(false);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "تعذّر حفظ الوكيل.");
    }
  }

  return (
    <div dir="rtl" className="flex flex-col gap-6">
      {/* Connections */}
      {actionError ? (
        <p
          role="alert"
          data-ocid="managed.action_error"
          className="rounded-md border border-destructive/25 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {actionError}
        </p>
      ) : null}

      <section className="flex flex-col gap-4">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 className="flex items-center gap-2 font-display text-xl font-bold">
              <Key className="size-5 text-primary" />
              اتصالات المزوّد (DeepSeek)
            </h2>
            <p className="text-xs text-muted-foreground">
              أنشئ اتصالاً، أدخل المفتاح (لا يُعرض بعد الحفظ)، واختبره. نجاح
              اختبار النماذج لا يضمن نجاح التوليد أو توفر الرصيد.
            </p>
          </div>
          <Button onClick={openCreateConn} className="gap-2">
            <Plus className="size-4" />
            إضافة اتصال
          </Button>
        </div>

        {connectionsLoading ? (
          <Skeleton className="h-24 w-full rounded-lg" />
        ) : connectionsError ? (
          <div
            role="alert"
            className="admin-card flex flex-col items-center gap-2 p-6 text-center text-sm text-destructive"
          >
            تعذّر تحميل الاتصالات.
            <Button
              variant="outline"
              size="sm"
              onClick={() => refetchConnections()}
            >
              إعادة المحاولة
            </Button>
          </div>
        ) : connections.length === 0 ? (
          <EmptyState
            icon={<Key />}
            title="لا توجد اتصالات"
            description="أضف اتصال DeepSeek لتمكين الوكلاء المُدارين."
          />
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {connections.map((conn) => (
              <div key={conn.id} className="admin-card flex flex-col gap-3 p-4">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <p className="font-semibold text-foreground">
                      {conn.label}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {conn.provider} · {conn.modelHint || "—"}
                    </p>
                    <p className="mt-1 text-[11px] text-muted-foreground">
                      {conn.hasKey
                        ? `مفتاح محفوظ (…${conn.keyLast4})`
                        : "لا يوجد مفتاح"}
                    </p>
                  </div>
                  <span
                    className={cn(
                      "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-semibold",
                      conn.lastCheckStatus === "ok"
                        ? "bg-success/15 text-success"
                        : conn.lastCheckStatus === "error"
                          ? "bg-destructive/15 text-destructive"
                          : "bg-secondary text-muted-foreground",
                    )}
                  >
                    {conn.lastCheckStatus === "ok" ? (
                      <CheckCircle2 className="size-3" />
                    ) : conn.lastCheckStatus === "error" ? (
                      <XCircle className="size-3" />
                    ) : (
                      <Wifi className="size-3" />
                    )}
                    {conn.lastCheckStatus === "ok"
                      ? "ناجح"
                      : conn.lastCheckStatus === "error"
                        ? "فشل"
                        : "لم يُختبر"}
                  </span>
                </div>
                {conn.lastCheckError && (
                  <p className="text-xs text-destructive">
                    {conn.lastCheckError}
                  </p>
                )}
                {checkResults[conn.id]?.models?.length ? (
                  <div className="flex flex-col gap-1 text-[11px] text-muted-foreground">
                    <span>النماذج المتاحة من المزوّد:</span>
                    <div className="flex flex-wrap gap-1">
                      {checkResults[conn.id].models.map((model) => (
                        <span
                          key={model.id}
                          className="rounded-full bg-secondary px-2 py-0.5 font-mono text-[10px] text-secondary-foreground"
                        >
                          {model.id}
                        </span>
                      ))}
                    </div>
                  </div>
                ) : null}
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => runConnectionCheck(conn.id)}
                    disabled={checkingId !== null}
                    className="gap-1.5 text-xs"
                  >
                    {checkingId === conn.id ? (
                      <Loader2 className="size-3 animate-spin" />
                    ) : (
                      <Play className="size-3" />
                    )}
                    اختبار
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={(event) => {
                      pricesTriggerRef.current = event.currentTarget;
                      setPricesConn(conn);
                    }}
                    className="gap-1.5 text-xs"
                  >
                    <CircleDollarSign className="size-3" />
                    الأسعار
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => openEditConn(conn)}
                    className="gap-1.5 text-xs"
                  >
                    تعديل
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setConnToDelete(conn)}
                    className="gap-1.5 text-xs text-destructive hover:bg-destructive/10"
                  >
                    <Trash2 className="size-3" />
                    حذف
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      {/* Managed agents */}
      <section className="flex flex-col gap-4">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <h2 className="flex items-center gap-2 font-display text-xl font-bold">
              <Bot className="size-5 text-primary" />
              الوكلاء المُدارون
            </h2>
            <p className="text-xs text-muted-foreground">
              «مفعّل للتوجيه» يختلف عن «اتصال المزوّد ناجح»؛ حالة الاتصال تُعرض فقط
              بعد اختبار. التكلفة المحسوبة تقديرية، وبلوغ سقف الميزانية يمنع
              التشغيل محلياً ولا يضمن قيمة الفاتورة النهائية لدى المزوّد.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Select
              className="w-[220px]"
              value={selectedGroupId ? String(selectedGroupId) : ""}
              onChange={(e) => setSelectedGroupId(Number(e.target.value))}
            >
              {groups.map((g) => (
                <option key={g.id} value={String(g.id)}>
                  {g.name}
                </option>
              ))}
            </Select>
            <Button
              onClick={openCreateAgent}
              className="gap-2"
              disabled={selectedGroupId === null || connections.length === 0}
            >
              <Plus className="size-4" />
              إضافة وكيل
            </Button>
          </div>
        </div>

        {agentsLoading ? (
          <Skeleton className="h-24 w-full rounded-lg" />
        ) : agentsError ? (
          <div
            role="alert"
            className="admin-card flex flex-col items-center gap-2 p-6 text-center text-sm text-destructive"
          >
            تعذّر تحميل الوكلاء المُدارين.
            <Button variant="outline" size="sm" onClick={() => refetchAgents()}>
              إعادة المحاولة
            </Button>
          </div>
        ) : agents.length === 0 ? (
          <EmptyState
            icon={<Bot />}
            title="لا يوجد وكلاء مُدارون في هذه المجموعة"
            description="أضف وكيلاً مُداراً واربطه بأحد اتصالات المزوّد."
          />
        ) : (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            {agents.map((agent) => (
              <AgentCard
                key={agent.botId}
                agent={agent}
                groupId={selectedGroupId!}
                onEdit={() => openEditAgent(agent)}
                onDelete={() => setAgentToDelete(agent)}
                onRuns={() => setAgentForRuns(agent)}
              />
            ))}
          </div>
        )}
      </section>

      {/* Connection dialog */}
      <Dialog open={connDialogOpen} onOpenChange={setConnDialogOpen}>
        <DialogContent dir="rtl">
          <DialogHeader>
            <DialogTitle>
              {editingConn ? "تعديل اتصال" : "اتصال جديد"}
            </DialogTitle>
            <DialogDescription>
              المفتاح لا يُعرض بعد الحفظ. اتركه فارغاً في التعديل للإبقاء على
              المفتاح الحالي.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Input
              placeholder="اسم الاتصال"
              value={connForm.label}
              onChange={(e) =>
                setConnForm({ ...connForm, label: e.target.value })
              }
            />
            <Select
              value={connForm.provider}
              onChange={(e) =>
                setConnForm({
                  ...connForm,
                  provider: e.target.value as "deepseek",
                })
              }
            >
              <option value="deepseek">DeepSeek</option>
            </Select>
            <Input
              type="password"
              placeholder="مفتاح API"
              value={connForm.apiKey}
              onChange={(e) =>
                setConnForm({ ...connForm, apiKey: e.target.value })
              }
              autoComplete="off"
            />
            <Input
              placeholder="نموذج افتراضي (deepseek-flash)"
              value={connForm.modelHint}
              onChange={(e) =>
                setConnForm({ ...connForm, modelHint: e.target.value })
              }
            />
          </div>
          <DialogFooter>
            <Button
              onClick={saveConn}
              disabled={createConn.isPending || updateConn.isPending}
            >
              حفظ
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Agent dialog */}
      <Dialog open={agentDialogOpen} onOpenChange={setAgentDialogOpen}>
        <DialogContent dir="rtl" className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {editingAgent ? "تعديل وكيل" : "وكيل جديد"}
            </DialogTitle>
          </DialogHeader>
          {!editingAgent && (
            <div className="flex flex-col gap-3">
              <Input id="agent-name" placeholder="اسم البوت" />
              <Input id="agent-role" placeholder="عنوان الدور" />
            </div>
          )}
          <div className="flex flex-col gap-3">
            <Select
              value={String(agentForm.connectionId)}
              onChange={(e) =>
                setAgentForm({
                  ...agentForm,
                  connectionId: Number(e.target.value),
                })
              }
            >
              {connections.map((c) => (
                <option key={c.id} value={String(c.id)}>
                  {c.label}
                </option>
              ))}
            </Select>
            <Input
              placeholder="النموذج (deepseek-flash)"
              value={agentForm.model}
              onChange={(e) =>
                setAgentForm({ ...agentForm, model: e.target.value })
              }
            />
            <textarea
              className="min-h-[80px] w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
              placeholder="Persona (التعليمات)"
              value={agentForm.persona}
              onChange={(e) =>
                setAgentForm({ ...agentForm, persona: e.target.value })
              }
            />
            <div className="grid grid-cols-2 gap-2">
              <Input
                type="number"
                placeholder="maxOutputTokens"
                value={agentForm.maxOutputTokens}
                onChange={(e) =>
                  setAgentForm({
                    ...agentForm,
                    maxOutputTokens: Number(e.target.value),
                  })
                }
              />
              <Input
                type="number"
                placeholder="contextMaxMessages"
                value={agentForm.contextMaxMessages}
                onChange={(e) =>
                  setAgentForm({
                    ...agentForm,
                    contextMaxMessages: Number(e.target.value),
                  })
                }
              />
              <Input
                type="number"
                placeholder="contextMaxChars"
                value={agentForm.contextMaxChars}
                onChange={(e) =>
                  setAgentForm({
                    ...agentForm,
                    contextMaxChars: Number(e.target.value),
                  })
                }
              />
              <Input
                type="number"
                placeholder="dailyRunLimit"
                value={agentForm.dailyRunLimit}
                onChange={(e) =>
                  setAgentForm({
                    ...agentForm,
                    dailyRunLimit: Number(e.target.value),
                  })
                }
              />
            </div>
            <Input
              type="number"
              placeholder="dailyBudgetMicros (اختياري)"
              value={agentForm.dailyBudgetMicros ?? ""}
              onChange={(e) =>
                setAgentForm({
                  ...agentForm,
                  dailyBudgetMicros:
                    e.target.value === "" ? null : Number(e.target.value),
                })
              }
            />
            <div className="flex items-center gap-2">
              <Switch
                checked={agentForm.enabled}
                onCheckedChange={(v) =>
                  setAgentForm({ ...agentForm, enabled: v })
                }
              />
              <span className="text-sm">مفعّل للتوجيه</span>
            </div>
          </div>
          <DialogFooter>
            <Button
              onClick={saveAgent}
              disabled={createAgent.isPending || updateAgent.isPending}
            >
              حفظ
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete connection */}
      <AlertDialog
        open={connToDelete !== null}
        onOpenChange={(o) => !o && setConnToDelete(null)}
      >
        <AlertDialogContent dir="rtl">
          <AlertDialogHeader>
            <AlertDialogTitle>حذف الاتصال</AlertDialogTitle>
            <AlertDialogDescription>
              حذف الاتصال {connToDelete?.label}؟
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>إلغاء</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (connToDelete) {
                  setActionError(null);
                  deleteConn.mutate(connToDelete.id, {
                    onSuccess: () => setConnToDelete(null),
                    onError: (err) => {
                      setConnToDelete(null);
                      setActionError(
                        err instanceof Error
                          ? err.message
                          : "تعذّر حذف الاتصال.",
                      );
                    },
                  });
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              تأكيد
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Delete agent */}
      <AlertDialog
        open={agentToDelete !== null}
        onOpenChange={(o) => !o && setAgentToDelete(null)}
      >
        <AlertDialogContent dir="rtl">
          <AlertDialogHeader>
            <AlertDialogTitle>حذف الوكيل</AlertDialogTitle>
            <AlertDialogDescription>
              حذف {agentToDelete?.botName}؟ يتطلب تأكيد.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>إلغاء</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (agentToDelete && selectedGroupId !== null) {
                  setActionError(null);
                  deleteAgent.mutate(
                    { groupId: selectedGroupId, botId: agentToDelete.botId },
                    {
                      onSuccess: () => setAgentToDelete(null),
                      onError: (err) => {
                        setAgentToDelete(null);
                        setActionError(
                          err instanceof Error
                            ? err.message
                            : "تعذّر حذف الوكيل.",
                        );
                      },
                    },
                  );
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              تأكيد الحذف
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Runs dialog */}
      <Dialog
        open={agentForRuns !== null}
        onOpenChange={(o) => !o && setAgentForRuns(null)}
      >
        <DialogContent dir="rtl" className="max-h-[90vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>سجل التشغيلات — {agentForRuns?.botName}</DialogTitle>
          </DialogHeader>
          {agentForRuns && selectedGroupId !== null && (
            <RunsPanel groupId={selectedGroupId} agent={agentForRuns} />
          )}
        </DialogContent>
      </Dialog>

      {/* Model prices dialog */}
      <Dialog
        open={pricesConn !== null}
        onOpenChange={(o) => !o && setPricesConn(null)}
      >
        <DialogContent
          dir="rtl"
          className="max-h-[90vh] overflow-y-auto"
          onCloseAutoFocus={(event) => {
            event.preventDefault();
            window.setTimeout(() => pricesTriggerRef.current?.focus(), 0);
          }}
        >
          <DialogHeader>
            <DialogTitle>أسعار النماذج — {pricesConn?.label}</DialogTitle>
            <DialogDescription>
              الأسعار يدخلها الأدمن وقد لا تطابق فاتورة المزوّد؛ تُستخدم لتقدير
              التكلفة وفحص سقف الميزانية فقط. الوحدة: دولار أمريكي (USD) لكل
              1,000,000 توكن، إدخال وإخراج منفصلان.
            </DialogDescription>
          </DialogHeader>
          {pricesConn && <PricesPanel connection={pricesConn} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function AgentCard({
  agent,
  groupId,
  onEdit,
  onDelete,
  onRuns,
}: {
  agent: ManagedAgent;
  groupId: number;
  onEdit: () => void;
  onDelete: () => void;
  onRuns: () => void;
}) {
  const { data: usage } = useManagedAgentUsage(groupId, agent.botId);
  const { data: prices } = useModelPrices(agent.connectionId);
  const modelPrice = prices?.find((p) => p.model === agent.model);
  return (
    <div className="admin-card flex flex-col gap-3 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="font-semibold text-foreground">{agent.botName}</p>
          <p className="text-xs text-muted-foreground">
            {agent.roleTitle} · {agent.provider}/{agent.model}
          </p>
        </div>
        <span
          className={cn(
            "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-semibold",
            agent.enabled
              ? "bg-success/15 text-success"
              : "bg-secondary text-muted-foreground",
          )}
        >
          {agent.enabled ? "مفعّل للتوجيه" : "معطّل"}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-2 text-[11px] text-muted-foreground">
        <span>
          تشغيلات 24h:{" "}
          {usage ? `${usage.runsLast24h}/${usage.dailyRunLimit}` : "—"}
        </span>
        <span>إنفاق: {usage ? microsToText(usage.spentMicros) : "—"}</span>
        <span>محسوب: {usage ? microsToText(usage.computedMicros) : "—"}</span>
        <span>
          سعر النموذج:{" "}
          {!prices
            ? "—"
            : modelPrice
              ? `$${microsToUsd(modelPrice.inputMicrosPer1M)} إدخال / $${microsToUsd(modelPrice.outputMicrosPer1M)} إخراج لكل 1M`
              : "غير مسجّل"}
        </span>
        <span>
          حالة الميزانية:{" "}
          {usage
            ? usage.budgetBlocked
              ? usage.budgetBlockReason === "cost_unknown"
                ? "مجهولة (ممنوع)"
                : "ممتلئة"
              : usage.costKnown
                ? "ضمن السقف"
                : "غير معروف"
            : "—"}
        </span>
      </div>
      {usage?.budgetBlocked && usage.budgetBlockReason === "cost_unknown" && (
        <p className="text-xs text-destructive">
          تكلفة مجهولة في نافذة 24 ساعة؛ المنع قد يستمر حتى خروج السجل من
          النافذة.
        </p>
      )}
      {usage?.budgetBlocked && usage.budgetBlockReason !== "cost_unknown" && (
        <p className="text-xs text-destructive">
          تم بلوغ سقف الميزانية اليومي؛ التشغيلات الجديدة ممنوعة حتى انخفاض
          الإنفاق أو تعديل السقف.
        </p>
      )}
      {prices && !modelPrice && (
        <p className="text-xs text-warning">
          لا يوجد سعر مسجّل لهذا النموذج؛ التكلفة تصبح مجهولة، وإذا ضُبط سقف
          ميزانية للوكيل فسيُمنع من التشغيل حتى يُسجَّل السعر.
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={onRuns}
          className="gap-1.5 text-xs"
        >
          <RefreshCw className="size-3" />
          سجل التشغيلات
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={onEdit}
          className="gap-1.5 text-xs"
        >
          تعديل
        </Button>
        <Button
          variant="ghost"
          size="sm"
          onClick={onDelete}
          className="gap-1.5 text-xs text-destructive hover:bg-destructive/10"
        >
          <Trash2 className="size-3" />
          حذف
        </Button>
      </div>
    </div>
  );
}

function PricesPanel({ connection }: { connection: LLMConnection }) {
  const {
    data: prices = [],
    isLoading,
    isError,
    refetch,
  } = useModelPrices(connection.id);
  const setPrice = useSetModelPrice();
  const deletePrice = useDeleteModelPrice();
  const [model, setModel] = useState("");
  const [inputUsd, setInputUsd] = useState("");
  const [outputUsd, setOutputUsd] = useState("");
  const [formError, setFormError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [confirmModel, setConfirmModel] = useState<string | null>(null);

  function loadForEdit(price: ModelPrice) {
    setModel(price.model);
    setInputUsd(microsToUsd(price.inputMicrosPer1M));
    setOutputUsd(microsToUsd(price.outputMicrosPer1M));
    setFormError(null);
    setNotice(null);
    setConfirmModel(null);
  }

  async function savePrice() {
    setFormError(null);
    setNotice(null);
    const name = model.trim();
    if (!MODEL_PATTERN.test(name)) {
      setFormError(
        "اسم النموذج غير صالح: حروف/أرقام إنجليزية و . _ : - فقط، ويبدأ بحرف أو رقم.",
      );
      return;
    }
    const inputMicros = parseUsdToMicros(inputUsd);
    if (inputMicros === null) {
      setFormError(
        "سعر الإدخال غير صالح: رقم غير سالب، بحد أقصى 6 منازل عشرية و1,000,000 دولار لكل 1M توكن. لا يُقبل التقريب.",
      );
      return;
    }
    const outputMicros = parseUsdToMicros(outputUsd);
    if (outputMicros === null) {
      setFormError(
        "سعر الإخراج غير صالح: رقم غير سالب، بحد أقصى 6 منازل عشرية و1,000,000 دولار لكل 1M توكن. لا يُقبل التقريب.",
      );
      return;
    }
    try {
      await setPrice.mutateAsync({
        connectionId: connection.id,
        model: name,
        input: {
          inputMicrosPer1M: inputMicros,
          outputMicrosPer1M: outputMicros,
          currency: "USD",
        },
      });
      setModel("");
      setInputUsd("");
      setOutputUsd("");
      setNotice(
        `حُفظ سعر ${name}. لن يُعاد تسعير التشغيلات السابقة؛ يسري على التشغيلات الجديدة فقط.`,
      );
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "تعذّر حفظ السعر.");
    }
  }

  async function removePrice(name: string) {
    setFormError(null);
    setNotice(null);
    try {
      await deletePrice.mutateAsync({
        connectionId: connection.id,
        model: name,
      });
      setConfirmModel(null);
      setNotice(`حُذف سعر ${name}. التشغيلات السابقة تحتفظ بتكلفتها المسجلة.`);
    } catch (err) {
      setConfirmModel(null);
      setFormError(err instanceof Error ? err.message : "تعذّر حذف السعر.");
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <p
        className={cn(
          "rounded-md border px-3 py-2 text-xs",
          prices.length > 0
            ? "border-border bg-secondary/40 text-muted-foreground"
            : "border-warning/30 bg-warning/10 text-warning",
        )}
      >
        {prices.length > 0
          ? "التكلفة تقديرية من الأسعار المدخلة يدوياً، وقد تختلف عن فاتورة المزوّد."
          : "لا توجد أسعار مسجّلة لهذا الاتصال: تكلفة تشغيلات وكيل بلا سعر تكون مجهولة، وإذا ضُبط سقف ميزانية للوكيل فسيُمنع من التشغيل (cost_unknown)."}
      </p>

      <div className="flex flex-col gap-3 rounded-md border border-border p-3">
        <p className="text-xs font-semibold">
          إضافة أو تعديل سعر — الوحدة: USD لكل 1,000,000 توكن
        </p>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
          <label
            htmlFor={`price-model-${connection.id}`}
            className="flex flex-col gap-1 text-[11px] text-muted-foreground"
          >
            النموذج
            <Input
              id={`price-model-${connection.id}`}
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="deepseek-flash"
              dir="ltr"
              list={`price-models-${connection.id}`}
            />
            <datalist id={`price-models-${connection.id}`}>
              {prices.map((p) => (
                <option key={p.model} value={p.model} />
              ))}
            </datalist>
          </label>
          <label
            htmlFor={`price-input-${connection.id}`}
            className="flex flex-col gap-1 text-[11px] text-muted-foreground"
          >
            الإدخال (USD / 1M توكن)
            <Input
              id={`price-input-${connection.id}`}
              value={inputUsd}
              onChange={(e) => setInputUsd(e.target.value)}
              placeholder="0.27"
              inputMode="decimal"
              dir="ltr"
            />
          </label>
          <label
            htmlFor={`price-output-${connection.id}`}
            className="flex flex-col gap-1 text-[11px] text-muted-foreground"
          >
            الإخراج (USD / 1M توكن)
            <Input
              id={`price-output-${connection.id}`}
              value={outputUsd}
              onChange={(e) => setOutputUsd(e.target.value)}
              placeholder="1.10"
              inputMode="decimal"
              dir="ltr"
            />
          </label>
        </div>
        <p className="text-[11px] text-muted-foreground">
          يُخزَّن السعر بوحدات micro-USD بدقة 6 منازل عشرية؛ القيم الأدق أو الأكبر
          من الحد تُرفض بدل تقريبها. تكرار الحفظ لنموذج موجود يحدّث سعره. تعديل
          السعر لا يعيد تسعير التشغيلات السابقة.
        </p>
        <div className="flex flex-wrap items-center gap-2">
          <Button
            onClick={savePrice}
            disabled={setPrice.isPending}
            className="gap-1.5"
          >
            {setPrice.isPending ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <CircleDollarSign className="size-4" />
            )}
            حفظ السعر
          </Button>
          <Button
            variant="ghost"
            onClick={() => {
              setModel("");
              setInputUsd("");
              setOutputUsd("");
              setFormError(null);
              setNotice(null);
            }}
          >
            مسح الحقول
          </Button>
        </div>
        {formError ? (
          <p role="alert" className="text-xs text-destructive">
            {formError}
          </p>
        ) : null}
        {notice ? (
          <output aria-live="polite" className="text-xs text-success">
            {notice}
          </output>
        ) : null}
      </div>

      {isLoading ? (
        <Skeleton className="h-20 w-full rounded-lg" />
      ) : isError ? (
        <div
          role="alert"
          className="flex flex-col items-center gap-2 rounded-md border border-destructive/25 p-4 text-center text-sm text-destructive"
        >
          تعذّر تحميل أسعار النماذج.
          <Button variant="outline" size="sm" onClick={() => refetch()}>
            إعادة المحاولة
          </Button>
        </div>
      ) : prices.length === 0 ? (
        <EmptyState
          icon={<CircleDollarSign />}
          title="لا توجد أسعار"
          description="أضف سعر الإدخال والإخراج للنماذج التي تستخدمها لتمكين تقدير التكلفة."
        />
      ) : (
        <div className="flex flex-col gap-2">
          {prices.map((price) => (
            <div
              key={price.model}
              className="admin-card flex flex-col gap-2 p-3 text-xs"
            >
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="font-mono font-semibold" dir="ltr">
                  {price.model}
                </span>
                <span className="text-muted-foreground">
                  {price.currency} · حُدّث{" "}
                  {new Date(price.updatedAt).toLocaleString("ar")}
                </span>
              </div>
              <div className="grid grid-cols-1 gap-1 text-muted-foreground sm:grid-cols-2">
                <span dir="rtl">
                  إدخال:{" "}
                  <span dir="ltr">${microsToUsd(price.inputMicrosPer1M)}</span>{" "}
                  لكل 1M توكن
                </span>
                <span dir="rtl">
                  إخراج:{" "}
                  <span dir="ltr">${microsToUsd(price.outputMicrosPer1M)}</span>{" "}
                  لكل 1M توكن
                </span>
              </div>
              {confirmModel === price.model ? (
                <div className="flex flex-wrap items-center gap-2 rounded-md bg-destructive/10 px-2 py-1">
                  <span className="text-destructive">
                    تأكيد حذف سعر {price.model}؟ التشغيلات السابقة لن تُعاد
                    تسعيرتها.
                  </span>
                  <Button
                    size="sm"
                    variant="destructive"
                    onClick={() => removePrice(price.model)}
                    disabled={deletePrice.isPending}
                  >
                    تأكيد الحذف
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setConfirmModel(null)}
                  >
                    إلغاء
                  </Button>
                </div>
              ) : (
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => loadForEdit(price)}
                    className="text-xs"
                  >
                    تعديل
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setFormError(null);
                      setNotice(null);
                      setConfirmModel(price.model);
                    }}
                    className="gap-1.5 text-xs text-destructive hover:bg-destructive/10"
                  >
                    <Trash2 className="size-3" />
                    حذف
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function RunsPanel({
  groupId,
  agent,
}: { groupId: number; agent: ManagedAgent }) {
  const {
    data: runs = [],
    isLoading,
    isError,
    refetch,
  } = useAgentRuns(groupId);
  const retry = useRetryAgentRun();
  const cancel = useCancelAgentRun();
  const [runError, setRunError] = useState<string | null>(null);
  const filtered = runs.filter((r) => r.botId === agent.botId);

  async function runAction(action: () => Promise<void>, fallback: string) {
    setRunError(null);
    try {
      await action();
    } catch (err) {
      setRunError(err instanceof Error ? err.message : fallback);
    }
  }
  if (isLoading) return <Skeleton className="h-24 w-full rounded-lg" />;
  if (isError) {
    return (
      <div
        role="alert"
        className="flex flex-col items-center gap-2 p-4 text-center text-sm text-destructive"
      >
        تعذّر تحميل سجل التشغيلات.
        <Button variant="outline" size="sm" onClick={() => refetch()}>
          إعادة المحاولة
        </Button>
      </div>
    );
  }
  if (filtered.length === 0) {
    return (
      <EmptyState
        icon={<Bot />}
        title="لا توجد تشغيلات"
        description="لا توجد محاولات بعد."
      />
    );
  }
  return (
    <div className="flex flex-col gap-3">
      {runError ? (
        <p role="alert" className="text-xs text-destructive">
          {runError}
        </p>
      ) : null}
      {filtered.map((run) => (
        <RunRow
          key={run.id}
          run={run}
          groupId={groupId}
          onRetry={() =>
            runAction(
              () => retry.mutateAsync({ groupId, runId: run.id }),
              "تعذّرت إعادة المحاولة.",
            )
          }
          onCancel={() =>
            runAction(
              () => cancel.mutateAsync({ groupId, runId: run.id }),
              "تعذّر إلغاء التشغيل.",
            )
          }
        />
      ))}
    </div>
  );
}

function RunRow({
  run,
  groupId: _groupId,
  onRetry,
  onCancel,
}: {
  run: AgentRun;
  groupId: number;
  onRetry: () => void;
  onCancel: () => void;
}) {
  const terminal = [
    "completed",
    "failed",
    "cancelled",
    "interrupted",
    "budget_reached",
    "cost_unknown",
  ].includes(run.status);
  const budgetBlocked =
    run.status === "budget_reached" ||
    run.status === "cost_unknown" ||
    run.errorCode === "budget_reached" ||
    run.errorCode === "cost_unknown";
  const canRetry = terminal && run.status !== "completed" && !budgetBlocked;
  const canCancel = run.status === "queued" || run.status === "running";
  return (
    <div className="admin-card flex flex-col gap-2 p-3 text-xs">
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono">
          #{run.id} · {run.status} · attempt {run.attempt}
        </span>
        <span className="text-muted-foreground">
          {new Date(run.createdAt).toLocaleString("ar")}
        </span>
      </div>
      <div className="grid grid-cols-2 gap-1 text-muted-foreground">
        <span>رسائل: {run.requestMsgCount}</span>
        <span>أحرف: {run.requestChars}</span>
        <span>tokens: {run.totalTokens ?? "—"}</span>
        <span>
          تكلفة تقديرية: {run.costMicros ?? run.estCostMicros ?? "—"} (
          {run.costBasis})
        </span>
      </div>
      {run.errorMsg && <p className="text-destructive">{run.errorMsg}</p>}
      <div className="flex gap-2">
        {canRetry && (
          <Button
            size="sm"
            variant="outline"
            onClick={onRetry}
            className="gap-1.5 text-xs"
          >
            <Play className="size-3" />
            Retry (قد ينتج طلب جديد وتكلفة)
          </Button>
        )}
        {canCancel && (
          <Button
            size="sm"
            variant="ghost"
            onClick={onCancel}
            className="gap-1.5 text-xs text-destructive"
          >
            إلغاء
          </Button>
        )}
      </div>
    </div>
  );
}
