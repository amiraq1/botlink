import Layout from "@/components/Layout";
import LogoutButton from "@/components/LogoutButton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authKey, useIsAdmin } from "@/hooks/useAdminQueries";
import { ApiError, login } from "@/lib/api";
import { formatNumberAr } from "@/lib/convert";
import { useQueryClient } from "@tanstack/react-query";
import { Bot, Lock, ShieldCheck } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useState,
} from "react";

/**
 * Applies the dark RTL Arabic admin look to the document while the admin shell
 * is mounted, so portals (drawers, dropdowns, toasts) inherit it, and restores
 * the previous state on unmount so the public agent room stays plain LTR.
 */
function useAdminDocumentMode() {
  useLayoutEffect(() => {
    const root = document.documentElement;
    const previous = {
      dark: root.classList.contains("dark"),
      dir: root.getAttribute("dir"),
      lang: root.getAttribute("lang"),
    };
    root.classList.add("dark");
    root.setAttribute("dir", "rtl");
    root.setAttribute("lang", "ar");
    return () => {
      if (!previous.dark) root.classList.remove("dark");
      if (previous.dir === null) root.removeAttribute("dir");
      else root.setAttribute("dir", previous.dir);
      if (previous.lang === null) root.removeAttribute("lang");
      else root.setAttribute("lang", previous.lang);
    };
  }, []);
}

/** Full-bleed dot-grid canvas shared by every standalone admin state. */
function Canvas({ children }: { children: ReactNode }) {
  return (
    <div className="canvas-grid flex min-h-dvh flex-col items-center justify-center px-4 py-10 text-foreground">
      <div className="flex w-full max-w-sm flex-col items-center gap-6 animate-rise-in">
        <Brand />
        {children}
      </div>
    </div>
  );
}

function Brand() {
  return (
    <div className="flex flex-col items-center gap-2.5">
      <span className="flex size-12 items-center justify-center rounded-xl bg-gradient-primary text-primary-foreground shadow-glow">
        <Bot aria-hidden="true" className="size-6" />
      </span>
      <span
        className="font-display text-lg font-bold tracking-[0.18em]"
        dir="ltr"
      >
        BOTLINK
      </span>
    </div>
  );
}

function Panel({
  ocid,
  title,
  description,
  children,
  footer,
}: {
  ocid: string;
  title: string;
  description?: string;
  children?: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <section
      data-ocid={ocid}
      className="admin-card w-full px-6 py-7 text-center shadow-lg"
    >
      <h1 className="font-display text-xl font-bold text-foreground">
        {title}
      </h1>
      {description ? (
        <p className="mx-auto mt-2 max-w-xs text-sm leading-relaxed text-muted-foreground">
          {description}
        </p>
      ) : null}
      {children ? <div className="mt-6">{children}</div> : null}
      {footer ? (
        <div className="mt-6 border-t border-border-subtle pt-4">{footer}</div>
      ) : null}
    </section>
  );
}

function LoadingPanel({ label }: { label: string }) {
  return (
    <Panel ocid="auth.loading_state" title="لحظة واحدة…">
      <div className="flex flex-col items-center gap-3 py-2">
        <span className="size-6 animate-spin rounded-full border-2 border-border-strong border-t-primary" />
        <p className="text-sm text-muted-foreground">{label}</p>
      </div>
    </Panel>
  );
}

type LoginError = "rate" | "config" | "invalid" | "network";

const RATE_FALLBACK_SEC = 30;

/** Password login for the single admin (ADMIN_PASSWORD on the server). */
function LoginPrompt() {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<LoginError | null>(null);
  const [cooldown, setCooldown] = useState(0);
  // Whether the cooldown length came from the server's Retry-After header;
  // the local fallback must not be presented as a server-provided duration.
  const [cooldownFromServer, setCooldownFromServer] = useState(false);

  useEffect(() => {
    if (cooldown <= 0) return;
    const id = window.setTimeout(
      () => setCooldown((seconds) => Math.max(0, seconds - 1)),
      1000,
    );
    return () => window.clearTimeout(id);
  }, [cooldown]);

  const locked = pending || cooldown > 0;

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (password.length === 0 || locked) return;
    setPending(true);
    setError(null);
    try {
      await login(password);
      setPassword("");
      await queryClient.invalidateQueries({ queryKey: authKey });
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        setCooldown(err.retryAfterSec ?? RATE_FALLBACK_SEC);
        setCooldownFromServer(err.retryAfterSec !== null);
        setError("rate");
      } else if (
        err instanceof Error &&
        err.message.includes("not configured")
      ) {
        setError("config");
      } else if (err instanceof ApiError) {
        setError("invalid");
      } else {
        setError("network");
      }
    } finally {
      setPending(false);
    }
  };

  const errorText: string | null =
    error === "rate"
      ? cooldown > 0
        ? cooldownFromServer
          ? `محاولات كثيرة خلال وقت قصير. أعد المحاولة بعد ${formatNumberAr(cooldown)} ثانية.`
          : `محاولات كثيرة خلال وقت قصير. أعد المحاولة بعد نحو ${formatNumberAr(cooldown)} ثانية (تقدير محلي).`
        : "محاولات كثيرة. يمكنك المحاولة الآن مجدداً."
      : error === "config"
        ? "تسجيل الدخول غير مفعّل على الخادم: حدّد ADMIN_PASSWORD أولاً."
        : error === "invalid"
          ? "كلمة المرور غير صحيحة. حاول مرة أخرى."
          : error === "network"
            ? "تعذّر الاتصال بالخادم. تحقق من الشبكة ثم أعد المحاولة."
            : null;

  return (
    <Panel
      ocid="auth.panel"
      title="تسجيل دخول المشرف"
      description="أدخل كلمة مرور المشرف لإدارة مجموعات البوتات ومتابعة محادثاتها."
      footer={
        <p className="flex items-center justify-center gap-1.5 text-2xs text-muted-foreground">
          <ShieldCheck aria-hidden="true" className="size-3.5" />
          جلسة مشفّرة وقابلة للإبطال من الخادم
        </p>
      }
    >
      <form onSubmit={handleSubmit} className="flex flex-col gap-3">
        <div className="relative">
          <Lock
            aria-hidden="true"
            className="pointer-events-none absolute inset-y-0 start-3 my-auto size-4 text-muted-foreground"
          />
          <Input
            type="password"
            dir="ltr"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value);
              if (error !== "rate") setError(null);
            }}
            placeholder="كلمة المرور"
            autoComplete="current-password"
            aria-label="كلمة مرور المشرف"
            data-ocid="auth.password_input"
            disabled={locked}
            aria-invalid={error !== null || undefined}
            className="h-11 ps-10 text-center"
          />
        </div>
        <Button
          type="submit"
          size="lg"
          loading={pending}
          data-ocid="auth.login_button"
          disabled={locked || password.length === 0}
        >
          {cooldown > 0
            ? cooldownFromServer
              ? `المحاولة بعد ${formatNumberAr(cooldown)} ثانية`
              : `المحاولة بعد نحو ${formatNumberAr(cooldown)} ثانية`
            : pending
              ? "جارٍ تسجيل الدخول…"
              : "تسجيل الدخول"}
        </Button>
      </form>
      {errorText ? (
        <p
          role="alert"
          aria-live="polite"
          data-ocid="auth.error_state"
          className="mt-4 flex items-start justify-center gap-1.5 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs font-medium text-destructive"
        >
          {errorText}
        </p>
      ) : null}
    </Panel>
  );
}

function AdminCheck({ children }: { children: ReactNode }) {
  const { data: isAdmin, isLoading, isError, refetch } = useIsAdmin();

  if (isLoading || isAdmin === undefined) {
    if (isError) {
      return (
        <Canvas>
          <Panel
            ocid="auth.error_state"
            title="تعذّر التحقق من الصلاحية"
            description="لم يستجب الخادم. تحقق من الاتصال ثم حاول مرة أخرى."
          >
            <div className="flex flex-col gap-2">
              <Button
                type="button"
                data-ocid="auth.retry_button"
                onClick={() => void refetch()}
              >
                إعادة المحاولة
              </Button>
              <LogoutButton />
            </div>
          </Panel>
        </Canvas>
      );
    }
    return (
      <Canvas>
        <LoadingPanel label="جارٍ التحقق من الصلاحية…" />
      </Canvas>
    );
  }
  if (!isAdmin) {
    return (
      <Canvas>
        <LoginPrompt />
      </Canvas>
    );
  }
  return <Layout>{children}</Layout>;
}

/** Wraps admin routes: signed-out sees the password login, admin sees the page in Layout. */
export default function AdminGate({ children }: { children: ReactNode }) {
  useAdminDocumentMode();
  return (
    <div
      dir="rtl"
      lang="ar"
      className="admin-shell min-h-dvh bg-background text-foreground"
    >
      <AdminCheck>{children}</AdminCheck>
    </div>
  );
}
