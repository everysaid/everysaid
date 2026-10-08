import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Someone the user has not named and no contact lists may be removed as spam: their page offers it,
// and the dialog says what would go (their messages, calls, files) before anything is removed. The
// test cancels: the demo stays as it was for the other tests (the removal itself is tested in Go).
// Someone named is not offered it; Settings → Names lists those removed (none here).

test("spam: a stranger's page offers the removal, and says first what goes", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people");
  const found = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const people = (await (await fetch("/api/people?limit=500", { headers: h })).json()).items;
    let stranger = 0, known = 0;
    for (const p of people) {
      const c = await (await fetch(`/api/people/${p.id}/spam`, { headers: h })).json();
      if (!c.refused && c.messages > 0 && !stranger) stranger = p.id;
      if (c.refused === "contact" && !known) known = p.id;
    }
    return { stranger, known };
  });
  expect(found.stranger).toBeGreaterThan(0);

  await page.goto(`/people/${found.stranger}`);
  await page.locator("[data-spam-open]").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator("[data-spam-check]")).toContainText(/μηνύματα|messages/);
  await expect(dialog.locator("[data-spam-remove]")).toBeVisible();
  await dialog.getByRole("button", { name: /^(Άκυρο|Cancel)$/ }).click();
  await expect(dialog).toBeHidden();

  if (found.known) {
    await page.goto(`/people/${found.known}`);
    await expect(page.getByRole("heading").first()).toBeVisible();
    await expect(page.locator("[data-spam-open]")).toHaveCount(0);
  }

  await page.goto("/settings?tab=names");
  await expect(page.locator("[data-spam-removed]")).toContainText(/Τίποτα εδώ|Nothing here/);
});
