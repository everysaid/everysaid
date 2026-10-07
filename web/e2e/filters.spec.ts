import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// The chat list's filters, in one small panel: the least messages (in growing steps), a service a
// chat must have or must not have, and back to all of it; kept on this device.

test("filters: the chat list narrowed, and back", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/");
  const rows = page.locator("[data-chat]");
  const shown = () => rows.evaluateAll((r) => r.map((x) => (x as HTMLElement).dataset.chat));
  await expect(rows.first()).toBeVisible();
  const all = await page.evaluate(async () => (await (await fetch("/api/chats", { headers: { "X-Everysaid": "1" } })).json()).items.length);
  await page.locator("[data-chat-filters]").click();
  const panel = page.locator("[data-filters-panel]");
  const slider = panel.locator("[data-min-messages]");
  await slider.fill("8");                                              // 100
  await expect(panel.locator("[data-min-shown]")).toHaveText("100+");
  await panel.locator("[data-service-filter=whatsapp]").click();        // with WhatsApp
  await expect(panel.locator("[data-service-filter=whatsapp]")).toHaveAttribute("data-state", "with");
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-chat-filters]")).toContainText("2");
  // the list is the server's, filtered: the same chats, in the same order
  const want = await page.evaluate(async () => (await (await fetch("/api/chats?min_messages=100&services=whatsapp",
    { headers: { "X-Everysaid": "1" } })).json()).items.map((c: any) => c.id));
  expect(want.length).toBeGreaterThan(0);
  expect(want.length).toBeLessThan(all);
  await expect.poll(async () => shown().then((ids) => ids.length > 0 && ids.join() === want.slice(0, ids.length).join())).toBe(true);
  // at most: the small chats instead
  await page.locator("[data-chat-filters]").click();
  await panel.getByRole("tab", { name: /^(το πολύ|at most)$/ }).click();
  await expect(panel.locator("[data-min-shown]")).toHaveText("≤ 100");
  await page.keyboard.press("Escape");
  const few = await page.evaluate(async () => (await (await fetch("/api/chats?max_messages=100&services=whatsapp",
    { headers: { "X-Everysaid": "1" } })).json()).items.map((c: any) => c.id));
  await expect.poll(async () => shown().then((ids) => ids.length > 0 && ids.join() === few.slice(0, ids.length).join())).toBe(true);
  await page.reload();                                                 // kept on this device
  await expect(page.locator("[data-chat-filters]")).toContainText("2");
  await page.locator("[data-chat-filters]").click();
  await page.locator("[data-filters-clear]").click();
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-chat-filters]")).not.toContainText("2");
});

// Settings in tabs; a service hidden there shows nowhere, and comes back.
test("settings: tabs, and a service hidden everywhere", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/settings");
  await page.getByRole("tab", { name: /^(Υπηρεσίες|Services)$/ }).click();
  await expect(page).toHaveURL(/tab=services/);
  const shown = page.locator("[data-service-shown=whatsapp]").getByRole("switch");
  await expect(shown).toHaveAttribute("aria-checked", "true");
  await shown.click();
  await expect(shown).toHaveAttribute("aria-checked", "false");
  const services = await page.evaluate(async () => (await (await fetch("/api/chats", { headers: { "X-Everysaid": "1" } })).json())
    .items.flatMap((c: any) => c.services));
  expect(services).not.toContain("whatsapp");
  await shown.click();
  await expect(shown).toHaveAttribute("aria-checked", "true");
});

// A chat's state from its info: archived there, then back.
test("chat info: archived and back", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () =>
    (await (await fetch("/api/chats", { headers: { "X-Everysaid": "1" } })).json()).items.find((c: any) => c.type === "person").id);
  await page.goto(`/chat/${chat}`);
  await page.locator("[data-chat-info]").click();
  const archived = page.locator("[data-state-field=archived]");
  await archived.getByRole("tab", { name: /^(Ναι|Yes)$/ }).click();
  await expect.poll(async () => page.evaluate(async (id) =>
    (await (await fetch(`/api/chats/${id}`, { headers: { "X-Everysaid": "1" } })).json()).archived, chat)).toBe(true);
  await archived.getByRole("tab", { name: /^(Όχι|No)$/ }).click();
  await expect.poll(async () => page.evaluate(async (id) =>
    (await (await fetch(`/api/chats/${id}`, { headers: { "X-Everysaid": "1" } })).json()).archived, chat)).toBe(false);
});
