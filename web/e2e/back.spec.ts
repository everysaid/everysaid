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

// From a group into a sender's own chat and back to the group, on every screen; a chat opened from
// the list of chats beside it (a wide screen) has none, the list being the way.
test("back: from a sender's chat to the group", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const groups = await page.evaluate(async () =>
    (await (await fetch("/api/chats?kind=group", { headers: { "X-Everysaid": "1" } })).json()).items.map((c: any) => c.id as string));
  test.skip(!groups.length, "no group");
  await page.goto("/");
  await page.locator("[data-chat]").first().waitFor();
  // the list is virtualized: take the first group that is on the first screen
  const group = (await page.locator("[data-chat]").evaluateAll((els) => els.map((e) => e.getAttribute("data-chat")))).find((id) => groups.includes(id!)) ?? groups[0];
  await page.locator(`[data-chat="${group}"] a`).first().click();
  await expect(page).toHaveURL(new RegExp(`/chat/${group}`));
  if (info.project.name === "desktop") await expect(page.locator("[data-back]")).toHaveCount(0);
  await page.locator("[data-sender]").last().click();
  await expect(page).toHaveURL(/\/chat\/p\d+/);
  await page.locator("[data-back]").click();
  await expect(page).toHaveURL(new RegExp(`/chat/${group}`));
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
