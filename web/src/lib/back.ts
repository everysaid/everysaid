import { useCanGoBack, useLocation, useNavigate, useRouter, type AnyRouter } from "@tanstack/react-router";

// Where each page of the session was reached from: the path of the page before it, or "nav" when
// it was opened from the app's own menu (the rail, the tabs), which is itself the way to go on.
const cameFrom = new Map<string, string>();
let viaNav = false;

/** The next navigation is the menu's. */
export const markNav = () => { viaNav = true; };

const keyOf = (loc: { state: Record<string, unknown>; href: string }) => String(loc.state.__TSR_key ?? loc.href);

export function watchNavigation(router: AnyRouter) {
  router.subscribe("onBeforeNavigate", ({ fromLocation, toLocation }) => {
    const key = keyOf(toLocation as never);
    if (viaNav) cameFrom.set(key, "nav");
    else if (fromLocation && !cameFrom.has(key)) cameFrom.set(key, fromLocation.pathname);
    viaNav = false;
  });
}

/** The way back from this page: to the page it was reached from, else to `fallback` (a page opened
 * by a link from outside, or reloaded). show: whether there is a way back that is not the menu's.
 * from: the path of the page before, if known. */
export function useBack(fallback?: string) {
  const router = useRouter();
  const navigate = useNavigate();
  const canGoBack = useCanGoBack();
  const loc = useLocation();
  const from = cameFrom.get(keyOf(loc as never));
  const inApp = canGoBack && !!from && from !== "nav";
  return {
    show: inApp || !!fallback,
    from: inApp ? from : undefined,
    go: () => (inApp ? router.history.back() : navigate({ to: fallback ?? "/" })),
  };
}
