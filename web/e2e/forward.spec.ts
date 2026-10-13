import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A message forwarded from its menu to chats chosen in a window: to one with its service by the
// service's own forward (shown forwarded), to one without it as a message of its own.

test("a message forwarded to a chat of its service and to one of another", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chats = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const all = (await (await fetch("/api/chats", { headers: h })).json()).items;
    const wa: { id: string; title: string }[] = [];
    let other: { id: string; title: string } | null = null;
    for (const c of all) {
      if (c.type !== "person") continue;
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (c.services.includes("whatsapp") && d.sendable.includes("whatsapp")) wa.push({ id: c.id, title: c.title });
      else if (!c.services.includes("whatsapp") && d.sendable.length && !other) other = { id: c.id, title: c.title };
      if (wa.length >= 2 && other) return { from: wa[0], same: wa[1], other };
    }
    return null;
  });
  test.skip(!chats, "no chats to forward between");
  const { from, same, other } = chats!;
  await page.goto(`/chat/${from.id}`);
  const via = page.locator("[data-via]");
  if ((await via.getAttribute("data-via")) !== "whatsapp") {
    await via.click();
    await page.getByRole("menuitem", { name: "WhatsApp" }).click();
  }
  const text = `quiet: forward me ${info.project.name} ${Date.now()}`;
  await page.locator("[data-composer-body] textarea").fill(text);
  await page.locator("[data-send]").click();
  const bubble = page.locator('[id^="m"]').filter({ hasText: text }).last();
  await expect.poll(async () => Number((await bubble.getAttribute("id"))!.slice(1)), { timeout: 10000 }).toBeGreaterThan(0);

  await bubble.locator("[data-actions]").click();
  await page.getByRole("menuitem", { name: /Προώθηση|Forward/ }).click();
  await expect(page.locator("[data-forward-preview]")).toContainText(text);     // what is forwarded, shown
  const search = page.getByPlaceholder(/Αναζήτηση συνομιλίας|Search chats/);
  for (const c of [same, other]) {
    await search.fill(c.title);
    await page.locator(`[data-forward-chat="${c.id}"]`).click();
    await expect(page.locator(`[data-forward-chat="${c.id}"]`)).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator(`[data-forward-via="${c.id}"] [aria-pressed="true"]`)).toHaveCount(1);   // through one service
    // the message's own service first where the chat has it
    if (c === same) await expect(page.locator(`[data-forward-via="${c.id}"] [data-service="whatsapp"]`)).toHaveAttribute("aria-pressed", "true");
  }
  await page.locator("[data-forward-send]").click();
  await expect(page.locator("[data-sonner-toast]").filter({ hasText: /2/ })).toBeVisible();

  // in the chat of its service: forwarded; in the other: a message of its own
  await page.goto(`/chat/${same.id}`);
  const there = page.locator('[id^="m"]').filter({ hasText: text });
  await expect(there).toHaveCount(1);
  await expect(there).toContainText(/Προωθημένο|Forwarded/);
  await page.goto(`/chat/${other.id}`);
  const copy = page.locator('[id^="m"]').filter({ hasText: text });
  await expect(copy).toHaveCount(1);
  await expect(copy).not.toContainText(/Προωθημένο|Forwarded/);
});
