import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// Emoji by keyboard in the composer: an emoticon standing alone turns into its emoji when a space or
// a mark ends it (a Backspace right after turns it back, and it stays so), and at the end of what is
// sent; a ":" and two letters open a list of emoji by their shortcodes, a whole ":code:" turns at
// once. A setting turns the emoticons off.

test("emoticons turn into emoji; a ':' finds emoji by name", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.last_service && d.sendable.includes(d.last_service)) return c.id as string;
    }
    return null;
  });
  test.skip(!chat, "no chat that can send where it was last active");
  await page.goto(`/chat/${chat}`);
  const box = page.locator("[data-composer-body] textarea");
  await box.click();
  await box.fill("");

  await box.pressSequentially("hi :) and :-P.");
  await expect(box).toHaveValue("hi 🙂 and 😛.");
  await box.press("Backspace");                                    // right after: back as typed
  await expect(box).toHaveValue("hi 🙂 and :-P");
  await box.pressSequentially(" ok");
  await expect(box).toHaveValue("hi 🙂 and :-P ok");                // and not turned again

  await box.fill("");
  await box.pressSequentially("at 10:30 see http://x.org/a :D ");   // only one standing alone
  await expect(box).toHaveValue("at 10:30 see http://x.org/a 😄 ");

  await box.fill("");
  await box.pressSequentially("so :jo");
  const list = page.locator("[data-emoji-list]");
  await expect(list.locator('[data-emoji-suggestion="😂"]')).toBeVisible();
  await box.press("Enter");                                        // the list's, not sending
  await expect(box).toHaveValue("so 😂");
  await expect(list).toHaveCount(0);
  await box.pressSequentially(" and :heart:");
  await expect(box).toHaveValue("so 😂 and ❤️");
  await box.pressSequentially(" :ro");
  await expect(list).toBeVisible();
  await box.press("Escape");
  await expect(list).toHaveCount(0);

  // the last one turned as it goes
  const text = `sent ${info.project.name} ${Date.now()} <3`;
  await box.fill("");
  await box.pressSequentially(text);
  await page.locator("[data-send]").click();
  await expect(page.getByText(text.replace("<3", "❤️"), { exact: true }).last()).toBeVisible();   // sent, or waiting to

  // turned off in the settings: as typed
  const set = (on: boolean) => page.evaluate((v) => fetch("/api/settings", {
    method: "PUT", headers: { "X-Everysaid": "1", "Content-Type": "application/json" }, body: JSON.stringify({ emoticons: v }),
  }), on);
  await set(false);
  try {
    await page.reload();
    await box.click();
    await box.pressSequentially("hi :) ");
    await expect(box).toHaveValue("hi :) ");
  } finally {
    await set(true);
    await box.fill("");
  }
});
