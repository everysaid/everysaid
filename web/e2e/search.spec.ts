import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Searching as in an editor: parts of words by default, accents and case ignored; Aa: exactly as
// written; W: whole words only. The demo's messages say «Καλημέρα» in many chats.

test("search: parts of words, whole words (W), as written (Aa)", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.evaluate(() => localStorage.removeItem("search-mode"));
  await page.goto("/search");
  const box = page.getByPlaceholder(/Αναζήτηση σε όλα τα μηνύματα|Search all messages/);
  const count = page.getByText(/\d+ αποτελέσματ|\d+ results?/).first();
  const none = page.getByText(/Κανένα αποτέλεσμα|No results/);

  await box.fill("λημερ");                                            // inside «Καλημέρα»
  await expect(count).toBeVisible();
  await expect(page.locator("mark, .mark").first()).toContainText(/λημέρ/i);
  await page.locator('[data-mode="whole"]').click();                   // not a word of its own
  await expect(none).toBeVisible();
  await box.fill("καλημερα");
  await expect(count).toBeVisible();
  await page.locator('[data-mode="whole"]').click();

  await box.fill("ΚΑΛΗΜΈΡΑ");
  await expect(count).toBeVisible();                                    // case and accents ignored
  await page.locator('[data-mode="case"]').click();
  await expect(page.locator('[data-mode="case"]')).toHaveAttribute("aria-pressed", "true");
  await expect(none).toBeVisible();                                     // nobody shouts it
  await box.fill("Καλημέρα");
  await expect(count).toBeVisible();
  await page.locator('[data-mode="case"]').click();
});

test("search: where it was found, one tap keeps only that person; more comes as it scrolls", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.evaluate(() => localStorage.removeItem("search-mode"));
  await page.goto("/search");
  await page.getByPlaceholder(/Αναζήτηση σε όλα τα μηνύματα|Search all messages/).fill("καλημερα");
  const picker = page.locator("[data-found]");
  await expect(picker).toBeVisible();
  const all = await page.getByText(/^\d+ (αποτελέσματ|results?)/).first().innerText();
  await picker.click();
  const first = page.getByRole("menuitem").nth(1);                         // after "all chats"
  const n = (await first.locator("span").last().innerText()).trim();
  const who = (await first.locator("span.truncate").innerText()).trim();
  await first.click();
  await expect(page.getByText(new RegExp(`^${n} (αποτέλεσμα|αποτελέσματα|result|results)$`))).toBeVisible();
  await expect(picker).toContainText(who);
  await page.screenshot({ path: `/tmp/chr-shots/${info.project.name}-search-picked.png` });
  await picker.click();
  await page.screenshot({ path: `/tmp/chr-shots/${info.project.name}-search-menu.png` });
  await page.getByRole("menuitem").first().click();                         // all chats again
  await expect(page.getByText(all, { exact: true })).toBeVisible();
  // no "more" button: the list loads as it comes near its end
  await expect(page.locator("[data-results]").getByRole("button", { name: /^(Περισσότερα|More)$/ })).toHaveCount(0);
  const before = await page.locator("a[href^='/chat/']").count();
  await page.locator("a[href^='/chat/']").last().scrollIntoViewIfNeeded();
  await expect.poll(() => page.locator("a[href^='/chat/']").count(), { timeout: 10000 }).toBeGreaterThan(before);
});

test("a person's calls and media, from their info: only theirs", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  // a person with calls
  const { id, title } = await page.evaluate(async () => {
    const h = { "X-Chronika": "1" };
    const calls = (await (await fetch("/api/calls?limit=50", { headers: h })).json()).items;
    const c = calls.find((x: any) => x.chat_id);
    const d = await (await fetch(`/api/chats/${c.chat_id}`, { headers: h })).json();
    return { id: c.chat_id as string, title: d.title as string };
  });
  await page.goto(`/chat/${id}`);
  await page.getByRole("button", { name: /^(Πληροφορίες|Info)$/ }).click();
  await page.locator(`a[href*="/calls?chat=${id}"]`).click();
  await expect(page.locator("[data-chat-filter]")).toContainText(title);
  const names = await page.evaluate(async (chat) => {
    const r = await (await fetch(`/api/calls?chat=${chat}&limit=200`, { headers: { "X-Chronika": "1" } })).json();
    return [...new Set(r.items.map((c: any) => c.chat_id))];
  }, id);
  expect(names).toEqual([id]);
  await page.locator("[data-chat-filter] button").click();                   // all calls again
  await expect(page.locator("[data-chat-filter]")).toHaveCount(0);
  await expect(page).toHaveURL(/\/calls$/);
});
