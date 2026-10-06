import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import "./index.css";
import "./lib/i18n";
import { router } from "./router";
import { TipProvider } from "./components/ui";
import { Toaster } from "sonner";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, refetchOnWindowFocus: true, retry: (n, e: any) => e?.status !== 401 && e?.status !== 404 && n < 2 },
  },
});

window.addEventListener("everysaid:logged-out", () => {
  queryClient.setQueryData(["auth"], (old: any) => (old ? { ...old, logged_in: false } : old));
});

// the service worker asks to open a chat (a tapped notification)
navigator.serviceWorker?.addEventListener("message", (e) => {
  if (e.data?.type === "open" && e.data.url) router.navigate({ to: e.data.url });
});

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <TipProvider>
        <RouterProvider router={router} context={{ queryClient }} />
        <Toaster position="top-center" richColors closeButton />
      </TipProvider>
    </QueryClientProvider>
  </React.StrictMode>,
);
