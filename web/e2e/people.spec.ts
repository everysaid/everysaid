import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A suggestion opens with its people side by side and a little of each one's history; those ticked
// become one. A demo person's Telegram name in Latin letters sounds like a contact's in Greek. The test
// splits them again afterwards, so the demo stays as it was for the other tests.

test("people: a suggestion shows each one's history, and merges those chosen", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people");
  await page.locator("[data-suggestion]", { hasText: /Ελένη Ιωάννου|Eleni Ioannou/ }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("[data-candidate]")).toHaveCount(2);
  await expect(dialog.locator("[data-candidate]", { hasText: "eleni_i" })).toContainText("Talk later?");
  const tick = dialog.locator("[data-candidate]").first().getByRole("button", { pressed: true });
  await tick.click();
  await expect(dialog.locator("[data-merge-chosen]")).toBeDisabled();       // one alone is no merge
  await dialog.locator("[data-candidate]").first().getByRole("button", { pressed: false }).click();
  await dialog.locator("[data-merge-chosen]").click();
  await expect(dialog).toBeHidden();
  await expect(page.locator("[data-suggestion]", { hasText: /Ελένη Ιωάννου|Eleni Ioannou/ })).toHaveCount(0);

  const split = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const found = (await (await fetch("/api/people?q=eleni_i", { headers: h })).json()).items[0];
    const p = await (await fetch(`/api/people/${found.id}`, { headers: h })).json();
    {
      const telegram = p.handles.find((x: any) => x.value === "eleni_i");
      return (await fetch(`/api/addresses/${telegram.address_id}/split`, { method: "POST", headers: h })).status;
    }
  });
  expect(split).toBe(200);
});

// All suggestions on one page: the same name in WhatsApp's contacts comes ticked, a name that only
// sounds alike does not. Applying merges the first and keeps the second apart; the second is then in
// the list of those said not to be one, and can be suggested again. The merge is split afterwards.
test("people: many suggestions decided at once, and one said apart by mistake suggested again", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people/merge");
  const book = page.locator("[data-alike]", { hasText: "Νίκος Γεωργίου" });
  const similar = page.locator("[data-alike]", { hasText: /Ελένη Ιωάννου|Eleni Ioannou/ });
  await expect(book.locator("[data-candidate][aria-pressed=true]")).toHaveCount(2);
  await expect(similar.locator("[data-candidate][aria-pressed=true]")).toHaveCount(0);
  await similar.locator("[data-same]").click();                               // one click: all of it
  await expect(similar.locator("[data-candidate][aria-pressed=true]")).toHaveCount(2);
  await similar.locator("[data-apart]").click();                              // changed my mind
  await page.locator("[data-apply]").click();
  await page.locator("[data-apply-confirm]").click();
  await expect(page.locator("[data-alike]")).toHaveCount(0);

  await page.getByRole("tab", { name: /^(Όχι ίδιοι|Not the same)$/ }).click();
  const pair = page.locator("[data-apart-pair]", { hasText: /Ελένη Ιωάννου|Eleni Ioannou/ });
  await expect(pair).toHaveCount(1);
  await pair.locator("[data-undo-apart]").click();
  await expect(pair).toHaveCount(0);
  await page.getByRole("tab", { name: /^(Προτάσεις|Suggestions)$/ }).click();
  await expect(page.locator("[data-alike]")).toHaveCount(1);

  const split = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const found = (await (await fetch("/api/people?q=15550100020", { headers: h })).json()).items[0];
    const p = await (await fetch(`/api/people/${found.id}`, { headers: h })).json();
    const second = p.handles.find((x: any) => x.value === "+15550100020");
    return (await fetch(`/api/addresses/${second.address_id}/split`, { method: "POST", headers: h })).status;
  });
  expect(split).toBe(200);
});

// Back from a person, the list is where it was left.
test("people: back from a person, the list is where it was", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people");
  const rows = page.locator("[data-person]");
  await expect(rows.first()).toBeVisible();
  await page.locator("[data-virtuoso-scroller]").evaluate((el) => el.scrollBy(0, 600));
  await page.waitForTimeout(400);
  const firstInView = async () => page.locator("[data-virtuoso-scroller]").evaluate((el) => {
    const edge = el.getBoundingClientRect().top;
    const r = [...el.querySelectorAll<HTMLElement>("[data-person]")].find((x) => x.getBoundingClientRect().bottom > edge + 1);
    return r?.dataset.person;
  });
  const before = await firstInView();
  expect(await page.locator("[data-virtuoso-scroller]").evaluate((el) => el.scrollTop)).toBeGreaterThan(300);
  await rows.nth(3).click();
  await expect(page).toHaveURL(/\/people\/\d+$/);
  await page.goBack();
  await expect(rows.first()).toBeVisible();
  await expect.poll(firstInView).toBe(before);
});

// The people without a name: one is named, another is said to be someone already named. What is
// done stays in place, marked. Both are undone afterwards, so the demo stays as it was.
test("people: without a name, one named and one the same as someone", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people/unnamed");
  const courier = page.locator("[data-unnamed]", { hasText: "+1 555-010-0010" });
  await expect(courier).toContainText("Your parcel will be delivered");
  await courier.locator("[data-unnamed-name]").fill("The courier");
  await courier.getByRole("button", { name: /^(Αποθήκευση|Save)$/ }).click();
  await expect(courier.locator("[data-unnamed-done]")).toContainText("The courier");

  const clinic = page.locator("[data-unnamed]", { hasText: "+1 555-010-0011" });
  await clinic.locator("[data-unnamed-same]").click();
  await page.locator("[data-same-search]").fill("Ελένη Ιωάννου");
  await page.locator("[data-same-pick]").first().click();
  await expect(clinic.locator("[data-unnamed-done]")).toContainText("Ελένη Ιωάννου");

  const undone = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1", "Content-Type": "application/json" };
    const find = async (q: string) => (await (await fetch(`/api/people?q=${encodeURIComponent(q)}`, { headers: h })).json()).items[0];
    const courier = await find("The courier");
    const a = (await fetch(`/api/people/${courier.id}`, { method: "PATCH", headers: h, body: JSON.stringify({ name: "" }) })).status;
    const eleni = await (await fetch(`/api/people/${(await find("15550100011")).id}`, { headers: h })).json();
    const handle = eleni.handles.find((x: any) => x.value === "+15550100011");
    const b = (await fetch(`/api/addresses/${handle.address_id}/split`, { method: "POST", headers: h })).status;
    return [a, b];
  });
  expect(undone).toEqual([200, 200]);
});
