import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// The main colour: the icon's brown by default, any other the user picks (kept by the server, so on
// every device), back to the default with a click; the dark theme takes a lighter shade of it.

test("the main colour, picked and back to the default", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.evaluate(() => localStorage.setItem("theme", "light"));
  await page.goto("/settings");
  const css = (v: string) => page.evaluate((x) => getComputedStyle(document.documentElement).getPropertyValue(x).trim(), v);
  const settings = async () => (await (await page.request.get("/api/settings", { headers: { "X-Everysaid": "1" } })).json()).accent ?? null;
  const picker = page.getByLabel(/^(Χρώμα|Colour)$/);
  await expect(picker).toHaveValue("#5e4428");
  expect(await css("--accent")).toBe("#5e4428");

  await picker.fill("#f2d04a");                     // a light one: dark text on it
  expect(await css("--accent")).toBe("#f2d04a");
  expect(await css("--accent-fg")).toBe("#12141a");
  await expect.poll(settings).toBe("#f2d04a");
  await page.reload();                              // kept: the next visit starts with it
  expect(await css("--accent")).toBe("#f2d04a");

  // the dark theme: a colour of its own, from the same one
  const swatch = () => page.evaluate(() => {
    const el = document.createElement("div");
    el.style.color = "var(--accent)";
    document.body.append(el);
    const c = getComputedStyle(el).color;
    el.remove();
    return c;
  });
  const lightOne = await swatch();
  await page.evaluate(() => document.documentElement.classList.add("dark"));
  expect(await swatch()).not.toBe(lightOne);
  await page.evaluate(() => document.documentElement.classList.remove("dark"));

  await page.getByRole("button", { name: /^(Προεπιλογή|Default)$/ }).first().click();
  expect(await css("--accent")).toBe("#5e4428");
  await expect.poll(settings).toBeNull();
});
