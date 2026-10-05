import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Fingerprint, KeyRound, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { login, register, supported } from "@/lib/passkeys";
import { Button, Field, Input } from "@/components/ui";
import { Logo } from "@/components/Logo";
import { PasswordSetup } from "@/components/PasswordSetup";

/** A passkey that did not work: the browser's own words go to the console (they are in its
 *  language, and technical); the user gets ours, and the other way in. */
export function passkeyFailed(e: any, t: (k: string) => string) {
  console.warn("passkey:", e?.name, e?.message);
  if (e?.name === "NotAllowedError" || e?.name === "SecurityError" || e?.name === "NotSupportedError")
    toast.error(t("auth.cancelled"), { description: t("auth.whereHint"), duration: 12000 });
  else toast.error(t("auth.failed"), { description: t("auth.whereHint"), duration: 12000 });
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-full items-center justify-center bg-bg p-6 pt-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))]">
      <div className="w-full max-w-sm space-y-8">
        <div className="flex flex-col items-center gap-4 text-center">
          <Logo className="size-16" />
          {children}
        </div>
      </div>
    </div>
  );
}

export function LoginScreen({ needsSetup }: { needsSetup: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [mode, setMode] = useState<"passkey" | "password" | "recover">(supported() ? "passkey" : "password");
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [otp, setOtp] = useState("");
  const done = () => qc.invalidateQueries();

  const go = async () => {
    setBusy(true);
    try {
      await login();
      await done();
    } catch (e: any) {
      if (e?.status) toast.error(e.message);
      else passkeyFailed(e, t);
    } finally {
      setBusy(false);
    }
  };

  const recover = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api.post("/api/auth/recover", { code });
      await done();
    } catch (err: any) {
      toast.error(err.message);
    } finally {
      setBusy(false);
    }
  };

  const withPassword = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await api.post("/api/auth/password/login", { name, password, code: otp });
      await done();
    } catch (err: any) {
      toast.error(err.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Frame>
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">{t("app.name")}</h1>
        <p className="mt-1 text-sm text-muted">{t("app.tagline")}</p>
      </div>
      {!needsSetup && mode === "password" ? (
        <form onSubmit={withPassword} className="w-full space-y-3 text-left">
          <Field label={t("auth.name")}><Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="username" autoFocus required /></Field>
          <Field label={t("auth.password")}><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></Field>
          <Field label={t("auth.code")}><Input inputMode="numeric" autoComplete="one-time-code" value={otp} onChange={(e) => setOtp(e.target.value)} required /></Field>
          <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy}><KeyRound className="size-5" /> {t("auth.withPassword")}</Button>
          <div className="flex justify-between text-sm">
            {supported() && <button type="button" className="text-accent hover:underline" onClick={() => setMode("passkey")}>{t("auth.usePasskey")}</button>}
            <button type="button" className="text-accent hover:underline" onClick={() => setMode("recover")}>{t("auth.recover")}</button>
          </div>
        </form>
      ) : needsSetup ? (
        <p className="rounded-2xl bg-panel p-4 text-sm text-muted">{t("auth.invalidLink")}</p>
      ) : mode === "passkey" ? (
        <div className="w-full space-y-3">
          {supported() ? (
            <Button variant="primary" size="lg" className="w-full" onClick={go} loading={busy}>
              <Fingerprint className="size-5" /> {t("auth.login")}
            </Button>
          ) : (
            <p className="text-sm text-danger">{t("auth.noPasskeys")}</p>
          )}
          <p className="text-xs text-muted">{t("auth.loginHint")}</p>
          <div className="flex flex-col items-center gap-1.5">
            <button className="text-sm text-accent hover:underline" onClick={() => setMode("password")}>{t("auth.withPassword")}</button>
            <button className="text-sm text-accent hover:underline" onClick={() => setMode("recover")}>{t("auth.recover")}</button>
          </div>
          <p className="pt-4 text-xs text-muted">{t("auth.needsLink")}</p>
        </div>
      ) : (
        <form onSubmit={recover} className="w-full space-y-3 text-left">
          <Field label={t("auth.recoverCode")}>
            <Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="abcd-ef01-2345-6789" autoFocus autoComplete="off" />
          </Field>
          <div className="flex gap-2">
            <Button type="button" variant="ghost" onClick={() => setMode(supported() ? "passkey" : "password")}>{t("common.back")}</Button>
            <Button type="submit" variant="primary" className="flex-1" loading={busy}>
              <KeyRound className="size-4" /> {t("auth.recoverGo")}
            </Button>
          </div>
        </form>
      )}
    </Frame>
  );
}

export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation();
  const text = codes.join("\n");
  return (
    <div className="w-full space-y-4 text-left">
      <div className="flex items-center gap-2 font-semibold"><ShieldCheck className="size-5 text-ok" /> {t("auth.codesTitle")}</div>
      <p className="text-sm text-muted">{t("auth.codesHint")}</p>
      <pre className="grid grid-cols-2 gap-2 rounded-2xl bg-panel p-4 font-mono text-sm">
        {codes.map((c) => <span key={c}>{c}</span>)}
      </pre>
      <div className="flex gap-2">
        <Button variant="outline" onClick={() => navigator.clipboard.writeText(text).then(() => toast.success(t("common.copied")))}>
          {t("common.copy")}
        </Button>
        <Button
          variant="outline"
          onClick={() => {
            const a = document.createElement("a");
            a.href = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
            a.download = "chronika-recovery-codes.txt";
            a.click();
          }}
        >
          {t("common.download")}
        </Button>
        <Button variant="primary" className="flex-1" onClick={onDone}>{t("auth.codesSaved")}</Button>
      </div>
    </div>
  );
}

export function SetupScreen() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const token = location.hash.slice(1);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<string[] | null>(null);
  const [way, setWay] = useState<"passkey" | "password">(supported() ? "passkey" : "password");
  const [named, setNamed] = useState(false);

  const finish = async () => {
    history.replaceState(null, "", "/");
    await qc.invalidateQueries();
    location.assign("/");
  };

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await register({ token, name: name.trim() || undefined });
      if (r.recovery_codes) setCodes(r.recovery_codes);
      else await finish();
    } catch (err: any) {
      if (err?.status) toast.error(err.status === 403 ? t("auth.invalidLink") : err.message);
      else passkeyFailed(err, t);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Frame>
      {codes ? (
        <RecoveryCodes codes={codes} onDone={finish} />
      ) : (
        <>
          <div>
            <h1 className="text-2xl font-semibold tracking-tight">{t("auth.setupTitle")}</h1>
            <p className="mt-2 text-sm text-muted">{way === "password" ? t("auth.passwordSetupHint") : t("auth.setupHint")}</p>
          </div>
          {!token ? (
            <p className="rounded-2xl bg-panel p-4 text-sm text-muted">{t("auth.invalidLink")}</p>
          ) : way === "password" ? (
            <div className="w-full space-y-3">
              {!named ? (
                <form className="space-y-3 text-left" onSubmit={(e) => { e.preventDefault(); if (name.trim()) setNamed(true); else toast.error(t("auth.nameNeeded")); }}>
                  <Field label={t("auth.yourName")} help={t("auth.nameHelp")}>
                    <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="username" autoFocus />
                  </Field>
                  <Button type="submit" variant="primary" size="lg" className="w-full">{t("auth.next")}</Button>
                </form>
              ) : (
                <PasswordSetup token={token} name={name.trim()} onDone={(c) => (c ? setCodes(c) : finish())} />
              )}
              {supported() && <button className="w-full text-center text-sm text-accent hover:underline" onClick={() => { setWay("passkey"); setNamed(false); }}>{t("auth.create")}</button>}
            </div>
          ) : (
            <form onSubmit={create} className="w-full space-y-4 text-left">
              <Field label={t("auth.yourName")}>
                <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus autoComplete="name" />
              </Field>
              <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} disabled={!supported()}>
                <Fingerprint className="size-5" /> {t("auth.create")}
              </Button>
              <button type="button" className="w-full text-center text-sm text-accent hover:underline" onClick={() => setWay("password")}>
                {t("auth.orPassword")}
              </button>
            </form>
          )}
        </>
      )}
    </Frame>
  );
}
