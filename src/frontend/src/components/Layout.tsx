import LogoutButton from "@/components/LogoutButton";
import { Link, useRouterState } from "@tanstack/react-router";
import { Bot } from "lucide-react";
import type { ReactNode } from "react";

/** Group chat routes render full-bleed: they own their sticky top bar and composer. */
function useIsFullBleed(): boolean {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  return pathname.startsWith("/admin/g/");
}

/** Dark RTL admin shell. Groups home gets a slim header; the group chat route is full-bleed. */
export default function Layout({ children }: { children: ReactNode }) {
  const fullBleed = useIsFullBleed();

  if (fullBleed) {
    return (
      <div className="admin-canvas flex h-dvh flex-col text-foreground">
        {children}
      </div>
    );
  }

  return (
    <div className="admin-canvas flex h-dvh flex-col text-foreground">
      <header className="admin-topbar flex h-14 shrink-0 items-center justify-between gap-3 px-4 md:px-6">
        <div className="mx-auto flex w-full max-w-7xl items-center justify-between gap-3">
          <Link
            to="/admin"
            data-ocid="nav.home_link"
            className="flex min-w-0 items-center gap-2.5 rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <span className="flex size-8 items-center justify-center rounded-lg bg-gradient-primary text-primary-foreground shadow-xs">
              <Bot aria-hidden="true" className="size-4" />
            </span>
            <span
              className="font-display text-sm font-bold tracking-[0.16em]"
              dir="ltr"
            >
              BOTLINK
            </span>
            <span className="hidden h-4 w-px bg-border sm:block" />
            <span className="hidden truncate text-xs text-muted-foreground sm:inline">
              مجموعات البوتات
            </span>
          </Link>
          <LogoutButton />
        </div>
      </header>
      <main className="scrollbar-thin min-h-0 flex-1 overflow-y-auto animate-fade-in">
        {children}
      </main>
    </div>
  );
}
