import { watchNavigation } from "@/lib/back";
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
  // hide: the services whose messages and calls the user turned off (comma separated)
  validateSearch: (s: Record<string, unknown>): { m?: number; ts?: number; hide?: string } => ({
    m: s.m ? Number(s.m) : undefined,
    ts: s.ts ? Number(s.ts) : undefined,
    hide: typeof s.hide === "string" && s.hide ? s.hide : undefined,
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
// a chat's own calls or media: ?chat=<id> (from its info); none: all of them
const chatFilter = (s: Record<string, unknown>): { chat?: string } => ({ chat: (s.chat as string) || undefined });
export const callsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/calls", validateSearch: chatFilter,
  component: lazyRouteComponent(() => import("./routes/CallsPage"), "CallsPage") });
export const mediaRoute = createRoute({ getParentRoute: () => rootRoute, path: "/media", validateSearch: chatFilter,
  component: lazyRouteComponent(() => import("./routes/MediaPage"), "MediaPage") });
const people = createRoute({ getParentRoute: () => rootRoute, path: "/people", component: lazyRouteComponent(() => import("./routes/PeoplePage"), "PeoplePage") });
const unnamed = createRoute({ getParentRoute: () => rootRoute, path: "/people/unnamed", component: lazyRouteComponent(() => import("./routes/UnnamedPage"), "UnnamedPage") });
const mergeReview = createRoute({ getParentRoute: () => rootRoute, path: "/people/merge", component: lazyRouteComponent(() => import("./routes/MergeReviewPage"), "MergeReviewPage") });
export const personRoute = createRoute({ getParentRoute: () => rootRoute, path: "/people/$personId", component: lazyRouteComponent(() => import("./routes/PersonPage"), "PersonPage") });
export const sourcesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/sources",
  validateSearch: (s: Record<string, unknown>): { tab?: string } => ({ tab: (s.tab as string) || undefined }), component: lazyRouteComponent(() => import("./routes/SourcesPage"), "SourcesPage") });
const settings = createRoute({ getParentRoute: () => rootRoute, path: "/settings", component: lazyRouteComponent(() => import("./routes/SettingsPage"), "SettingsPage") });
const overview = createRoute({ getParentRoute: () => rootRoute, path: "/overview", component: OverviewPage });
const setup = createRoute({ getParentRoute: () => rootRoute, path: "/setup", component: () => <Outlet /> });

const routeTree = rootRoute.addChildren([index, chatRoute, searchRoute, callsRoute, mediaRoute, people, mergeReview, unnamed, personRoute, sourcesRoute,
  settings, overview, setup]);

export const router = createRouter({ routeTree, context: { queryClient: undefined! }, defaultPreload: "intent", scrollRestoration: true });
watchNavigation(router);

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
