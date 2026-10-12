import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Each theme's main colour: the icon's brown by default, any other the user picks (kept by the
// server, so on every device), back to the default with a click.

test("a theme's main colour, picked and back to the default", async ({ page }, info) => {
  test.skip(info.project.name !== "desktop", "once is enough");
  await signedIn(page, info.project.name);
  await page.evaluate(() => localStorage.setItem("theme", "light"));
  await page.goto("/settings");
  const accent = () => page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--accent").trim());
  const picker = page.getByLabel(/Χρώμα στο φωτεινό θέμα|Colour on the light theme/);
  await expect(picker).toHaveValue("#5e4428");
  expect(await accent()).toBe("#5e4428");

  await picker.fill("#f2d04a");                     // a light one: dark text on it
  expect(await accent()).toBe("#f2d04a");
  expect(await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue("--accent-fg").trim())).toBe("#12141a");
  await expect.poll(async () => (await (await page.request.get("/api/settings", { headers: { "X-Everysaid": "1" } })).json()).accent_light)
    .toBe("#f2d04a");
  await page.reload();                              // kept: the next visit starts with it
  expect(await accent()).toBe("#f2d04a");

  await page.getByRole("button", { name: /Προεπιλογή|Default/ }).first().click();
  expect(await accent()).toBe("#5e4428");
  await expect.poll(async () => (await (await page.request.get("/api/settings", { headers: { "X-Everysaid": "1" } })).json()).accent_light ?? null)
    .toBeNull();
});
