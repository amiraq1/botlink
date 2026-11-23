import Layout from "@/components/Layout";
import LogoutButton from "@/components/LogoutButton";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { authKey, useIsAdmin } from "@/hooks/useAdminQueries";
import { login } from "@/lib/api";
import { useQueryClient } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
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

function CenteredPanel({
  ocid,
  title,
  description,
  children,
}: {
  ocid: string;
  title: string;
  description?: string;
  children?: ReactNode;
}) {
  return (
    <section
      data-ocid={ocid}
      className="admin-canvas flex min-h-dvh flex-col items-center justify-center gap-4 px-6 text-center text-foreground animate-fade-in"
    >
      <span className="font-display text-3xl font-bold" dir="ltr">
        BOTLINK
      </span>
      <h1 className="text-xl font-semibold">{title}</h1>
      {description && (
        <p className="max-w-md text-sm text-muted-foreground">{description}</p>
      )}
      {children}
    </section>
  );
}

function LoadingPanel({ label }: { label: string }) {
  return (
    <CenteredPanel ocid="auth.loading_state" title={label}>
      <Loader2
        aria-hidden="true"
        className="size-6 animate-spin text-muted-foreground"
      />
    </CenteredPanel>
  );
}

/** Password login for the single admin (ADMIN_PASSWORD on the server). */
function LoginPrompt() {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (password.length === 0 || pending) return;
    setPending(true);
    setError(null);
    try {
      await login(password);
      setPassword("");
      await queryClient.invalidateQueries({ queryKey: authKey });
    } catch (err) {
      setError(
        err instanceof Error && err.message.includes("not configured")
          ? "تسجيل الدخول غير مفعّل على الخادم: حدّد ADMIN_PASSWORD أولاً."
          : "كلمة المرور غير صحيحة. حاول مرة أخرى.",
      );
    } finally {
      setPending(false);
    }
  };

  return (
    <CenteredPanel
      ocid="auth.panel"
      title="تسجيل دخول المشرف"
      description="أدخل كلمة مرور المشرف لإدارة مجموعات البوتات ومتابعة محادثاتها."
    >
      <form
        onSubmit={handleSubmit}
        className="flex w-full max-w-xs flex-col gap-3"
      >
        <Input
          type="password"
          dir="ltr"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="كلمة المرور"
          autoComplete="current-password"
          aria-label="كلمة مرور المشرف"
          data-ocid="auth.password_input"
          disabled={pending}
          className="admin-input h-12 text-center"
        />
        <Button
          type="submit"
          size="lg"
          data-ocid="auth.login_button"
          className="rounded-full px-8"
          disabled={pending || password.length === 0}
        >
          {pending ? "جارٍ تسجيل الدخول…" : "تسجيل الدخول"}
        </Button>
      </form>
      {error && (
        <p
          role="alert"
          data-ocid="auth.error_state"
          className="text-sm text-destructive"
        >
          {error}
        </p>
      )}
    </CenteredPanel>
  );
}

function AdminCheck({ children }: { children: ReactNode }) {
  const { data: isAdmin, isLoading, isError, refetch } = useIsAdmin();

  if (isLoading || isAdmin === undefined) {
    if (isError) {
      return (
        <CenteredPanel
          ocid="auth.error_state"
          title="تعذّر التحقق من الصلاحية"
          description="لم يستجب الخادم. تحقق من الاتصال ثم حاول مرة أخرى."
        >
          <div className="flex gap-2">
            <Button
              type="button"
              data-ocid="auth.retry_button"
              className="rounded-full"
              onClick={() => void refetch()}
            >
              إعادة المحاولة
            </Button>
            <LogoutButton />
          </div>
        </CenteredPanel>
      );
    }
    return <LoadingPanel label="جارٍ التحقق من الصلاحية…" />;
  }
  if (!isAdmin) {
    return <LoginPrompt />;
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
