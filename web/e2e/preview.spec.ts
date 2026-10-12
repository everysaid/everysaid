import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A link's preview, as its service sent it: a card in the bubble with the site, title and
// description, its picture from the archive, the whole card a link to the page; one without a
// picture shows its words alone.

const found = (page: import("@playwright/test").Page, q: string) => page.evaluate(async (text) => {
  const r = await (await fetch(`/api/search?q=${encodeURIComponent(text)}`, { headers: { "X-Everysaid": "1" } })).json();
  return { chat: r.items[0].chat_id as string, id: r.items[0].id as number };
}, q);

test("a link's preview: its card, its picture, a link to the page", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const recipe = await found(page, "εύκολη");
  await page.goto(`/chat/${recipe.chat}?m=${recipe.id}`);
  const card = page.locator(`#m${recipe.id} [data-preview]`);
  await expect(card).toBeVisible();
  await expect(card).toContainText("Συνταγές");
  await expect(card).toContainText("Φακές με δυόσμο");
  await expect(card).toHaveAttribute("href", "https://example.org/syntages/fakes");
  await expect(card).toHaveAttribute("target", "_blank");
  const img = card.locator("img");
  await expect(img).toHaveAttribute("src", /^\/api\/previews\/[0-9a-f]{64}$/);
  await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth)).toBeGreaterThan(0);

  const concert = await found(page, "Πάμε;");
  await page.goto(`/chat/${concert.chat}?m=${concert.id}`);
  const words = page.locator(`#m${concert.id} [data-preview]`);
  await expect(words).toContainText("Συναυλία στο Ηρώδειο");
  await expect(words.locator("img")).toHaveCount(0);
});
