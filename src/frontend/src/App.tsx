import AdminGate from "@/components/AdminGate";
import AdminDashboardPage from "@/pages/AdminDashboardPage";
import AgentRoomPage from "@/pages/AgentRoomPage";
import GroupChatPage from "@/pages/GroupChatPage";
import {
  Link,
  Outlet,
  RouterProvider,
  createRootRoute,
  createRoute,
  createRouter,
  useParams,
} from "@tanstack/react-router";

const rootRoute = createRootRoute({ component: Outlet });

/** Pathless shell: every child route is inside AdminGate + Layout. */
const adminShellRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "admin-shell",
  component: function AdminShell() {
    return (
      <AdminGate>
        <Outlet />
      </AdminGate>
    );
  },
});

const indexRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/",
  component: AdminDashboardPage,
});

const adminRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/admin",
  component: AdminDashboardPage,
});

const groupChatRoute = createRoute({
  getParentRoute: () => adminShellRoute,
  path: "/admin/g/$id",
  component: function GroupChatRoute() {
    const { id } = useParams({ strict: false });
    return <GroupChatPage id={id ?? ""} />;
  },
});

/** Public route: outside AdminGate and Layout, anonymous actor only. */
const agentRoomRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/a/$token",
  component: function AgentRoomRoute() {
    const { token } = useParams({ strict: false });
    return <AgentRoomPage token={token ?? ""} />;
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
