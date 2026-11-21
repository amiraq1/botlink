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
        <Link
          to="/admin"
          data-ocid="nav.home_link"
          className="flex min-w-0 items-center gap-3 rounded-full focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        >
          <span className="flex size-9 items-center justify-center rounded-full bg-primary text-primary-foreground">
            <Bot aria-hidden="true" className="size-5" />
          </span>
          <span className="font-display text-lg font-bold" dir="ltr">
            BOTLINK
          </span>
          <span className="hidden truncate text-sm text-muted-foreground sm:inline">
            مجموعات البوتات
          </span>
        </Link>
        <LogoutButton />
      </header>
      <main className="scrollbar-thin min-h-0 flex-1 overflow-y-auto animate-fade-in">
        {children}
      </main>
    </div>
  );
}
