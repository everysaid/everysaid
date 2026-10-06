import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// The way to answer: where the chat was last active, even where nothing can send now (a lock then
// takes Send's place, and says why); any other of the chat's services is a choice away, each with
// its icon. The demo's own source sends everywhere but iMessage.

test("answers where the chat was last active, with a lock where it cannot send", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Chronika": "1" };
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
  await expect(items.filter({ has: page.locator("[data-icon]") })).toHaveCount(await items.count());
  await items.filter({ hasText: (await page.evaluate(async (s) => {
    const h = { "X-Chronika": "1" };
    return (await (await fetch("/api/services", { headers: h })).json())[s].name;
  }, chat!.other)) }).click();
  await expect(via).toHaveAttribute("data-via", chat!.other);
  await expect(page.locator("[data-send]")).toBeVisible();
  await expect(page.locator("[data-locked]")).toHaveCount(0);
  await expect(page.locator("[data-composer-body] textarea")).toBeEnabled();
});
