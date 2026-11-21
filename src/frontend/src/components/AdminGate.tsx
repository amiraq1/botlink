import { RequireSignIn } from "@/components/InternetIdentity";
import Layout from "@/components/Layout";
import LogoutButton from "@/components/LogoutButton";
import { Button } from "@/components/ui/button";
import { useIsAdmin } from "@/hooks/useAdminQueries";
import { useInternetIdentity } from "@caffeineai/core-infrastructure";
import { Loader2, ShieldAlert } from "lucide-react";
import { type ReactNode, useLayoutEffect } from "react";

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

function LoginPrompt() {
  const { login, isLoginError, isLoggingIn, isInitializing } =
    useInternetIdentity();
  return (
    <CenteredPanel
      ocid="auth.panel"
      title="دخول المشرف"
      description="سجّل الدخول بهوية الإنترنت لإدارة مجموعات البوتات ومتابعة محادثاتها."
    >
      <Button
        type="button"
        size="lg"
        data-ocid="auth.login_button"
        className="rounded-full px-8"
        disabled={isInitializing || isLoggingIn}
        onClick={() => login()}
      >
        {isLoggingIn ? "جارٍ تسجيل الدخول…" : "تسجيل الدخول"}
      </Button>
      {isLoginError && (
        <p
          role="alert"
          data-ocid="auth.error_state"
          className="text-sm text-destructive"
        >
          لم يكتمل تسجيل الدخول. حاول مرة أخرى.
        </p>
      )}
    </CenteredPanel>
  );
}

function AdminCheck({ children }: { children: ReactNode }) {
  const { data: isAdmin, isLoading, isError, refetch } = useIsAdmin();

  if (isLoading || (isAdmin === undefined && !isError)) {
    return <LoadingPanel label="جارٍ التحقق من الصلاحية…" />;
  }
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
  if (!isAdmin) {
    return (
      <CenteredPanel
        ocid="auth.access_denied_state"
        title="الوصول مرفوض"
        description="هذا الحساب ليس مشرف BOTLINK."
      >
        <ShieldAlert aria-hidden="true" className="size-8 text-destructive" />
        <LogoutButton />
      </CenteredPanel>
    );
  }
  return <Layout>{children}</Layout>;
}

/** Wraps admin routes: signed-out sees login, non-admin is denied, admin sees the page in Layout. */
export default function AdminGate({ children }: { children: ReactNode }) {
  useAdminDocumentMode();
  const { isInitializing, isLoggingIn } = useInternetIdentity();
  return (
    <div
      dir="rtl"
      lang="ar"
      className="admin-shell min-h-dvh bg-background text-foreground"
    >
      {isInitializing || isLoggingIn ? (
        <LoadingPanel
          label={isLoggingIn ? "جارٍ تسجيل الدخول…" : "جارٍ التحميل…"}
        />
      ) : (
        <RequireSignIn fallback={<LoginPrompt />}>
          <AdminCheck>{children}</AdminCheck>
        </RequireSignIn>
      )}
    </div>
  );
}
