import { browserSupportsWebAuthn, startAuthentication, startRegistration } from "@simplewebauthn/browser";
import { api } from "./api";

export const supported = () => browserSupportsWebAuthn();

export async function register(opts: { token?: string; name?: string; label?: string }) {
  const { nonce, options } = await api.post<{ nonce: string; options: any }>("/api/auth/register/options", {
    token: opts.token, name: opts.name,
  });
  const credential = await startRegistration({ optionsJSON: options });
  return api.post<{ ok: boolean; recovery_codes: string[] | null }>("/api/auth/register/verify", {
    nonce, credential, label: opts.label ?? deviceLabel(),
  });
}

export async function login() {
  const { nonce, options } = await api.post<{ nonce: string; options: any }>("/api/auth/login/options");
  const credential = await startAuthentication({ optionsJSON: options });
  return api.post("/api/auth/login/verify", { nonce, credential });
}

export function deviceLabel() {
  const ua = navigator.userAgent;
  const os = /iPhone|iPad/.test(ua) ? "iOS" : /Android/.test(ua) ? "Android" : /Mac/.test(ua) ? "macOS" : /Windows/.test(ua) ? "Windows" : /Linux/.test(ua) ? "Linux" : "";
  const br = /Firefox/.test(ua) ? "Firefox" : /Edg\//.test(ua) ? "Edge" : /Chrome/.test(ua) ? "Chrome" : /Safari/.test(ua) ? "Safari" : "";
  return [br, os].filter(Boolean).join(" · ") || "Passkey";
}
