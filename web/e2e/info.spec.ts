import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A long message's info: its text cut at three lines, and nothing of a fourth showing below them.

test("a long message's text in its info: three lines, no part of a fourth", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes("whatsapp")) return c.id as string;
    }
    return null;
  });
  test.skip(!chat, "no chat to send to");
  await page.goto(`/chat/${chat}`);
  const words = "quiet: " + Array.from({ length: 60 }, (_, i) => `word${i}`).join(" ");
  await page.locator("[data-composer-body] textarea").fill(words);
  await page.locator("[data-send]").click();
  const sent = page.locator("[id^=m]", { hasText: "word59" }).last();
  await sent.locator("[data-ticks]").click({ timeout: 30000 });   // the first sending of a fresh demo is slower
  const text = page.locator("[data-message-info] .line-clamp-3");
  await expect(text).toBeVisible();
  // the box clamped has no padding of its own: a fourth line would show, cut, in it
  expect(await text.evaluate((el) => getComputedStyle(el).paddingBottom)).toBe("0px");
  const lines = await text.evaluate((el) => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)));
  expect(lines).toBe(3);
});
