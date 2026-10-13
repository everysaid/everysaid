import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// GIFs, behind the emoji's button: the demo's searcher finds its invented ones by name, a tap sends
// one as a GIF, which is then in the History; a searcher that cannot now is there, greyed, saying
// why. "@gif" and words find them above the text as it is written.

// the tab of the plugin that finds GIFs (not the History's, nor the emoji's)
const searcherTab = "[data-results-tab]:not([data-results-history]):not([data-results-tab='emoji'])";

async function sendableChat(page: Page) {
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes("whatsapp") && d.fileable?.includes("whatsapp")) return c.id as string;
    }
    return null;
  });
  if (chat) {
    await page.goto(`/chat/${chat}`);
    await page.locator("[data-via]").click();                 // through WhatsApp, which takes files
    await page.getByRole("menuitem", { name: "WhatsApp" }).click();
  }
  return chat;
}

// the last message the user sent in the chat went as a GIF (kept a while first where the chat has
// others waiting before it: the demo's limit of messages a minute, reached by the tests before)
async function lastWentAsGif(page: Page, chat: string) {
  await expect.poll(() => page.evaluate(async (id) => {
    const r = await (await fetch(`/api/chats/${id}/stream?limit=5`, { headers: { "X-Everysaid": "1" } })).json();
    const mine = r.items.filter((i: { type: string; outgoing: boolean }) => i.type === "message" && i.outgoing);
    return mine[mine.length - 1]?.subtype;
  }, chat), { timeout: 45_000 }).toBe("gif");
}

test("a GIF found, sent with a tap, then in the History", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  await page.locator("[data-emoji]").click();
  const box = page.locator("[data-expressions]");
  await expect(box.locator("[data-emoji-picker]")).toBeVisible();      // the emoji first
  await box.locator("[data-gif-tab]:not([data-gif-history])").first().click();
  await expect(box.locator("[data-gif-waiting]")).toBeVisible();        // nothing asked for before words
  await box.locator("[data-gif-search]").fill("a");                    // a second after them
  await expect(box.locator("[data-gif-option]")).toHaveCount(2);
  await box.locator("[data-gif-search]").fill("par");
  await expect(box.locator("[data-gif-option]")).toHaveCount(1);
  await expect(box.locator("[data-gif-option] video")).toHaveCount(1);

  const sent = page.waitForResponse((r) => r.url().includes(`/api/chats/${chat}/send`) && r.request().method() === "POST");
  await box.locator("[data-gif-option]").click();
  expect((await sent).ok()).toBe(true);
  await lastWentAsGif(page, chat!);
  await expect(box).toHaveCount(0);                                    // closed once picked
  await expect(page.locator('[id^="m"]').filter({ has: page.locator("video") }).last()).toBeVisible();   // the demo answers after it

  await page.locator("[data-emoji]").click();
  await box.locator("[data-gif-history]").click();
  await expect(box.locator("[data-gif-option]").first()).toBeVisible();
});

test("a searcher that cannot now: greyed, saying why", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  await page.route("**/api/gifs", (r) => r.fulfill({ json: { items: [{ instance: 999, name: "Telegram", why: "Not signed in to Telegram yet" }] } }));
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  await page.locator("[data-emoji]").click();
  const box = page.locator("[data-expressions]");
  await expect(box.locator("[data-gif-tab='999']")).toHaveClass(/opacity-50/);
  await box.locator("[data-gif-tab='999']").click();
  await expect(box.locator("[data-gif-unavailable]")).toContainText("Not signed in to Telegram yet");
});

test("@gif and words: GIFs above the text, one sent with a tap, the text gone", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  const field = page.locator("[data-composer-body] textarea");
  await field.fill("");
  await field.pressSequentially("@g");
  await expect(page.locator("[data-command-option='gif']")).toBeVisible();     // among what @ offers
  await field.press("Enter");
  await expect(field).toHaveValue("@gif ");
  await field.pressSequentially("dog");
  const results = page.locator("[data-gif-results]");
  await results.locator(searcherTab).click();                                  // the plugin's (the History comes first)
  await expect(results.locator("[data-gif-option]")).toHaveCount(1);
  await field.press("Enter");                                                  // not sent as text
  await expect(field).toHaveValue("@gif dog");
  await results.locator("[data-gif-option]").click();
  await lastWentAsGif(page, chat!);
  await expect(field).toHaveValue("");
  await expect(results).toHaveCount(0);
});

test("@gif by keys: a tab a plugin's and one the emoji's, the one chosen last opened again", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await sendableChat(page);
  test.skip(!chat, "no chat to send to");
  const field = page.locator("[data-composer-body] textarea");
  const results = page.locator("[data-gif-results]");
  await field.fill("@gif cat");
  await results.locator(searcherTab).click();
  await expect(results.locator("[data-gif-option]")).toHaveCount(1);
  await field.press("ArrowDown");                                              // the first marked, the caret still in the text
  await expect(results.locator("[data-gif-option][data-active]")).toHaveCount(1);
  await expect(field).toBeFocused();
  await field.press("Enter");                                                  // sent
  await lastWentAsGif(page, chat!);
  await expect(field).toHaveValue("");

  await field.fill("@gif cat");
  await field.press("Tab");                                                    // the emoji's tab
  await expect(results.locator("[data-results-tab='emoji']")).toBeVisible();
  await expect(results.locator("[data-emoji-result]").first()).toBeVisible();
  const first = await results.locator("[data-emoji-result]").first().getAttribute("data-emoji-result");
  await field.press("ArrowDown");
  await field.press("Enter");                                                  // the emoji in place of "@gif cat"
  await expect(field).toHaveValue(first!);
  await expect(results).toHaveCount(0);

  await field.fill("@gif dog");                                               // opened at the tab chosen last
  await expect(results.locator("[data-emoji-result]").first()).toBeVisible();
  await expect(results.locator("[data-gif-option]")).toHaveCount(0);
  await field.press("Escape");
  await expect(results).toHaveCount(0);
});
