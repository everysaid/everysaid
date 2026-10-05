import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import QRCode from "qrcode";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { Button, Field, Input, Spinner } from "./ui";

/** A password with an authenticator app (TOTP): the way in that needs no passkey. Used on the setup
 *  page (with its one-time token) and in Settings (signed in). */
export function PasswordSetup({ token, name, onDone }: { token?: string; name?: string; onDone: (codes: string[] | null) => void }) {
  const { t } = useTranslation();
  const [opts, setOpts] = useState<{ nonce: string; secret: string; uri: string } | null>(null);
  const [qr, setQr] = useState<string | null>(null);
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [tried, setTried] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [round, setRound] = useState(0);
  // What is still wrong, said as soon as it can be (never a silently disabled button).
  const short = password.length > 0 && password.length < 12 && t("auth.passwordShort", { n: 12 - password.length });
  const differ = again.length > 0 && again !== password && t("auth.mismatch");
  const badCode = !/^\d{6}$/.test(code.replace(/\s/g, "")) && t("auth.codeBad");
  const problem = (password.length < 12 && t("auth.passwordShort", { n: 12 - password.length })) || (again !== password && t("auth.mismatch")) || badCode;

  useEffect(() => {
    setOpts(null);
    api.post<{ nonce: string; secret: string; uri: string }>("/api/auth/password/options", { token, name })
      .then((o) => {
        setOpts(o);
        return QRCode.toDataURL(o.uri, { margin: 1, width: 220 });
      })
      .then(setQr)
      .catch((e) => setError(e.message));
  }, [token, name, round]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setTried(true);
    setError(null);
    if (problem) return toast.error(problem);
    setBusy(true);
    try {
      const r = await api.post<{ recovery_codes: string[] | null }>("/api/auth/password/set", { nonce: opts!.nonce, password, code });
      onDone(r.recovery_codes);
    } catch (err: any) {
      // the server's reason stays on the form; an expired setup brings a new code to scan
      if (err?.status === 410) {
        setError(t("auth.setupExpired"));
        setCode("");
        setRound((n) => n + 1);
      } else setError(err.message);
    } finally {
      setBusy(false);
    }
  };

  const alert = error && <p role="alert" className="rounded-xl bg-danger/10 p-3 text-sm text-danger">{error}</p>;
  if (!opts) return alert || <div className="grid h-40 place-items-center"><Spinner /></div>;
  return (
    <form onSubmit={submit} className="w-full space-y-4 text-left">
      <p className="text-sm text-muted">{t("auth.passwordHint")}</p>
      <div className="flex flex-col items-center gap-2">
        {qr && <img src={qr} alt="QR" className="size-44 rounded-xl bg-white p-1" />}
        <div className="w-full text-center">
          <div className="text-xs text-muted">{t("auth.secret")}</div>
          <code className="select-all break-all text-sm">{opts.secret.match(/.{1,4}/g)?.join(" ")}</code>
        </div>
      </div>
      <Field label={t("auth.password")} help={t("auth.passwordRule")} error={short}>
        <Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
      <Field label={t("auth.passwordAgain")} error={differ}>
        <Input type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />
      </Field>
      <Field label={t("auth.code")} error={tried && badCode}>
        <Input inputMode="numeric" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} />
      </Field>
      {alert}
      <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy}>
        {t("auth.setPassword")}
      </Button>
    </form>
  );
}
