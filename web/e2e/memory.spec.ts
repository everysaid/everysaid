import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// What a chat keeps while the user is elsewhere: the unsent text, where it was being read; and the
// Chats tab (on a wide screen) comes back to the chat open last.

test("a chat keeps its unsent text and its place; the Chats tab comes back to it", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "the tabs beside a chat are the wide screen's");
  await signedIn(page, info.project.name);
  const [a, b] = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    const out = [];
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes(d.last_service) && d.type === "person" && d.person.stats.messages > 150) out.push(c.id);
      if (out.length === 2) break;
    }
    return out;
  });
  const field = page.locator("[data-composer-body] textarea");
  const scroller = "[data-stream] [data-virtuoso-scroller]";
  await page.goto(`/chat/${a}`);
  await expect(page.locator(scroller)).toBeVisible();
  await page.waitForTimeout(800);
  await field.fill("half written");
  await page.evaluate((s) => { document.querySelector(s)!.scrollTop -= 2500; }, scroller);
  await page.waitForTimeout(800);
  const topText = await page.evaluate((s) => {
    const sc = document.querySelector(s)!.getBoundingClientRect();
    const rows = [...document.querySelectorAll("[data-stream] [id^=m]")] as HTMLElement[];
    return rows.find((r) => r.getBoundingClientRect().bottom > sc.top + 40)?.id ?? "";
  }, scroller);

  await page.goto(`/chat/${b}`);
  await expect(field).toHaveValue("");
  await page.locator("[data-show-archived]").click();                // the list as it is left
  await page.locator('nav a[href="/overview"]').click();
  await page.locator('nav a[aria-label="Συνομιλίες"], nav a[aria-label="Chats"]').click();
  await expect(page).toHaveURL(new RegExp(`/chat/${b}`));          // the chat open last
  await expect(page.locator("[data-show-archived]")).toHaveAttribute("aria-pressed", "true");
  await page.locator("[data-show-archived]").click();

  await page.goto(`/chat/${a}`);
  await expect(field).toHaveValue("half written");
  await page.waitForTimeout(1200);
  await expect(page.locator(`[id="${topText}"]`)).toBeInViewport();  // where it was left
  await field.fill("");

  // left far up (older pages loaded), then back: the latest comes with the button, in place
  for (let i = 0; i < 8; i++) {
    await page.evaluate((s) => { document.querySelector(s)!.scrollTop = 0; }, scroller);
    await page.waitForTimeout(400);
  }
  await page.goto(`/chat/${b}`);
  await page.goto(`/chat/${a}`);
  const latest = await page.evaluate(async (id) => {
    const p = await (await fetch(`/api/chats/${id}/stream?limit=5`, { headers: { "X-Everysaid": "1" } })).json();
    return `m${p.items.filter((i: any) => i.type === "message").at(-1).id}`;
  }, a);
  await expect(page.locator("[data-latest]")).toBeVisible();
  await expect(page.locator(`[id="${latest}"]`)).toHaveCount(0);       // not loaded yet
  await page.locator("[data-latest]").click();
  await expect(page.locator(`[id="${latest}"]`)).toBeInViewport();
});
