import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Two groups merged from a group's info are one chat, made of both; each can leave again (and does,
// so the demo stays as it was for the other tests).

test("groups: merge two into one chat, then split them again", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const [a, b] = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const items = (await (await fetch("/api/chats?kind=group&archived=true", { headers: h })).json()).items;
    return items.filter((c: any) => c.type === "group").slice(0, 2).map((c: any) => ({ id: c.id as string, title: c.title as string }));
  });
  await page.goto(`/chat/${a.id}`);
  await page.getByRole("button", { name: /^(Πληροφορίες|Info)$/ }).click();
  await expect(page.locator("[data-group-part]")).toHaveCount(1);
  await page.locator("[data-merge-group]").click();
  await page.getByPlaceholder(/Αναζήτηση ομάδας|Search groups/).fill(b.title);
  await page.getByRole("dialog").last().getByRole("button", { name: /^(Συγχώνευση|Merge)$/ }).first().click();
  await expect(page.locator("[data-group-part]")).toHaveCount(2);
  const made = await page.evaluate(async (id) =>
    (await (await fetch(`/api/chats/${id}`, { headers: { "X-Everysaid": "1" } })).json()).conversations.length, a.id);
  expect(made).toBe(2);

  await page.locator("[data-split-group]").last().click();
  await expect(page.locator("[data-group-part]")).toHaveCount(1);
  const back = await page.evaluate(async (ids) => Promise.all(ids.map(async (id) =>
    (await fetch(`/api/chats/${id}`, { headers: { "X-Everysaid": "1" } })).status)), [a.id, b.id]);
  expect(back).toEqual([200, 200]);                                   // both chats as they were
});
