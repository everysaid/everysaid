import { expect, type Page } from "@playwright/test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { createHmac } from "node:crypto";
import { execFileSync } from "node:child_process";

// What the end-to-end tests share: the demo's folders, an authenticator's code, and a way in (a
// one-time link from `chronika user link`, then a password with an authenticator code).
const DEMO = process.env.DEMO_DIR ?? "/tmp/chr-demo";
export const env = { ...process.env, CHRONIKA_DATA: `${DEMO}/data`, CHRONIKA_CACHE: `${DEMO}/cache`, CHRONIKA_CONFIG: `${DEMO}/config` };

export function totp(secret: string, offset = 0) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret.replace(/[\s=]/g, "").toUpperCase()) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000) + offset));
  const h = createHmac("sha1", key).update(counter).digest();
  const o = h[h.length - 1] & 15;
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

export function setupLink() {
  const out = execFileSync("uv", ["run", "chronika", "user", "link", "--user", "1"], { cwd: "..", env }).toString();
  const url = new URL(out.split("\n")[0]);
  return url.pathname + url.hash;
}

/** Signed in as the demo's user (who exists once app.spec.ts has run): a new password and secret. */
export async function signIn(page: Page) {
  await page.goto(setupLink());
  await page.getByRole("button", { name: /κωδικός και εφαρμογή|password and an authenticator/ }).click();
  await page.locator('input[autocomplete="username"]').fill("Demo");
  await page.getByRole("button", { name: /Συνέχεια|Continue/ }).click();
  const secret = (await page.locator("code").innerText()).replace(/\s/g, "");
  const pw = "a long enough password";
  await page.locator('input[type="password"]').nth(0).fill(pw);
  await page.locator('input[type="password"]').nth(1).fill(pw);
  await page.locator('input[autocomplete="one-time-code"]').fill(totp(secret));
  await page.getByRole("button", { name: /Ορισμός κωδικού|Set a password/ }).click();
  await expect(page.getByRole("heading", { name: /Συνομιλίες|Chats/ })).toBeVisible({ timeout: 15000 });
}

/** Signed in, once per project and demo run: the session is kept and reused by the next tests (each
 *  sign-in counts against the server's limit on attempts, as it should). */
export async function signedIn(page: Page, project: string) {
  const file = `/tmp/chr-e2e-session-${project}.json`;
  if (existsSync(file)) {
    await page.context().addCookies(JSON.parse(readFileSync(file, "utf8")));
    await page.goto("/");
    const ok = await page.getByRole("heading", { name: /Συνομιλίες|Chats/ }).waitFor({ timeout: 5000 }).then(() => true, () => false);
    if (ok) return;                     // else the session is gone (a new demo): sign in again
  }
  await signIn(page);
  writeFileSync(file, JSON.stringify(await page.context().cookies()));
}
