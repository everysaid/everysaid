/// <reference lib="webworker" />
// The service worker: the app's shell offline (never the archive's data: /api is always network),
// push notifications, and opening the right chat when one is tapped.
import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute } from "workbox-precaching";
import { NavigationRoute, registerRoute } from "workbox-routing";

declare const self: ServiceWorkerGlobalScope;

cleanupOutdatedCaches();
precacheAndRoute(self.__WB_MANIFEST);
registerRoute(new NavigationRoute(createHandlerBoundToURL("/index.html"), { denylist: [/^\/api\//] }));

self.addEventListener("message", (e) => {
  if (e.data?.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("push", (event) => {
  let data: { title?: string; body?: string; chat?: string | null; tag?: string } = {};
  try {
    data = event.data?.json() ?? {};
  } catch {
    data = { title: "Chronika", body: event.data?.text() };
  }
  event.waitUntil(
    (async () => {
      const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      // the chat is open in a visible window: no notification for it
      if (data.chat && wins.some((w) => w.visibilityState === "visible" && new URL(w.url).pathname === `/chat/${data.chat}`)) return;
      await self.registration.showNotification(data.title || "Chronika", {
        body: data.body || "",
        tag: data.tag || data.chat || undefined,
        icon: "/icon-192.png",
        badge: "/badge-72.png",
        data: { chat: data.chat },
      });
      const nav = self.navigator as unknown as { setAppBadge?: (n?: number) => Promise<void> };
      if (nav.setAppBadge) await nav.setAppBadge().catch(() => {});
    })(),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const chat = (event.notification.data as { chat?: string })?.chat;
  const url = chat ? `/chat/${chat}` : "/";
  event.waitUntil(
    (async () => {
      const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const w of wins) {
        if ("focus" in w) {
          await w.focus();
          w.postMessage({ type: "open", url });
          return;
        }
      }
      await self.clients.openWindow(url);
    })(),
  );
});
