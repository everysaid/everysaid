import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { loadServices } from "@/lib/services";
import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { api } from "@/lib/api";
import { connectEvents, onEvent } from "@/lib/events";
import { localNotify, showLocal } from "@/lib/push";
import { toast } from "sonner";
import { Center, Spinner } from "@/components/ui";
import { AppShell } from "@/components/AppShell";
import { LoginScreen, SetupScreen } from "./Auth";
import { UpdatePrompt } from "@/components/UpdatePrompt";

export interface AuthStatus {
  logged_in: boolean;
  user: { id: number; name: string } | null;
  needs_setup: boolean;
  rp_id: string;
}

export function Root() {
  const qc = useQueryClient();
  const path = useRouterState({ select: (s) => s.location.pathname });
  const auth = useQuery({ queryKey: ["auth"], queryFn: () => api.get<AuthStatus>("/api/auth/status"), staleTime: 60_000 });

  const { i18n } = useTranslation();
  const services = useQuery({ queryKey: ["services", i18n.language], queryFn: () => loadServices(i18n.language),
    enabled: !!auth.data?.logged_in, staleTime: Infinity });

  useEffect(() => {
    if (auth.data?.logged_in) return connectEvents(qc);
  }, [auth.data?.logged_in, qc]);
  // a plugin's warning (as a push too): stays until dismissed
  useEffect(() => onEvent((e) => {
    if (e.type === "alert") toast.warning(e.title, { description: e.body, duration: Infinity, closeButton: true });
  }), []);
  // no push in this browser: the page shows the notifications itself (not for the chat being looked at)
  const navigate = useNavigate();
  useEffect(() => onEvent((e) => {
    if (e.type !== "new" || !e.notify || !localNotify()) return;
    for (const n of e.notify) {
      if (document.visibilityState === "visible" && document.hasFocus() && location.pathname === `/chat/${n.chat}`) continue;
      showLocal(n, (chat) => navigate({ to: "/chat/$chatId", params: { chatId: chat } }));
    }
  }), [navigate]);
  // the server says some things without being asked (logs, notifications): in the language last chosen
  useEffect(() => {
    if (auth.data?.logged_in) api.put("/api/settings", { language: i18n.language }).catch(() => {});
  }, [auth.data?.logged_in, i18n.language]);

  if (auth.isLoading) return <Center><Spinner className="size-7" /></Center>;
  if (path === "/setup") return <SetupScreen />;
  if (!auth.data?.logged_in) return <LoginScreen needsSetup={!!auth.data?.needs_setup} />;
  if (services.isPending && !services.isError) return <Center><Spinner className="size-7" /></Center>;
  return (
    <>
      {/* drawn again whole when the services' looks change, e.g. in another language (memoized parts too); not on a
          refetch that brings the same, which would lose the focus and the scroll */}
      <AppShell key={`${i18n.language}:${JSON.stringify(services.data)}`}>
        <Outlet />
      </AppShell>
      <UpdatePrompt />
    </>
  );
}
