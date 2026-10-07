import { expect, test } from "@playwright/test";
import { signIn } from "./demo";

// The assistant's tokens (MCP): made, listed with when made and used, revoked; the security
// activity in the user's words. Runs after app.spec.ts (the demo's user exists).

test("assistant tokens are listed and revoked", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "once is enough");
  await signIn(page);                                   // a fresh sign-in: changing the ways in is allowed
  await page.goto("/settings?tab=security");
  const revoke = page.getByRole("button", { name: /^(Ανάκληση|Revoke)$/ });
  const before = await revoke.count();
  await page.getByRole("button", { name: /^(Νέο token|New token)$/ }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(revoke).toHaveCount(before + 1);
  await expect(page.getByText(/δεν έχει χρησιμοποιηθεί|never used/).first()).toBeVisible();
  await expect(page.getByText(/Νέο token βοηθού|Assistant token made/).first()).toBeVisible();   // the activity, in words
  page.once("dialog", (d) => d.accept());
  await revoke.last().click();
  await expect(revoke).toHaveCount(before);
  await expect(page.getByText(/Ανακλήθηκε token βοηθού|Assistant token revoked/).first()).toBeVisible();
});
