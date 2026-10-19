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

test("photos: the main photo and the history's open whole, the arrows go on, the black closes", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const found = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats?limit=500", { headers: h })).json()).items;
    let several = null, history = null;
    for (const c of chats.filter((c: any) => c.type === "person" && c.avatar)) {
      const p = await (await fetch(`/api/people/${c.person_id}`, { headers: h })).json();
      if (!several && p.photo_choices.length > 1) several = c.person_id;
      if (!history && p.photo_history.some((x: any) => x.available)) history = c.person_id;
    }
    return { several, history };
  });
  expect(found.several).not.toBeNull();
  expect(found.history).not.toBeNull();
  const viewer = page.locator("[data-image-viewer]");
  const close = async () => {            // the top bar's empty right end is black, away from the arrows
    const box = (await viewer.boundingBox())!;
    await page.mouse.click(box.x + box.width - 6, box.y + 28);
    await expect(viewer).toBeHidden();
  };

  await page.goto(`/chat/p${found.several}`);
  await page.getByRole("button", { name: /Πληροφορίες|Info/ }).first().click();
  await page.locator("[data-main-photo]").click();
  await expect(viewer).toBeVisible();
  await viewer.locator("img").click();                       // the picture itself does not close it
  await expect(viewer).toBeVisible();
  const src = await viewer.locator("img").getAttribute("src");
  const next = viewer.getByRole("button", { name: /Επόμενο|Next/ });
  const previous = viewer.getByRole("button", { name: /Προηγούμενο|Previous/ });
  if (await next.count()) await next.click();
  else await previous.click();
  await expect.poll(() => viewer.locator("img").getAttribute("src")).not.toBe(src);
  await close();

  await page.goto(`/people/${found.history}`);
  const history = page.locator("[data-photo-history]");
  await history.scrollIntoViewIfNeeded();
  await history.getByRole("button").first().click();
  await expect(viewer).toBeVisible();
  await close();

  await page.locator("[data-main-photo]").click();           // the person page's main photo opens whole too
  await expect(viewer).toBeVisible();
  await close();
});
