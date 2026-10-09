import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// What can be done to a message through its service: a reaction (put, changed, taken back with a
// tap on it), an edit of the user's own (in the composer, which then has the text it had), a
// deletion for everyone (asked first). The demo's own source does them all, into the demo archive.

test("a reaction, an edit and a deletion of the user's own message", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      const s = d.sendable.find((x: string) => x !== "sms" && d.reactions?.[x] !== undefined && d.editable?.[x] !== undefined && d.deletable?.[x] !== undefined);
      if (s && c.type === "person") return { id: c.id, service: s };
    }
    return null;
  });
  test.skip(!chat, "no chat where the demo's source does all three");
  await page.goto(`/chat/${chat!.id}`);
  const via = page.locator("[data-via]");
  if ((await via.getAttribute("data-via")) !== chat!.service) {
    await via.click();
    await page.getByRole("menuitem").filter({ has: page.locator(`[data-icon="${chat!.service}"]`) }).click();
  }
  const text = `quiet: to be changed ${info.project.name} ${Date.now()}`;
  await page.locator("[data-composer-body] textarea").fill(text);
  await page.locator("[data-send]").click();
  const bubble = page.locator('[id^="m"]').filter({ hasText: text }).last();
  await expect(bubble).toBeVisible();
  await expect.poll(async () => Number((await bubble.getAttribute("id"))!.slice(1)), { timeout: 10000 }).toBeGreaterThan(0);

  // a reaction, then another in its place, then taken back by a tap on it
  await bubble.locator("[data-actions]").click();
  await page.locator('[data-react="👍"]').click();
  await expect(bubble.locator("[data-reaction][data-mine]")).toHaveText("👍");
  await bubble.locator("[data-actions]").click();
  await page.locator("[data-more-reactions]").click();
  await page.locator("[data-reaction-picker] [data-emoji-search]").fill("unicorn");    // any emoji: found in the picker
  await page.locator("[data-reaction-picker] [data-emoji-option]:visible", { hasText: "🦄" }).click();
  await expect(bubble.locator("[data-reaction][data-mine]")).toHaveText("🦄");
  await expect(bubble.locator("[data-reaction]")).toHaveCount(1);
  await bubble.locator("[data-reaction][data-mine]").click();
  await expect(bubble.locator("[data-reaction]")).toHaveCount(0);

  // an emoji written where the caret is, from the composer's picker (the one used lately first)
  const field = page.locator("[data-composer-body] textarea");
  await field.fill("hi ");
  await page.locator("[data-emoji]").click();
  await expect(page.locator("[data-emoji-recent] button").first()).toHaveText("🦄");
  await page.locator("[data-emoji-recent] button").first().click();
  await expect(field).toHaveValue("hi 🦄");
  await page.keyboard.press("Escape");
  await field.fill("");

  // an edit: the composer takes the text, then has what it had again
  const composer = page.locator("[data-composer-body] textarea");
  await composer.fill("a draft");
  await bubble.locator("[data-actions]").click();
  await page.getByRole("menuitem", { name: /Επεξεργασία|Edit/ }).click();
  await expect(page.locator("[data-editing]")).toBeVisible();
  await expect(composer).toHaveValue(text);
  await composer.fill(`${text} (fixed)`);
  await page.locator("[data-send]").click();
  await expect(page.locator("[data-editing]")).toHaveCount(0);
  await expect(composer).toHaveValue("a draft");
  const fixed = page.locator('[id^="m"]').filter({ hasText: `${text} (fixed)` });
  await expect(fixed).toContainText(/επεξεργασμένο|edited/);

  // a deletion for everyone, asked first
  await fixed.locator("[data-actions]").click();
  await page.getByRole("menuitem", { name: /Διαγραφή για όλους|Delete for everyone/ }).click();
  await page.locator("[data-confirm-delete]").click();
  // the notice only, as the service shows it; what it was, kept, on a tap
  const gone = page.locator("[data-deleted]").last();
  await expect(gone).toBeVisible();
  await expect(page.locator('[id^="m"]').filter({ hasText: `${text} (fixed)` })).toHaveCount(0);
  await gone.click();
  await expect(page.locator('[id^="m"]').filter({ hasText: `${text} (fixed)` }).locator(".italic")).toHaveCount(1);
  await composer.fill("");
});
