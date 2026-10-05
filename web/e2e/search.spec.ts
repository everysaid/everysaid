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
