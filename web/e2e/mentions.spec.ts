import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// In a group: "@" opens a list of its members, the one chosen goes in as "@Name" and is sent as a
// mention; what is sent gets its ticks as the others get and read it, and its info says who and when.
// A file goes with its caption, or is dropped or pasted, or copied from a message to another chat.
// Mentions in what arrived show as the person's name, a link to them.

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
  let answer = () => {};
  const held = new Promise<void>((ok) => { answer = ok; });
  await page.route("**/api/chats/*/send", async (r) => { await held; await r.continue(); });   // the sending held
  await page.locator("[data-send]").click();
  await expect(page.locator("[data-file]")).toHaveCount(0);
  const sent = page.locator("[id^=m]", { hasText: "a dot" }).last();
  await expect(sent.locator("[data-local] img")).toBeVisible();          // at once, from the file itself
  answer();
  await expect(sent.locator("button img")).toBeVisible({ timeout: 15000 });   // then the server's
  await expect(sent.locator("[data-local]")).toHaveCount(0);
});

test("a file dropped on the chat or pasted goes to the composer; one copied from a message, to another chat", async ({ page, context }, info) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await signedIn(page, info.project.name);
  const group = await whatsappGroup(page);
  test.skip(!group, "no WhatsApp group the demo can send to");
  await page.goto(`/chat/${group.id}`);
  const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==";
  const files = (name: string) => page.evaluateHandle(([b64, name]) => {
    const d = new DataTransfer();
    d.items.add(new File([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))], name, { type: "image/png" }));
    return d;
  }, [png, name]);

  // dropped on the messages (once the composer can take a file)
  await expect(page.locator("[data-attach]")).toBeVisible();
  const drop = await files("dropped.png");
  await page.locator("[data-stream]").dispatchEvent("dragover", { dataTransfer: drop });
  await expect(page.locator("[data-drop]")).toBeVisible();
  await page.locator("[data-stream]").dispatchEvent("drop", { dataTransfer: drop });
  await expect(page.locator("[data-drop]")).toHaveCount(0);
  await expect(page.locator("[data-file]")).toContainText("dropped.png");
  await page.getByRole("button", { name: /Αφαίρεση αρχείου|Remove the file/ }).click();

  // pasted in the field
  const box = page.locator("[data-composer-body] textarea");
  await box.evaluate((el, d) => el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: d, bubbles: true, cancelable: true })), await files("pasted.png"));
  await expect(page.locator("[data-file]")).toContainText("pasted.png");
  await box.fill("quiet: a pasted dot");
  await page.locator("[data-send]").click();
  const sent = page.locator("[id^=m]", { hasText: "a pasted dot" }).last();
  await expect(sent.locator("img")).toBeVisible({ timeout: 15000 });
  await expect.poll(async () => Number((await sent.getAttribute("id"))!.slice(1)), { timeout: 10000 }).toBeGreaterThan(0);

  // copied from the message, pasted in a chat of another service, as the file it is
  await sent.locator("[data-actions]").click();
  await page.getByRole("menuitem", { name: /Αντιγραφή αρχείου|Copy the file/ }).click();
  await expect(page.getByText(/^(Αντιγράφηκε|Copied)$/)).toBeVisible();
  const link = await page.evaluate(() => navigator.clipboard.readText());
  expect(link).toMatch(/\/api\/media\/[0-9a-f]{64}\/original(#name=pasted\.png)?$/);       // the name, where the archive kept it
  const other = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    for (const c of (await (await fetch("/api/chats", { headers: h })).json()).items) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      const s = d.fileable?.find((x: string) => x !== "whatsapp" && d.sendable.includes(x));
      if (s) return { id: c.id, service: s };
    }
    return null;
  });
  test.skip(!other, "no chat of another service the demo can send a file to");
  await page.goto(`/chat/${other!.id}`);
  const via = page.locator("[data-via]");
  if ((await via.getAttribute("data-via")) !== other!.service) {
    await via.click();
    await page.getByRole("menuitem").filter({ has: page.locator(`[data-icon="${other!.service}"]`) }).click();
  }
  const field = page.locator("[data-composer-body] textarea");
  await field.evaluate((el, text) => {
    const d = new DataTransfer();
    d.setData("text/plain", text);
    el.dispatchEvent(new ClipboardEvent("paste", { clipboardData: d, bubbles: true, cancelable: true }));
  }, link);
  await expect(page.locator("[data-file]")).toContainText(".png");
  await expect(field).toHaveValue("");
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
