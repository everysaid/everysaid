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
  // a group whose latest messages have one from another member (its sender's name to click): not
  // the notes to oneself, nor one the other tests filled with their own messages
  const group = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    for (const c of (await (await fetch("/api/chats?kind=group", { headers: h })).json()).items) {
      const s = await (await fetch(`/api/chats/${c.id}/stream?limit=10`, { headers: h })).json();
      if (s.items.some((m: any) => !m.outgoing && m.sender_id)) return { id: c.id as string, name: c.title as string };
    }
    return null;
  });
  expect(group, "a group with others' messages").not.toBeNull();
  await page.goto("/");
  // found by its name: the list is virtualized and the other tests' messages reorder it
  await page.getByRole("textbox", { name: /Αναζήτηση συνομιλιών|Search chats/ }).fill(group!.name);
  await page.locator(`[data-chat="${group!.id}"] a`).first().click();
  await expect(page).toHaveURL(new RegExp(`/chat/${group!.id}`));
  if (info.project.name === "desktop") await expect(page.locator("[data-back]")).toHaveCount(0);
  await page.locator("[data-sender]").last().click();
  await expect(page).toHaveURL(/\/chat\/p\d+/);
  await page.locator("[data-back]").click();
  await expect(page).toHaveURL(new RegExp(`/chat/${group!.id}`));
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
