import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A message of emoji alone, as the services show it: one alone large and without its bubble, two
// and three smaller in theirs, four or more (or with words) as any text.

test("emoji alone: larger the fewer, one without its bubble", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const r = await (await fetch("/api/chats", { headers: { "X-Everysaid": "1" } })).json();
    return r.items[0].id as string;
  });
  // the chat's newest messages, whatever they were (files, replies: the tests before may have sent
  // many, the plain ones far above the screen, never drawn), made plain with emoji for their words
  const texts = ["😂", "👍🏽 ❤️", "🎉🎉🎉", "😮😮😮😮", "ok 😂"];
  const changed: number[] = [];
  await page.route(`**/api/chats/${chat}/stream*`, async (r) => {
    const res = await r.fetch();
    const body = await res.json();
    const said = body.items.filter((m: Record<string, unknown>) => m.type === "message" && m.kind !== "system");
    for (const [i, m] of said.slice(-texts.length).entries()) {
      Object.assign(m, { kind: "text", text: texts[i], deleted: false, reply: null, attachments: [], local: undefined,
        preview: null, location: null, notice: null });
      changed[i] = m.id;
    }
    await r.fulfill({ response: res, json: body });
  });
  await page.goto(`/chat/${chat}`);
  await expect.poll(() => changed.length).toBe(texts.length);

  const bubble = (i: number) => page.locator(`#m${changed[i]} [data-emoji], #m${changed[i]} [data-bare]`).first();
  const size = (i: number) => page.locator(`#m${changed[i]}`).getByText(texts[i], { exact: true })
    .evaluate((el) => parseFloat(getComputedStyle(el.closest("div")!).fontSize));

  await expect(bubble(0)).toHaveAttribute("data-bare", "true");
  await expect(bubble(0)).toHaveAttribute("data-emoji", "1");
  expect(await bubble(0).evaluate((el) => getComputedStyle(el).backgroundColor)).toBe("rgba(0, 0, 0, 0)");
  await expect(bubble(1)).toHaveAttribute("data-emoji", "2");
  await expect(bubble(1)).not.toHaveAttribute("data-bare");
  await expect(bubble(2)).toHaveAttribute("data-emoji", "3");
  await expect(page.locator(`#m${changed[3]} [data-emoji]`)).toHaveCount(0);
  await expect(page.locator(`#m${changed[4]} [data-emoji]`)).toHaveCount(0);

  const [one, two, three, four] = [await size(0), await size(1), await size(2), await size(3)];
  expect(one).toBeGreaterThan(two);
  expect(two).toBeGreaterThan(three);
  expect(three).toBeGreaterThan(four);
  expect(four).toBe(15);
});
