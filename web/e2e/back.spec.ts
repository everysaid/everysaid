import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Every page reached from another has a way back to it, on every screen; a page opened from the
// menu has none (the menu is the way), and one opened directly goes back to its section.

test("back: to where a page was reached from", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people");
  await expect(page.locator("[data-back]")).toHaveCount(0);
  await page.locator("[data-person]").first().click();
  await expect(page).toHaveURL(/\/people\/\d+$/);
  await page.locator("[data-back]").click();
  await expect(page).toHaveURL(/\/people$/);
  // from a person's page into the chat, and back to the person
  await page.locator("[data-person]").first().click();
  const person = page.url();
  await page.getByRole("button", { name: /Άνοιγμα συνομιλίας|Open chat/ }).click();
  await expect(page).toHaveURL(/\/chat\/p\d+/);
  await page.locator("[data-back]").click();
  await expect(page).toHaveURL(person);
  // opened directly: back to its section
  await page.goto("/people/unnamed");
  await page.locator("[data-back]").click();
  await expect(page).toHaveURL(/\/people$/);
});

test("sources: one tab for each kind of plugin", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/sources");
  await expect(page.getByText("Demo phone").first()).toBeVisible();
  await page.getByRole("tab", { name: /Φωτογραφίες|Pictures/ }).click();
  await expect(page).toHaveURL(/tab=library/);
  await expect(page.getByText("Photos folder")).toBeVisible();
  await expect(page.getByText("Demo phone")).toHaveCount(0);
  await page.getByRole("tab", { name: /Συσκευές|Devices/ }).click();
  await expect(page.getByText("demo-phone").first()).toBeVisible();
});
