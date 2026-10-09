import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// The way to answer: where the chat was last active, even where nothing can send now (a lock then
// takes Send's place, and says why); any other of the chat's services is a choice away, each with
// its icon. The demo's own source sends everywhere but iMessage.

test("answers where the chat was last active, with a lock where it cannot send", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.last_service && !d.sendable.includes(d.last_service) && d.sendable.length) return { id: c.id, last: d.last_service, other: d.sendable[0] };
    }
    return null;
  });
  test.skip(!chat, "no chat last active where nothing can send");
  await page.goto(`/chat/${chat!.id}`);
  const via = page.locator("[data-via]");
  await expect(via).toHaveAttribute("data-via", chat!.last);
  await expect(via.locator(`[data-icon="${chat!.last}"]`)).toHaveCount(1);   // the service's own icon
  await expect(page.locator("[data-locked]")).toBeVisible();
  await expect(page.locator("[data-send]")).toHaveCount(0);
  await expect(page.locator("[data-composer-body] textarea")).toBeDisabled();   // nothing to write there
  await page.locator("[data-locked]").click();
  await expect(page.locator("[data-sonner-toast]")).toBeVisible();      // it says why, and where to look

  await via.click();
  const items = page.getByRole("menuitem");
  await expect(items.first()).toBeVisible();
  const choices = items.filter({ hasNotText: /Αναζήτηση σε άλλες υπηρεσίες|Look for them on other services/ });   // a person's chat: one more, to look for them
  await expect(items.filter({ has: page.locator("[data-icon]") })).toHaveCount(await choices.count());
  await items.filter({ hasText: (await page.evaluate(async (s) => {
    const h = { "X-Everysaid": "1" };
    return (await (await fetch("/api/services", { headers: h })).json())[s].name;
  }, chat!.other)) }).click();
  await expect(via).toHaveAttribute("data-via", chat!.other);
  await expect(page.locator("[data-send]")).toBeVisible();
  await expect(page.locator("[data-locked]")).toHaveCount(0);
  await expect(page.locator("[data-composer-body] textarea")).toBeEnabled();
});

test("a person's services, all on at first: one turned off leaves its messages out, and comes back", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    const looks = await (await fetch("/api/services", { headers: h })).json();
    return chats.find((c: any) => c.type === "person" && c.services.filter((s: string) => looks[s]?.messages).length > 1);
  });
  await page.goto(`/chat/${chat.id}`);
  const toggles = page.locator("[data-service-toggle]");
  await expect(toggles).toHaveCount(chat.services.length);
  for (const s of chat.services) await expect(page.locator(`[data-service-toggle="${s}"]`)).toHaveAttribute("aria-pressed", "true");
  const off = chat.services[0];
  await page.locator(`[data-service-toggle="${off}"]`).click();
  await expect(page).toHaveURL(new RegExp(`hide=${off}`));
  await expect(page.locator(`[data-service-toggle="${off}"]`)).toHaveAttribute("aria-pressed", "false");   // only that one
  for (const s of chat.services.slice(1)) await expect(page.locator(`[data-service-toggle="${s}"]`)).toHaveAttribute("aria-pressed", "true");
  const shown = await page.evaluate(async ([id, s]) => {
    const r = await fetch(`/api/chats/${id}/stream?hide=${s}&limit=80`, { headers: { "X-Everysaid": "1" } });
    return (await r.json()).items.map((i: any) => i.service);
  }, [chat.id, off]);
  expect(shown).not.toContain(off);
  // the last one on stays on
  for (const s of chat.services.slice(1, -1)) await page.locator(`[data-service-toggle="${s}"]`).click();
  await expect(page.locator(`[data-service-toggle="${chat.services.at(-1)}"]`)).toBeDisabled();
  for (const s of chat.services.slice(0, -1)) await page.locator(`[data-service-toggle="${s}"]`).click();   // all back on
  await expect(page).not.toHaveURL(/hide=/);
});

// A person looked for on the services their chat has none of (the demo's source finds every number on
// WhatsApp and Viber): each answer shows as it comes, and a first message there brings the service in.
test("a person found on another service, and a first message there", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      if (c.type !== "person" || c.services.includes("viber")) continue;
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.findable) return { id: c.id };
    }
    return null;
  });
  test.skip(!chat, "no person to look for");
  await page.goto(`/chat/${chat!.id}`);
  const via = page.locator("[data-via]");
  await via.click();
  await page.getByRole("menuitem", { name: /Αναζήτηση σε άλλες υπηρεσίες|Look for them on other services/ }).click();
  const viber = await page.evaluate(async () => (await (await fetch("/api/services", { headers: { "X-Everysaid": "1" } })).json()).viber.name);
  await expect.poll(async () => {
    await via.click();
    const n = await page.getByRole("menuitem", { name: viber }).count();
    await page.keyboard.press("Escape");
    return n;
  }, { timeout: 10_000 }).toBe(1);
  await via.click();
  await page.getByRole("menuitem", { name: viber }).click();
  await expect(via).toHaveAttribute("data-via", "viber");
  await page.locator("[data-composer-body] textarea").fill("quiet: a first message");
  await page.locator("[data-send]").click();
  await expect(page.locator('[id^="m-"]').getByText("quiet: a first message")).toBeVisible();
  await expect.poll(async () => page.evaluate(async (id) =>
    (await (await fetch(`/api/chats/${id}`, { headers: { "X-Everysaid": "1" } })).json()).services.includes("viber"), chat!.id)).toBe(true);
});
