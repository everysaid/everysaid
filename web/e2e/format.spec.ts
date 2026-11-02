import { expect, test, type Locator, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// Styles in the composer, the same whatever service it goes to: a pair of marks typed at the edges
// of words ("*γεια*") turns into its style at once, the marks gone (a Backspace right after brings
// them back, and they stay); within a word only the menu or the keys style; a style taken off part
// of a styled text leaves the rest on both sides; undo brings styles back too. What goes says the
// styles in characters.

/** Selects [start, end) of the field's text. */
async function select(field: Locator, start: number, end: number) {
  await field.evaluate((el, [a, b]) => {
    const at = (n: number): [Node, number] => {
      const walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      let left = n;
      for (let t = walk.nextNode(); t; t = walk.nextNode()) {
        const len = (t as Text).data.length;
        if (left <= len) return [t, left];
        left -= len;
      }
      return [el, el.childNodes.length];
    };
    const [an, ao] = at(a), [bn, bo] = at(b);
    window.getSelection()!.setBaseAndExtent(an, ao, bn, bo);
  }, [start, end]);
}

const styled = (field: Locator, kind: string) => field.locator(`[data-k~="${kind}"]`);

/** A button of the styles' bar: on a touch screen, the bar asked for first (Aa). */
async function styleButton(page: Page, which: string): Promise<Locator> {
  const bar = page.locator("[data-format]");
  const aa = page.locator("[data-format-open]");
  await expect(bar.or(aa).first()).toBeVisible();      // either, once the selection is seen
  if (!(await bar.isVisible())) await aa.click();
  await expect(bar).toBeVisible();
  return bar.locator(which);
}

test("styles in the composer: marks turned, the menu, a style taken off in part, undo, and what goes", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (c.type === "person" && d.last_service && d.sendable.includes(d.last_service)) return c.id as string;
    }
    return null;
  });
  test.skip(!chat, "no chat that can send where it was last active");
  await page.goto(`/chat/${chat}`);
  const field = page.locator("[data-composer-field]");
  await field.click();
  await field.fill("");

  // Shift+Enter breaks the line where the caret is, and the caret goes on after it
  await field.pressSequentially("ab");
  await field.press("Shift+Enter");
  await field.pressSequentially("c");
  await field.press("ArrowLeft");
  await field.press("ArrowLeft");
  await field.press("Shift+Enter");
  await field.pressSequentially("d");
  expect(await field.evaluate((el) => el.textContent)).toBe("ab\nd\nc");
  await field.fill("");

  // at the edges of a word: turned at once, the marks gone
  await field.pressSequentially("say *γεια*");
  await expect(field).toHaveText("say γεια");
  await expect(styled(field, "bold")).toHaveText("γεια");
  // a Backspace right after: the marks back, as typed, and not turned again
  await field.press("Backspace");
  await expect(field).toHaveText("say *γεια*");
  await expect(styled(field, "bold")).toHaveCount(0);
  await field.pressSequentially(" ok");
  await expect(field).toHaveText("say *γεια* ok");
  await expect(styled(field, "bold")).toHaveCount(0);

  // within a word, or with a space inside: not turned
  await field.fill("");
  await field.pressSequentially("2*3*4 and wo_rd_s and * no*");
  await expect(field).toHaveText("2*3*4 and wo_rd_s and * no*");
  await expect(field.locator("[data-k]")).toHaveCount(0);

  // the menu, on a selection within a word; taken off in part, the rest stays on both sides
  await field.fill("");
  await field.pressSequentially("hello world");
  await select(field, 2, 4);
  if (info.project.name === "desktop") {   // with a mouse: above where the selection starts, nothing moved
    const top = (await field.boundingBox())!.y;
    await expect(page.locator("[data-format]")).toBeVisible();
    const bar = (await page.locator("[data-format]").boundingBox())!;
    expect(bar.y + bar.height).toBeLessThanOrEqual(top + 4);
    expect((await field.boundingBox())!.y).toBe(top);
  }
  await (await styleButton(page, '[data-style="italic"]')).click();
  await expect(styled(field, "italic")).toHaveText("ll");
  await select(field, 0, 11);
  await (await styleButton(page, '[data-style="bold"]')).click();
  await expect(styled(field, "bold")).toHaveText(["he", "ll", "o world"]);
  await select(field, 4, 7);
  await expect(await styleButton(page, '[data-style="bold"]')).toHaveAttribute("aria-pressed", "true");
  await (await styleButton(page, "[data-style-clear]")).click();
  await expect(styled(field, "bold")).toHaveText(["he", "ll", "orld"]);
  await expect(styled(field, "italic")).toHaveText("ll");
  // undo: the styles back as they were
  await field.press("ControlOrMeta+z");
  await expect(styled(field, "bold")).toHaveText(["he", "ll", "o world"]);

  // a key with nothing selected: on for what is typed next
  await field.fill("");
  await field.pressSequentially("a ");
  await field.press("ControlOrMeta+b");
  await field.pressSequentially("bold");
  await field.press("ControlOrMeta+b");
  await field.pressSequentially(" c");
  await expect(field).toHaveText("a bold c");
  await expect(styled(field, "bold")).toHaveText("bold");

  // a Greek layout: Ctrl with the key of B writes β, and is still bold
  await field.fill("");
  await field.pressSequentially("ένα δύο");
  await select(field, 4, 7);
  await field.dispatchEvent("keydown", { key: "β", code: "KeyB", ctrlKey: true, bubbles: true, cancelable: true });
  await expect(styled(field, "bold")).toHaveText("δύο");
  // Esc closes the styles' bar, the selection kept; a new selection shows it again, the same words too
  await styleButton(page, '[data-style="bold"]');
  await field.press("Escape");
  await expect(page.locator("[data-format]")).toHaveCount(0);
  expect(await page.evaluate(() => window.getSelection()!.toString())).toBe("δύο");
  await select(field, 7, 7);
  await expect(page.locator("[data-format-open]")).toHaveCount(0);   // let go: nothing selected seen
  await select(field, 4, 7);
  await styleButton(page, '[data-style="bold"]');
  await select(field, 0, 3);
  await styleButton(page, '[data-style="bold"]');

  // what goes: the text without marks, the styles in characters (an emoji is one)
  await field.fill("");
  await field.pressSequentially(`quiet: 😀 *hi* ${info.project.name}`);
  await expect(styled(field, "bold")).toHaveText("hi");
  const sent = page.waitForRequest((r) => r.url().includes("/send") && r.method() === "POST");
  if (info.project.name === "desktop") {     // sent with words selected (Enter): the styles' bar goes with them
    await select(field, 7, 9);
    await expect(page.locator("[data-format]")).toBeVisible();
    await field.press("Enter");
    await expect(page.locator("[data-format]")).toHaveCount(0);
  } else await page.locator("[data-send]").click();
  const body = (await sent).postDataJSON();
  expect(body.text).toBe(`quiet: 😀 hi ${info.project.name}`);
  expect(body.styles).toEqual([{ start: 9, length: 2, kind: "bold" }]);
  await expect(field).toHaveText("");
});

// Sent through WhatsApp, whose apps read styles from marks in the text: the message as it comes
// back shows them as the apps do, bold with no marks, also within a word.
test("styles sent through WhatsApp come back shown, not as marks", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (c.type === "person" && d.sendable.includes("whatsapp")) return c.id as string;
    }
    return null;
  });
  test.skip(!chat, "no chat that can send through WhatsApp");
  await page.goto(`/chat/${chat}`);
  const via = page.locator("[data-via]");
  if ((await via.getAttribute("data-via")) !== "whatsapp") {
    await via.click();
    await page.getByRole("menuitem").filter({ has: page.locator('[data-icon="whatsapp"]') }).click();
  }
  const field = page.locator("[data-composer-field]");
  await field.click();
  await field.fill("");
  const tag = `${info.project.name}${Date.now()}`;
  await field.pressSequentially(`quiet: *strong* ${tag}`);
  await select(field, 7 + 2, 7 + 4);                       // "ro" of "strong" in italic too: within a word
  await (await styleButton(page, '[data-style="italic"]')).click();
  await page.locator("[data-send]").click();
  await expect(page.locator("[data-stream] [data-sending]")).toHaveCount(0, { timeout: 10000 });
  const bubble = page.locator("[data-stream] [data-cursor]").filter({ hasText: tag }).last();
  await expect(bubble).toContainText(`quiet: strong ${tag}`);
  await expect(bubble).not.toContainText("*");
  await expect(bubble.locator('[data-k~="bold"]')).toHaveText(["st", "ro", "ng"]);
  await expect(bubble.locator('[data-k~="italic"]')).toHaveText("ro");
});
