import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { ArrowDown, ArrowUp, Bell, Download, Fingerprint, KeyRound, LogOut, Monitor, Moon, Palette, EyeOff, Shield, Sun, Tags, Trash2, UserRound } from "lucide-react";
import { toast } from "sonner";
import { api, type Account, type NameSources } from "@/lib/api";
import { fullDate, number, relative } from "@/lib/format";
import { setLanguage } from "@/lib/i18n";
import { currentSubscription, disablePush, enablePush, isIos, isStandalone, pushSupported } from "@/lib/push";
import { register } from "@/lib/passkeys";
import { applyTheme, getTheme, type Theme } from "@/lib/theme";
import { useSettings } from "@/lib/hooks";
import { Button, Card, Dialog, Section, Segmented, ServiceDot, Switch } from "@/components/ui";
import { settingsRoute } from "@/router";
import { service } from "@/lib/services";

import { PageHeader } from "@/components/PageHeader";
import { passkeyFailed, RecoveryCodes } from "./Auth";
import { PasswordSetup } from "@/components/PasswordSetup";
import { LabelLists } from "@/components/Labels";

let installEvent: any = null;
window.addEventListener("beforeinstallprompt", (e) => {
  e.preventDefault();
  installEvent = e;
});

function Line({ label, hint, children }: { label: React.ReactNode; hint?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="flex items-center gap-4 px-4 py-3.5">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{label}</div>
        {hint && <div className="mt-0.5 text-xs text-muted">{hint}</div>}
      </div>
      {children}
    </div>
  );
}

const TABS = [
  { id: "general", icon: Palette },
  { id: "names", icon: UserRound },
  { id: "labels", icon: Tags },
  { id: "services", icon: EyeOff },
  { id: "security", icon: Shield },
] as const;

/** The services the archive has, each one hidden or shown everywhere in the app (the archive keeps all). */
function HiddenServices() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const used = useQuery({ queryKey: ["services-used"], queryFn: () => api.get<{ items: { id: string; messages: number; calls: number; hidden: boolean;
    accounts: { id: number; label: string; chats: number; hidden: boolean }[] }[] }>("/api/services/used") });
  const items = used.data?.items ?? [];
  const toggle = (id: string, hide: boolean) => {
    const hidden = items.filter((s) => (s.id === id ? hide : s.hidden)).map((s) => s.id);
    api.put("/api/settings", { hidden_services: hidden }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message));
  };
  const toggleAccount = (id: number, hide: boolean) => {
    const hidden = items.flatMap((s) => s.accounts).filter((x) => (x.id === id ? hide : x.hidden)).map((x) => x.id);
    api.put("/api/settings", { hidden_accounts: [...new Set(hidden)] }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message));
  };
  return (
    <Section title={<span className="flex items-center gap-2"><EyeOff className="size-4" />{t("settings.services")}</span>}>
      <p className="-mt-1 px-1 text-xs text-muted">{t("settings.servicesHint")}</p>
      <Card className="divide-y divide-line">
        {items.map((s) => (
          <div key={s.id}>
            <Line label={<span className="flex items-center gap-2"><ServiceDot id={s.id} />{service(s.id).name}</span>}
              hint={[s.messages && t("settings.nMessages", { count: s.messages, n: number(s.messages) }), s.calls && t("settings.nCalls", { count: s.calls, n: number(s.calls) })].filter(Boolean).join(" · ")}>
              <span data-service-shown={s.id}><Switch checked={!s.hidden} onChange={(v) => toggle(s.id, !v)} label={service(s.id).name} /></span>
            </Line>
            {!s.hidden && s.accounts.length > 1 && s.accounts.map((x) => (
              <div key={x.id} className="flex items-center gap-3 py-2 pl-10 pr-4 text-sm" data-account={x.label}>
                <span className="min-w-0 flex-1 truncate">{x.label}<span className="ml-2 text-xs text-muted">{t("settings.nChats", { count: x.chats, n: number(x.chats) })}</span></span>
                <Switch checked={!x.hidden} onChange={(v) => toggleAccount(x.id, !v)} label={x.label} />
              </div>
            ))}
          </div>
        ))}
      </Card>
    </Section>
  );
}

export function SettingsPage() {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const tab = settingsRoute.useSearch().tab ?? "general";
  const qc = useQueryClient();
  const settings = useSettings();
  const account = useQuery({ queryKey: ["account"], queryFn: () => api.get<Account>("/api/auth/account") });
  const [theme, setThemeState] = useState<Theme>(getTheme());
  const [push, setPush] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const [settingPassword, setSettingPassword] = useState(false);
  const [canInstall, setCanInstall] = useState(!!installEvent);

  useEffect(() => { currentSubscription().then((s) => setPush(!!s)); }, []);
  useEffect(() => {
    const f = () => setCanInstall(true);
    window.addEventListener("beforeinstallprompt", f);
    return () => window.removeEventListener("beforeinstallprompt", f);
  }, []);

  const put = useMutation({
    mutationFn: (b: Record<string, unknown>) => api.put("/api/settings", b),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["settings"] }),
  });
  const names = useQuery({ queryKey: ["names", i18n.language], queryFn: () => api.get<NameSources>(`/api/names?lang=${i18n.language}`) });
  const order = names.data?.order ?? [];
  const setOrder = (next: string[] | null) =>
    api.put("/api/settings", { name_order: next }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message));
  const move = (i: number, by: number) => {
    const next = order.map((x) => x.id);
    [next[i], next[i + by]] = [next[i + by], next[i]];
    setOrder(next);
  };
  const refreshAccount = () => qc.invalidateQueries({ queryKey: ["account"] });

  const togglePush = async (on: boolean) => {
    try {
      if (on) await enablePush();
      else await disablePush();
      setPush(on);
    } catch (e: any) {
      toast.error(e.message === "denied" ? t("settings.pushDenied") : e.message);
    }
  };

  const a = account.data;
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("settings.title")} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-2xl space-y-8 p-4 pb-12 md:p-6">
          <div className="-mx-4 overflow-x-auto px-4 md:mx-0 md:px-0">
            <Segmented value={tab} onChange={(v) => navigate({ to: "/settings", search: { tab: v === "general" ? undefined : v }, replace: true })}
              options={TABS.map(({ id, icon: Icon }) => ({ value: id, label: <span className="flex items-center gap-1.5 whitespace-nowrap"><Icon className="size-3.5" />{t(`settings.tab.${id}`)}</span> }))} />
          </div>
          {tab === "services" && <HiddenServices />}
          {tab === "general" && (
          <Section title={<span className="flex items-center gap-2"><Palette className="size-4" />{t("settings.appearance")}</span>}>
            <Card className="divide-y divide-line">
              <Line label={t("settings.theme")}>
                <Segmented value={theme} onChange={(v) => { setThemeState(v); applyTheme(v); }} options={[
                  { value: "light", label: <span className="flex items-center gap-1"><Sun className="size-3.5" />{t("settings.light")}</span> },
                  { value: "dark", label: <span className="flex items-center gap-1"><Moon className="size-3.5" />{t("settings.dark")}</span> },
                  { value: "system", label: <span className="flex items-center gap-1"><Monitor className="size-3.5" />{t("settings.system")}</span> },
                ]} />
              </Line>
              <Line label={t("settings.language")}>
                <Segmented value={i18n.language} onChange={(l) => setLanguage(l)} options={[{ value: "el", label: "Ελληνικά" }, { value: "en", label: "English" }]} />
              </Line>
              <Line label={t("settings.sendEnter")}>
                <Switch checked={(settings.data?.send_enter as boolean | undefined) ?? true} onChange={(v) => put.mutate({ send_enter: v })} />
              </Line>
              {(canInstall || (isIos() && !isStandalone())) && (
                <Line label={t("settings.install")} hint={isIos() ? t("settings.pushIos") : t("settings.installHint")}>
                  {canInstall && <Button size="sm" variant="primary" onClick={async () => { await installEvent?.prompt(); installEvent = null; setCanInstall(false); }}><Download className="size-4" />{t("settings.install")}</Button>}
                </Line>
              )}
            </Card>
          </Section>
          )}

          {tab === "names" && (
          <Section title={<span className="flex items-center gap-2"><UserRound className="size-4" />{t("settings.names")}</span>}
            action={names.data?.custom && <Button size="sm" variant="ghost" onClick={() => setOrder(null)}>{t("settings.namesDefault")}</Button>}>
            <p className="-mt-1 px-1 text-xs text-muted">{t("settings.namesHint")}</p>
            <Card className="divide-y divide-line">
              {order.map((w, i) => (
                <Line key={w.id} label={<span className="flex items-center gap-3"><span className="w-4 text-right text-muted">{i + 1}</span>{w.label}</span>}>
                  <Button size="sm" variant="ghost" aria-label={t("settings.up")} disabled={i === 0} onClick={() => move(i, -1)}><ArrowUp className="size-4" /></Button>
                  <Button size="sm" variant="ghost" aria-label={t("settings.down")} disabled={i === order.length - 1} onClick={() => move(i, 1)}><ArrowDown className="size-4" /></Button>
                </Line>
              ))}
            </Card>
            <Card className="divide-y divide-line">
              <Line label={t("settings.showUnnamed")} hint={t("settings.showUnnamedHint")}>
                <Switch checked={(settings.data?.show_unnamed as boolean | undefined) ?? false} label={t("settings.showUnnamed")}
                  onChange={(v) => api.put("/api/settings", { show_unnamed: v }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message))} />
              </Line>
              <Line label={t("settings.hideEmptyGroups")} hint={t("settings.hideEmptyGroupsHint")}>
                <Switch checked={(settings.data?.hide_empty_groups as boolean | undefined) ?? true} label={t("settings.hideEmptyGroups")}
                  onChange={(v) => api.put("/api/settings", { hide_empty_groups: v }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message))} />
              </Line>
            </Card>
          </Section>
          )}

          {tab === "labels" && (
          <Section title={<span className="flex items-center gap-2"><Tags className="size-4" />{t("settings.labels")}</span>}>
            <p className="-mt-1 px-1 text-xs text-muted">{t("settings.labelsHint")}</p>
            <Card className="divide-y divide-line">
              <Line label={t("settings.showTone")} hint={t("settings.showToneHint")}>
                <Switch checked={(settings.data?.show_tone as boolean | undefined) ?? false} label={t("settings.showTone")}
                  onChange={(v) => api.put("/api/settings", { show_tone: v }).then(() => qc.invalidateQueries(), (e) => toast.error(e.message))} />
              </Line>
              <Line label={t("settings.mcpLabels")} hint={t("settings.mcpLabelsHint")}>
                <Switch checked={(settings.data?.mcp_labels as boolean | undefined) ?? false} label={t("settings.mcpLabels")}
                  onChange={(v) => put.mutate({ mcp_labels: v })} />
              </Line>
            </Card>
            <LabelLists />
          </Section>
          )}

          {tab === "general" && (
          <Section title={<span className="flex items-center gap-2"><Bell className="size-4" />{t("settings.notifications")}</span>}>
            <Card className="divide-y divide-line">
              <Line label={t("settings.push")} hint={isIos() && !isStandalone() ? t("settings.pushIos") : t("settings.pushHint")}>
                <Switch checked={push} onChange={togglePush} disabled={!pushSupported()} />
              </Line>
              <Line label={t("settings.pushPreview")}>
                <Switch checked={(settings.data?.push_preview as boolean | undefined) ?? true} onChange={(v) => put.mutate({ push_preview: v })} />
              </Line>
              {push && (
                <Line label={t("settings.pushTest")}>
                  <Button size="sm" onClick={() => api.post("/api/push/test").then(() => toast.success("✓"), (e) => toast.error(e.message))}>{t("common.run")}</Button>
                </Line>
              )}
            </Card>
          </Section>
          )}

          {tab === "security" && (
          <Section title={<span className="flex items-center gap-2"><Shield className="size-4" />{t("settings.security")}</span>}>
            <Card className="divide-y divide-line">
              <div className="px-4 pt-3.5 text-sm font-medium">{t("settings.passkeys")}</div>
              {a?.passkeys.map((p) => (
                <Line key={p.id} label={<span className="flex items-center gap-2"><Fingerprint className="size-4 text-muted" />{p.name || t("settings.passkey")}</span>}
                  hint={`${fullDate(p.created_at * 1000)}${p.last_used ? ` · ${relative(p.last_used)}` : ""}`}>
                  <Button size="sm" variant="ghost" onClick={() => {
                    const n = prompt(t("settings.rename"), p.name ?? "");
                    if (n != null) api.patch(`/api/auth/passkeys/${p.id}`, { name: n }).then(refreshAccount);
                  }}>{t("settings.rename")}</Button>
                  {a.passkeys.length > 1 && (
                    <Button size="sm" variant="ghost" aria-label={t("common.delete")} onClick={() => confirm(`${t("common.delete")}: ${p.name}?`) && api.del(`/api/auth/passkeys/${p.id}`).then(refreshAccount, (e) => toast.error(e.message))}>
                      <Trash2 className="size-4" />
                    </Button>
                  )}
                </Line>
              ))}
              <div className="px-4 py-3">
                <Button size="sm" variant="outline" onClick={() => register({}).then(() => { refreshAccount(); toast.success(t("common.done")); }, (e) => (e?.status ? toast.error(e.message) : passkeyFailed(e, t)))}>
                  <Fingerprint className="size-4" />{t("settings.addPasskey")}
                </Button>
              </div>
            </Card>
            <Card className="divide-y divide-line">
              <div className="px-4 pt-3.5 text-sm font-medium">{t("settings.sessions")}</div>
              {a?.sessions.map((s) => (
                <Line key={s.id} label={<span>{s.agent?.slice(0, 60) || "—"} {s.current && <span className="ml-1 rounded-full bg-accent/15 px-2 py-0.5 text-[11px] text-accent">{t("settings.thisDevice")}</span>}</span>}
                  hint={`${s.ip ?? ""} · ${t(`settings.via.${s.via}`, { defaultValue: s.via })} · ${relative(s.last_seen)}`}>
                  {!s.current && <Button size="sm" variant="ghost" onClick={() => api.del(`/api/auth/sessions/${s.id}`).then(refreshAccount)}>{t("settings.endSession")}</Button>}
                </Line>
              ))}
            </Card>
            <Card className="divide-y divide-line">
              <Line label={t("auth.passwordTitle")} hint={`${a?.has_password ? t("auth.passwordOn") : t("auth.passwordOff")}${a ? ` · ${t("auth.name")}: ${a.user.name}` : ""}`}>
                <Button size="sm" onClick={() => setSettingPassword(true)}>{a?.has_password ? t("auth.changePassword") : t("auth.setPassword")}</Button>
                {a?.has_password && (a?.passkeys.length ?? 0) > 0 && (
                  <Button size="sm" variant="ghost" onClick={() => confirm(t("auth.removePassword") + "?") && api.del("/api/auth/password").then(refreshAccount, (e) => toast.error(e.message))}>
                    {t("auth.removePassword")}
                  </Button>
                )}
              </Line>
            </Card>
            <Card className="divide-y divide-line">
              <Line label={<span className="flex items-center gap-2"><KeyRound className="size-4 text-muted" />{t("settings.recovery")}</span>} hint={a ? t("settings.recoveryLeft", { n: a.recovery_left }) : ""}>
                <Button size="sm" onClick={() => confirm(t("settings.newCodes") + "?") && api.post<{ codes: string[] }>("/api/auth/recovery-codes").then((r) => { setCodes(r.codes); refreshAccount(); })}>
                  {t("settings.newCodes")}
                </Button>
              </Line>
              <Line label={t("settings.mcp")} hint={t("settings.mcpHint")}>
                <Button size="sm" onClick={() => api.post<{ token: string }>("/api/auth/mcp-token", { label: "MCP" }).then((r) => setToken(r.token))}>{t("settings.newToken")}</Button>
              </Line>
            </Card>
            <Button variant="outline" className="w-full" onClick={() => api.post("/api/auth/logout").then(() => location.assign("/"))}>
              <LogOut className="size-4" />{t("settings.logout")}
            </Button>
          </Section>
          )}

          {a && tab === "security" && (
            <Section title={t("settings.activity")}>
              <Card className="divide-y divide-line text-sm">
                {a.audit.slice(0, 15).map((e, i) => (
                  <div key={i} className="flex gap-3 px-4 py-2">
                    <span className="shrink-0 text-muted">{fullDate(e.ts * 1000)}</span>
                    <span className="min-w-0 truncate">{e.event}{e.detail ? ` · ${e.detail}` : ""}</span>
                  </div>
                ))}
              </Card>
            </Section>
          )}
        </div>
      </div>
      <Dialog open={!!codes} onOpenChange={(o) => !o && setCodes(null)} title={t("settings.recovery")}>
        {codes && <RecoveryCodes codes={codes} onDone={() => setCodes(null)} />}
      </Dialog>
      <Dialog open={settingPassword} onOpenChange={setSettingPassword} title={t("auth.passwordTitle")}>
        {settingPassword && <PasswordSetup onDone={() => { setSettingPassword(false); refreshAccount(); toast.success(t("common.done")); }} />}
      </Dialog>
      <Dialog open={!!token} onOpenChange={(o) => !o && setToken(null)} title={t("settings.mcp")} description={t("settings.mcpHint")}>
        <pre className="break-all whitespace-pre-wrap rounded-2xl bg-panel-2 p-4 font-mono text-xs">{token}</pre>
        <Button className="mt-3" onClick={() => navigator.clipboard.writeText(token ?? "").then(() => toast.success(t("common.copied")))}>{t("common.copy")}</Button>
      </Dialog>
    </div>
  );
}
