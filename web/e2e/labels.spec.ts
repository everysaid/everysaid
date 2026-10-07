import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// One person of the demo has no name, but their email says who they are: the name is
// suggested on the page of those without one, and accepted with a click. A label from the lists is
// given by hand on her page, and the lists themselves are changed in Settings. Everything is put back
// afterwards, so the demo stays as it was for the other tests.

test("labels: a name read from an email, accepted with a click", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/people/unnamed");
  await page.getByRole("switch", { name: /Με πρόταση πρώτα|Suggested first/ }).click();
  const row = page.locator("[data-unnamed]").first();
  await expect(row).toContainText("katerina.oikonomou@example.com");
  await expect(row.locator("[data-guess=handle]")).toContainText("Κατερίνα Οικονόμου");
  await row.locator("[data-guess-accept]").click();
  await expect(row.locator("[data-unnamed-done]")).toContainText("Κατερίνα Οικονόμου");

  const pid = await row.getAttribute("data-unnamed");
  const back = await page.evaluate(async (pid) => {
    const h = { "X-Everysaid": "1", "Content-Type": "application/json" };
    return (await fetch(`/api/people/${pid}`, { method: "PATCH", headers: h, body: JSON.stringify({ name: "" }) })).status;
  }, pid);
  expect(back).toBe(200);
});

test("labels: one given by hand on a person's page, and taken away", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const pid = await page.evaluate(async () =>
    (await (await fetch("/api/people?q=katerina", { headers: { "X-Everysaid": "1" } })).json()).items[0].id);
  await page.goto(`/people/${pid}`);
  const card = page.locator("[data-labels]");
  await card.locator("[data-label-add]").click();
  await page.getByRole("dialog").locator("[data-pick=colleague]").click();
  const chip = card.locator("[data-chip=colleague]");
  await expect(chip).toHaveAttribute("data-state", "yes");
  await chip.getByRole("button").last().click();
  await expect(chip).toHaveCount(0);
});

test("labels: the lists are the user's: a tone added, renamed, merged into another", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.goto("/settings");
  const tones = page.locator("[data-list=tone]");
  await expect(tones.locator("[data-label=romantic]")).toBeVisible();
  await expect(tones.locator("[data-label=sexual]")).toHaveCount(0);
  await tones.locator("[data-list-add]").click();
  let dialog = page.getByRole("dialog");
  await dialog.locator("[data-label-name]").fill("Acme");
  await dialog.locator("[data-label-meaning]").fill("work talk about the Acme project");
  await dialog.locator("[data-label-save]").click();
  const acme = tones.locator("[data-label=Acme]");
  await expect(acme).toContainText("work talk about the Acme project");
  await acme.locator("[data-label-edit]").click();
  dialog = page.getByRole("dialog");
  const into = await dialog.locator("[data-label-into] option", { hasText: /Επαγγελματική|Professional/ }).getAttribute("value");
  await dialog.locator("[data-label-into]").selectOption(into!);
  page.once("dialog", (d) => d.accept());
  await dialog.locator("[data-label-merge]").click();
  await expect(acme).toHaveCount(0);
  await expect(tones.locator("[data-label=professional]")).toBeVisible();
});
