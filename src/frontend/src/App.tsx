import {
  Link,
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  useParams,
} from "@tanstack/react-router";
import { Suspense, lazy } from "react";

const AdminGate = lazy(() => import("@/components/AdminGate"));
const AdminDashboardPage = lazy(() => import("@/pages/AdminDashboardPage"));
const AgentRoomPage = lazy(() => import("@/pages/AgentRoomPage"));
const GroupChatPage = lazy(() => import("@/pages/GroupChatPage"));

/** Dark placeholder for the admin shell while its chunk loads. */
function AdminPending() {
  return (
    <div className="dark flex min-h-dvh items-center justify-center bg-background">
      <span className="font-mono text-sm text-muted-foreground" dir="rtl">
        …
      </span>
    </div>
  );
}

/** Plain placeholder for the public agent room while its chunk loads. */
function RoomPending() {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-background">
      <span className="font-mono text-sm font-bold text-foreground">
        Loading...
      </span>
    </div>
  );
}

const rootRoute = createRootRoute({ component: Outlet });

/** Pathless shell: every child route is inside AdminGate + Layout. */
const adminShellRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "admin-shell",
  component: function AdminShell() {
    return (
      <Suspense fallback={<AdminPending />}>
        <AdminGate>
          <Outlet />
        </AdminGate>
      </Suspense>
    );
  },
});

function AdminDashboardRoute() {
  return (
    <Suspense fallback={<AdminPending />}>
      <AdminDashboardPage />
    </Suspense>
  );
}

const indexRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/",
  component: AdminDashboardRoute,
});

const adminRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/admin",
  component: AdminDashboardRoute,
});

const groupChatRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/admin/g/$id",
  component: function GroupChatRoute() {
    const { id } = useParams({ strict: false });
    return (
      <Suspense fallback={<AdminPending />}>
        <GroupChatPage id={id ?? ""} />
      </Suspense>
    );
  },
});

/** Public route: outside AdminGate and Layout, anonymous actor only. */
const agentRoomRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/a/$token",
  component: function AgentRoomRoute() {
    const { token } = useParams({ strict: false });
    return (
      <Suspense fallback={<RoomPending />}>
        <AgentRoomPage token={token ?? ""} />
      </Suspense>
    );
  },
});

const routeTree = rootRoute.addChildren([
  adminShellRoute.addChildren([indexRoute, adminRoute, groupChatRoute]),
  agentRoomRoute,
]);

const router = createRouter({
  routeTree,
  defaultNotFoundComponent: () => (
    <div
      data-ocid="notfound.page"
      className="flex min-h-dvh flex-col items-center justify-center gap-3 bg-background text-foreground"
    >
      <h1 className="font-display text-2xl font-semibold">Page not found</h1>
      <Link to="/admin" className="text-primary underline">
        Go to dashboard
      </Link>
    </div>
  ),
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

export default function App() {
  return <RouterProvider router={router} />;
}
