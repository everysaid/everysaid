import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";

const SHOTS = process.env.SHOTS ?? "/tmp/chr-shots";
mkdirSync(SHOTS, { recursive: true });
const DEMO = process.env.DEMO_DIR ?? "/tmp/chr-demo";
const env = { ...process.env, CHRONIKA_DATA: `${DEMO}/data`, CHRONIKA_CACHE: `${DEMO}/cache`, CHRONIKA_CONFIG: `${DEMO}/config` };

function setupLink(existing: boolean) {
  const out = execFileSync("uv", ["run", "chronika", "user", "link", ...(existing ? ["--user", "1"] : [])], { cwd: "..", env }).toString();
  return new URL(out.split("\n")[0]).pathname + new URL(out.split("\n")[0]).hash;
}

function hasUser() {
  return execFileSync("uv", ["run", "chronika", "user", "list"], { cwd: "..", env }).toString().startsWith("1\t");
}

async function authenticator(page: Page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });
}

test("setup, chats, a chat, search, media, sources, settings", async ({ page }, info) => {
  const tag = info.project.name;
  await authenticator(page);
  const existing = hasUser();
  await page.goto(setupLink(existing));
  await page.getByLabel(/όνομά σου|Your name/).fill("Demo");
  await page.screenshot({ path: `${SHOTS}/${tag}-01-setup.png` });
  await page.getByRole("button", { name: /passkey/ }).click();
  if (!existing) {
    await expect(page.getByText(/κωδικοί ανάκτησ|recovery codes/i)).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/${tag}-02-codes.png` });
    await page.getByRole("button", { name: /φύλαξα|saved them/ }).click();
  }
  await expect(page.getByRole("heading", { name: /Συνομιλίες|Chats/ })).toBeVisible({ timeout: 15000 });
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOTS}/${tag}-03-chats.png` });

  // a person's chat
  const first = page.locator('a[href^="/chat/p"]').first();
  await first.click();
  await expect(page.locator("[id^=m]").first()).toBeVisible();
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOTS}/${tag}-04-chat.png` });
  // the page fits the window: nothing below its edge (the composer or the read-only note whole, with room)
  const fit = await page.evaluate(() => {
    const box = document.querySelector("[data-composer-body]")?.getBoundingClientRect();
    return { doc: document.documentElement.scrollHeight, win: innerHeight, bottom: box?.bottom ?? Infinity };
  });
  expect(fit.doc).toBeLessThanOrEqual(fit.win);
  expect(fit.win - fit.bottom).toBeGreaterThanOrEqual(6);     // room below the field or the note

  // older pages load when scrolling up
  const before = await page.locator("[id^=m]").count();
  for (let i = 0; i < 6; i++) {
    await page.mouse.move(tag === "mobile" ? 200 : 900, 300);
    await page.mouse.wheel(0, -4000);
    await page.waitForTimeout(400);
  }
  await page.screenshot({ path: `${SHOTS}/${tag}-05-chat-older.png` });
  expect(await page.locator("[id^=m]").count()).toBeGreaterThan(0);
  console.log(`[${tag}] bubbles before scroll ${before}`);

  // info panel
  await page.getByRole("button", { name: /Πληροφορίες|Info/ }).first().click();
  await page.waitForTimeout(500);
  await page.screenshot({ path: `${SHOTS}/${tag}-06-info.png` });
  const states = page.getByText(/^(Κατάσταση|State)$/).last();
  await states.scrollIntoViewIfNeeded();
  await expect(page.getByText(/^(Αρχειοθετημένη|Archived)$/).last()).toBeVisible();
  await page.screenshot({ path: `${SHOTS}/${tag}-06b-states.png` });
  await page.keyboard.press("Escape");

  // a group
  await page.goto("/");
  if (tag === "desktop") {
    await page.getByRole("tab", { name: /Ομάδες|Groups/ }).click();
    await page.locator('a[href^="/chat/c"]').first().click();
    await page.waitForTimeout(800);
    await page.screenshot({ path: `${SHOTS}/${tag}-07-group.png` });
  }

  // search, then open a result
  await page.goto("/search?q=καλημερα");
  await expect(page.getByText(/αποτελέσματα|results/)).toBeVisible();
  await page.screenshot({ path: `${SHOTS}/${tag}-08-search.png` });
  await page.locator('a[href*="/chat/"]').first().click();
  await expect(page.locator(".flash").first()).toBeVisible({ timeout: 10000 });
  await page.screenshot({ path: `${SHOTS}/${tag}-09-search-hit.png` });

  for (const [path, name] of [["/media", "10-media"], ["/calls", "11-calls"], ["/people", "12-people"], ["/sources", "13-sources"],
    ["/settings", "14-settings"], ["/overview", "15-overview"]] as const) {
    await page.goto(path);
    await page.waitForTimeout(900);
    await page.screenshot({ path: `${SHOTS}/${tag}-${name}.png` });
  }
  // a picture in the lightbox
  await page.goto("/media");
  await page.locator("img[src*='/thumb']").first().click();
  await page.waitForTimeout(700);
  await page.screenshot({ path: `${SHOTS}/${tag}-16-lightbox.png` });
  await page.keyboard.press("Escape");

  // a reaction sits on the bubble's edge, whole, above it
  const withReaction = await page.evaluate(async () => {
    const h = { "X-Chronika": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const page = await (await fetch(`/api/chats/${c.id}/stream?limit=200`, { headers: h })).json();
      if (page.items.some((i: any) => i.reactions?.length)) return c.id;
    }
    return null;
  });
  if (withReaction) {
    await page.goto(`/chat/${withReaction}`);
    await page.waitForTimeout(1000);                  // the list settles at the latest first
    await page.locator("[data-reaction]").first().evaluate((el) => el.scrollIntoView({ block: "center" }));
    await page.waitForTimeout(500);
    // a chip on screen: its top edge (over the bubble) is the chip itself, not the bubble
    const seen = await page.evaluate(() => {
      const chips = [...document.querySelectorAll("[data-reaction]")].map((el) => el.getBoundingClientRect())
        .filter((r) => r.top > 0 && r.bottom < innerHeight);
      const r = chips[0];
      if (!r) return null;
      const top = !!document.elementFromPoint(r.x + r.width / 2, r.y + 3)?.closest("[data-reaction]");
      return { top, x: r.x, y: r.y };
    });
    expect(seen?.top).toBe(true);
    const box = { x: seen!.x, y: seen!.y };
    await page.screenshot({ path: `${SHOTS}/${tag}-19-reaction.png`,
      clip: { x: Math.max(0, box.x - 260), y: Math.max(0, box.y - 80), width: 400, height: 130 } });
  }

  // the order of name sources: the address book first, and it can move (and back)
  await page.goto("/settings");
  const topName = page.getByText(/^1(Επαφές|Contacts|WhatsApp)/);
  await expect(topName).toHaveText(/Επαφές|Contacts/);
  await page.getByRole("button", { name: /^Κάτω$|^Down$/ }).first().click();
  await expect(topName).toHaveText(/WhatsApp/);
  await page.getByRole("button", { name: /^Πάνω$|^Up$/ }).nth(1).click();
  await expect(topName).toHaveText(/Επαφές|Contacts/);

  // dark theme
  await page.getByRole("tab", { name: /Σκοτεινό|Dark/ }).click();
  await page.goto("/");
  await page.locator('a[href^="/chat/p"]').nth(1).click();
  await page.waitForTimeout(800);
  await page.screenshot({ path: `${SHOTS}/${tag}-17-dark-chat.png` });

  // sign out, then back in with the passkey
  await page.goto("/settings");
  await page.getByRole("button", { name: /^Αποσύνδεση$|^Sign out$/ }).last().click();
  await expect(page.getByRole("button", { name: /Σύνδεση με passkey|Sign in with a passkey/ })).toBeVisible();
  await page.screenshot({ path: `${SHOTS}/${tag}-18-login.png` });
  await page.getByRole("button", { name: /Σύνδεση με passkey|Sign in with a passkey/ }).click();
  await expect(page.getByRole("heading", { name: /Συνομιλίες|Chats/ })).toBeVisible({ timeout: 15000 });
});
