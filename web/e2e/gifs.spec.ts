import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// GIFs: the demo's searcher finds its invented ones by name, a tap sends one as a GIF, which is then
// among those of the archive ("Mine"); a searcher that cannot now is there, greyed, saying why.

async function sendableChat(page: import("@playwright/test").Page) {
  return page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes("whatsapp") && d.fileable?.includes("whatsapp")) return c.id as string;
    }
    return null;
  });
}

test("a GIF found, sent with a tap, then among those of the archive", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  await page.goto(`/chat/${chat}`);
  await page.locator("[data-via]").click();                 // through WhatsApp, which takes files
  await page.getByRole("menuitem", { name: "WhatsApp" }).click();
  await page.locator("[data-gif]").click();
  const picker = page.locator("[data-gif-picker]");
  await expect(picker.locator("[data-gif-option]")).toHaveCount(3);     // the searcher's tab first, all of them
  await picker.locator("[data-gif-search]").fill("par");
  await expect(picker.locator("[data-gif-option]")).toHaveCount(1);
  await expect(picker.locator("[data-gif-option] video")).toHaveCount(1);

  const sent = page.waitForResponse((r) => r.url().includes(`/api/chats/${chat}/send`) && r.request().method() === "POST");
  await picker.locator("[data-gif-option]").click();
  expect((await sent).ok()).toBe(true);
  // it went as a GIF (the form's body cannot be read here: the archive says it)
  await expect.poll(() => page.evaluate(async (id) => {
    const r = await (await fetch(`/api/chats/${id}/stream?limit=5`, { headers: { "X-Everysaid": "1" } })).json();
    const mine = r.items.filter((i: { type: string; outgoing: boolean }) => i.type === "message" && i.outgoing);
    return mine[mine.length - 1]?.subtype;
  }, chat)).toBe("gif");
  await expect(picker).toHaveCount(0);                                     // closed once picked
  await expect(page.locator('[id^="m"]').last().locator("video")).toBeVisible();

  await page.locator("[data-gif]").click();
  await picker.getByRole("tab", { name: "Mine" }).or(picker.getByRole("tab", { name: "Δικά μου" })).click();
  await expect(picker.locator("[data-gif-option]").first()).toBeVisible();
});

test("a searcher that cannot now: greyed, saying why", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.route("**/api/gifs", (r) => r.fulfill({ json: { items: [{ instance: 999, name: "Telegram", why: "Not signed in to Telegram yet" }] } }));
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  await page.goto(`/chat/${chat}`);
  await page.locator("[data-gif]").click();
  const picker = page.locator("[data-gif-picker]");
  await expect(picker.locator("[data-gif-tab='999']")).toHaveClass(/opacity-50/);
  await picker.locator("[data-gif-tab='999']").click();
  await expect(picker.locator("[data-gif-unavailable]")).toContainText("Not signed in to Telegram yet");
});
