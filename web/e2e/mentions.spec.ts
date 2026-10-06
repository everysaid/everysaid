import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// In a group: "@" opens a list of its members, the one chosen goes in as "@Name" and is sent as a
// mention; what is sent gets its ticks as the others get and read it, and its info says who and when.
// A file goes with its caption. Mentions in what arrived show as the person's name, a link to them.

async function whatsappGroup(page: Page) {
  return page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats?kind=group", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.mentionable.includes("whatsapp") && d.fileable.includes("whatsapp")) return d;
    }
    return null;
  });
}

test("@ names a member, the message gets its ticks and says who read it", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const group = await whatsappGroup(page);
  test.skip(!group, "no WhatsApp group the demo can send to");
  await page.goto(`/chat/${group.id}`);
  const member = group.members.find((m: any) => m.services.includes("whatsapp") && m.name);
  const box = page.locator("[data-composer-body] textarea");
  await box.fill("quiet: hi ");
  await box.press("End");
  await box.pressSequentially(`@${member.name.slice(0, 3)}`);
  const list = page.locator("[data-mention-list]");
  await expect(list).toBeVisible();
  await expect(list.locator("[data-mention-option]", { hasText: member.name })).toBeVisible();
  await list.locator("[data-mention-option]", { hasText: member.name }).click();
  await expect(box).toHaveValue(`quiet: hi @${member.name} `);
  await expect(list).toHaveCount(0);
  await box.pressSequentially("see you");
  await page.locator("[data-send]").click();

  const sent = page.locator("[id^=m]", { hasText: "see you" }).last();
  await expect(sent.locator("[data-mention]")).toHaveText(`@${member.name}`, { timeout: 15000 });
  await expect(sent.locator('[data-ticks="read"]')).toBeVisible({ timeout: 15000 });    // got, then read by all
  await sent.locator("[data-ticks]").click();
  const infoBox = page.locator("[data-message-info]");
  await expect(infoBox).toBeVisible();
  await expect(infoBox.getByText(member.name).first()).toBeVisible();
  await page.keyboard.press("Escape");
});

test("Esc closes the @ list, and a file goes with its caption", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const group = await whatsappGroup(page);
  test.skip(!group, "no WhatsApp group the demo can send to");
  await page.goto(`/chat/${group.id}`);
  const box = page.locator("[data-composer-body] textarea");
  await box.fill("");
  await box.pressSequentially("@");
  await expect(page.locator("[data-mention-list]")).toBeVisible();
  await box.press("Escape");
  await expect(page.locator("[data-mention-list]")).toHaveCount(0);
  await box.fill("");

  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==", "base64");
  await page.locator("[data-file-input]").setInputFiles({ name: "dot.png", mimeType: "image/png", buffer: png });
  await expect(page.locator("[data-file]")).toContainText("dot.png");
  await box.fill("quiet: a dot");
  await page.locator("[data-send]").click();
  await expect(page.locator("[data-file]")).toHaveCount(0);
  const sent = page.locator("[id^=m]", { hasText: "a dot" }).last();
  await expect(sent.locator("img")).toBeVisible({ timeout: 15000 });
});

test("a mention in what arrived shows the person's name, a link to them", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const found = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats?kind=group", { headers: h })).json()).items;
    for (const c of chats) {
      let before: string | undefined;
      for (let i = 0; i < 20; i++) {
        const page = await (await fetch(`/api/chats/${c.id}/stream?limit=200${before ? `&before=${before}` : ""}`, { headers: h })).json();
        const m = page.items.find((x: any) => x.mentions?.length && x.mentions[0].person_id);
        if (m) return { chat: c.id, id: m.id, name: m.mentions[0].name, person: m.mentions[0].person_id };
        if (!page.has_older) break;
        before = page.items[0].cursor;
      }
    }
    return null;
  });
  test.skip(!found, "no mention in the demo's groups");
  await page.goto(`/chat/${found!.chat}?m=${found!.id}`);
  const link = page.locator(`#m${found!.id} [data-mention]`).first();
  await expect(link).toHaveText(`@${found!.name}`);
  await link.click();
  await expect(page).toHaveURL(new RegExp(`/people/${found!.person}`));
});
