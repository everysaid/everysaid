import { createRootRouteWithContext, createRoute, createRouter, lazyRouteComponent, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { Root } from "./routes/Root";
import { ChatsHome } from "./routes/ChatsHome";
import { ChatPage } from "./routes/ChatPage";
import { OverviewPage } from "./routes/OverviewPage";

const rootRoute = createRootRouteWithContext<{ queryClient: QueryClient }>()({ component: Root });

const index = createRoute({ getParentRoute: () => rootRoute, path: "/", component: ChatsHome });
export const chatRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/chat/$chatId",
  validateSearch: (s: Record<string, unknown>): { m?: number; ts?: number } => ({
    m: s.m ? Number(s.m) : undefined,
    ts: s.ts ? Number(s.ts) : undefined,
  }),
  component: ChatPage,
});
export const searchRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/search",
  validateSearch: (s: Record<string, unknown>): { q?: string; service?: string; chat?: string } => ({
    q: (s.q as string) || undefined,
    service: (s.service as string) || undefined,
    chat: (s.chat as string) || undefined,
  }),
  component: lazyRouteComponent(() => import("./routes/SearchPage"), "SearchPage"),
});
const calls = createRoute({ getParentRoute: () => rootRoute, path: "/calls", component: lazyRouteComponent(() => import("./routes/CallsPage"), "CallsPage") });
const media = createRoute({ getParentRoute: () => rootRoute, path: "/media", component: lazyRouteComponent(() => import("./routes/MediaPage"), "MediaPage") });
const people = createRoute({ getParentRoute: () => rootRoute, path: "/people", component: lazyRouteComponent(() => import("./routes/PeoplePage"), "PeoplePage") });
export const personRoute = createRoute({ getParentRoute: () => rootRoute, path: "/people/$personId", component: lazyRouteComponent(() => import("./routes/PersonPage"), "PersonPage") });
const sources = createRoute({ getParentRoute: () => rootRoute, path: "/sources", component: lazyRouteComponent(() => import("./routes/SourcesPage"), "SourcesPage") });
const settings = createRoute({ getParentRoute: () => rootRoute, path: "/settings", component: lazyRouteComponent(() => import("./routes/SettingsPage"), "SettingsPage") });
export const dayRoute = createRoute({ getParentRoute: () => rootRoute, path: "/day/$day", component: lazyRouteComponent(() => import("./routes/DayPage"), "DayPage") });
const overview = createRoute({ getParentRoute: () => rootRoute, path: "/overview", component: OverviewPage });
const setup = createRoute({ getParentRoute: () => rootRoute, path: "/setup", component: () => <Outlet /> });

const routeTree = rootRoute.addChildren([index, chatRoute, searchRoute, calls, media, people, personRoute, sources,
  settings, dayRoute, overview, setup]);

export const router = createRouter({ routeTree, context: { queryClient: undefined! }, defaultPreload: "intent", scrollRestoration: true });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
