import { expect, test } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { signedIn } from "./demo";

// The services' photos of the demo's invented people and groups: a group's photo in the chat list,
// the choice of where a person's photo comes from (then back to automatic), and a person's photos
// with the older ones.

const SHOTS = process.env.SHOTS ?? "/tmp/chr-shots";
mkdirSync(SHOTS, { recursive: true });

test("photos: a group's in the list, a person's choice and history", async ({ page }, info) => {
  const tag = info.project.name;
  await signedIn(page, tag);
  const found = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats?limit=500", { headers: h })).json()).items;
    const group = chats.find((c: any) => c.type === "group" && c.avatar);
    let chosen = null, history = null;
    for (const c of chats.filter((c: any) => c.type === "person" && c.avatar)) {
      const p = await (await fetch(`/api/people/${c.person_id}`, { headers: h })).json();
      if (!chosen && p.photo_choices.length > 1) chosen = c.person_id;
      if (!history && p.photo_history.length > 1) history = c.person_id;
    }
    return { group: group?.id ?? null, chosen, history };
  });
  expect(found.group).not.toBeNull();
  expect(found.chosen).not.toBeNull();
  expect(found.history).not.toBeNull();

  // the group's photo where the chats are listed
  await page.goto("/");
  await expect(page.locator(`img[src^="/api/chats/${found.group}/avatar"]`).first()).toBeVisible();

  // a person with an address book's photo and a service's: one chosen, then automatic again
  await page.goto(`/chat/p${found.chosen}`);
  await page.getByRole("button", { name: /Πληροφορίες|Info/ }).first().click();
  const from = page.locator("[data-photo-from]").last();
  await from.scrollIntoViewIfNeeded();
  await expect(from.getByRole("button")).toHaveCount(4);            // automatic, contacts, the service, none
  await page.screenshot({ path: `${SHOTS}/${tag}-photo-from.png` });
  await from.getByRole("button", { name: /Χωρίς φωτογραφία|No photo/ }).click();
  await expect(from.getByRole("button", { name: /Χωρίς φωτογραφία|No photo/ })).toHaveAttribute("aria-pressed", "true");
  await from.getByRole("button", { name: /Αυτόματα|Automatic/ }).click();
  await expect(from.getByRole("button", { name: /Αυτόματα|Automatic/ })).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Escape");

  // a person whose photo changed: both in their card
  await page.goto(`/people/${found.history}`);
  const history = page.locator("[data-photo-history]");
  await history.scrollIntoViewIfNeeded();
  await expect(history.locator("img")).toHaveCount(2);
  await page.screenshot({ path: `${SHOTS}/${tag}-photo-history.png` });
});
