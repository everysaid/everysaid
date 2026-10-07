import { expect, test } from "@playwright/test";
import { setupLink, totp } from "./demo";

// The password with an authenticator app (TOTP): set it from a one-time link, sign out, sign in
// with name, password and code. Runs after app.spec.ts (the demo's user exists).

test("password and authenticator code", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "once is enough");
  await page.goto(setupLink());
  await page.getByRole("button", { name: /κωδικός και εφαρμογή|password and an authenticator/ }).click();
  await page.locator('input[autocomplete="username"]').fill("Demo");
  await page.getByRole("button", { name: /Συνέχεια|Continue/ }).click();
  const secret = (await page.locator("code").innerText()).replace(/\s/g, "");
  expect(secret.length).toBeGreaterThan(20);
  await expect(page.locator('img[alt="QR"]')).toBeVisible();
  await page.screenshot({ path: "/tmp/chr-shots/totp-01-setup.png" });
  await page.locator('input[type="password"]').nth(0).fill("too short");    // says what is missing, never a silent button
  await expect(page.getByText(/λείπουν 3|3 to go/)).toBeVisible();
  const pw = "a long enough password";
  await page.locator('input[type="password"]').nth(0).fill(pw);
  await page.locator('input[type="password"]').nth(1).fill(pw);
  await page.locator('input[autocomplete="one-time-code"]').fill("000000");
  await page.getByRole("button", { name: /Ορισμός κωδικού|Set a password/ }).click();
  await expect(page.getByText(/δεν ταιριάζει|does not match|ταιριάζει/)).toBeVisible();      // a wrong code is refused
  await page.locator('input[autocomplete="one-time-code"]').fill(totp(secret));
  await page.getByRole("button", { name: /Ορισμός κωδικού|Set a password/ }).click();
  await expect(page.getByRole("heading", { name: /Συνομιλίες|Chats/ })).toBeVisible({ timeout: 15000 });

  // sign out, then in with name, password and the next code (each code is accepted once)
  await page.goto("/settings?tab=security");
  await expect(page.getByText(/Κωδικός και εφαρμογή επαλήθευσης|Password and authenticator app/)).toBeVisible();
  await page.getByRole("button", { name: /^Αποσύνδεση$|^Sign out$/ }).last().click();
  await page.getByRole("button", { name: /Σύνδεση με κωδικό|Sign in with a password/ }).click();
  await page.locator('input[autocomplete="username"]').fill("Demo");
  await page.locator('input[type="password"]').fill(pw);
  await page.locator('input[autocomplete="one-time-code"]').fill(totp(secret, 1));
  await page.screenshot({ path: "/tmp/chr-shots/totp-02-login.png" });
  await page.getByRole("button", { name: /Σύνδεση με κωδικό|Sign in with a password/ }).click();
  await expect(page.getByRole("heading", { name: /Συνομιλίες|Chats/ })).toBeVisible({ timeout: 15000 });
});
