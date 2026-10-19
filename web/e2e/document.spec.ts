import { existsSync, readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// A document is shown, not downloaded: a PDF in the document viewer (pdf.js), its words there to
// select; an office document as the PDF the server makes of it, where it has a program for it
// (otherwise its card only downloads it); a text file as it is (an old one in its language's old
// encoding), a Markdown one formatted.

async function whatsappGroup(page: Page) {
  return page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats?kind=group", { headers: h })).json()).items;
    for (const c of chats) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.fileable.includes("whatsapp")) return d;
    }
    return null;
  });
}

async function open(page: Page, name: string, mimeType: string, project: string, buffer?: Buffer) {
  await signedIn(page, project);
  const group = await whatsappGroup(page);
  test.skip(!group, "no WhatsApp group the demo can send to");
  await page.goto(`/chat/${group.id}`);
  await page.locator("[data-file-input]").setInputFiles({ name, mimeType, buffer: buffer ?? readFileSync(`e2e/files/${name}`) });
  await expect(page.locator("[data-file]")).toContainText(name);
  const caption = `quiet: ${name} ${project} ${Date.now()}`;
  await page.locator("[data-composer-body] textarea").fill(caption);
  await page.locator("[data-send]").click();
  return page.locator("[id^=m]", { hasText: caption }).last();
}

test("a PDF opens in the document viewer, its words there, its download beside", async ({ page }, info) => {
  const sent = await open(page, "hello.pdf", "application/pdf", info.project.name);
  const card = sent.locator("[data-document]");
  await expect(card).toBeVisible({ timeout: 15000 });
  await card.click();
  const pages = page.locator("[data-document-pages]");
  await expect(pages.locator("canvas").first()).toBeVisible();
  await expect(pages.locator(".textLayer")).toContainText("Hello from a PDF");
  await expect(page.locator("[data-document-viewer] a[download]")).toHaveAttribute("href", /\/original$/);
  await page.keyboard.press("Escape");
  await expect(pages).toHaveCount(0);

  // closed, it leaves nothing behind: a picture dropped on the chat afterwards goes to the composer
  // alone, and in the message field Backspace, Delete, Ctrl+A and Ctrl+C do what they do (a viewer
  // never let go took the picture for itself, as a stamp on the PDF, and from then on its editing keys
  // and copy ate them, while letters still went in)
  const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg==";
  const drop = await page.evaluateHandle((b64) => {
    const d = new DataTransfer();
    d.items.add(new File([Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))], "dropped.png", { type: "image/png" }));
    return d;
  }, png);
  await page.locator("[data-stream]").dispatchEvent("dragover", { dataTransfer: drop });
  await page.locator("[data-stream]").dispatchEvent("drop", { dataTransfer: drop });
  await expect(page.locator("[data-file]")).toContainText("dropped.png");
  await page.getByRole("button", { name: /Αφαίρεση αρχείου|Remove the file/ }).click();
  const field = page.locator("[data-composer-body] textarea");
  await field.click();
  await page.keyboard.type("after the PDF");
  await page.keyboard.press("Backspace");
  await expect(field).toHaveValue("after the PD");
  await page.keyboard.press("Home");
  await page.keyboard.press("Delete");
  await expect(field).toHaveValue("fter the PD");
  await page.keyboard.press("Control+a");
  await expect.poll(() => field.evaluate((el: HTMLTextAreaElement) => [el.selectionStart, el.selectionEnd])).toEqual([0, 11]);
  const copied = page.evaluate(() => new Promise<boolean>((ok) =>
    document.addEventListener("copy", (e) => setTimeout(() => ok(e.defaultPrevented)), { once: true })));
  await page.keyboard.press("Control+c");
  expect(await copied).toBe(false);                     // the browser's own copy, not cancelled
  await page.keyboard.press("Backspace");
  await expect(field).toHaveValue("");
});

test("a text file as it is, an old one in its language's old encoding", async ({ page }, info) => {
  let sent = await open(page, "hello.txt", "text/plain", info.project.name);
  await sent.locator("[data-document]").click({ timeout: 15000 });
  await expect(page.locator("[data-document-text]")).toHaveText("Hello from txt\nΓεια από ένα αρχείο κειμένου\n");
  await page.keyboard.press("Escape");
  sent = await open(page, "old-greek.txt", "text/plain", info.project.name);
  await sent.locator("[data-document]").click({ timeout: 15000 });
  await expect(page.locator("[data-document-text]")).toContainText("Γεια από παλιό αρχείο");
});

test("a large text file: its beginning, still read as UTF-8 though cut in the middle of a character", async ({ page }, info) => {
  // one byte, then two-byte letters: the 2 MB shown end in the middle of one
  const big = Buffer.from("x" + "αβγδ\n".repeat(450_000), "utf8");
  const sent = await open(page, "big.txt", "text/plain", info.project.name, big);
  await sent.locator("[data-document]").click({ timeout: 30000 });
  const text = page.locator("[data-document-text]");
  await expect(text).toContainText("xαβγδ\nαβγδ");
  expect(await text.evaluate((e) => /[ÎÏ�]/.test(e.textContent ?? ""))).toBe(false);
  await expect(page.locator("[data-document-pages]")).toContainText(/Φαίνεται μόνο η αρχή|Only the beginning/);
});

test("a Markdown file formatted, its links to a new tab", async ({ page }, info) => {
  const sent = await open(page, "hello.md", "text/markdown", info.project.name);
  await sent.locator("[data-document]").click({ timeout: 15000 });
  const md = page.locator("[data-document-markdown]");
  await expect(md.locator("h1")).toHaveText("Hello from md");
  await expect(md.locator("strong")).toHaveText("bold");
  await expect(md.locator("table td").first()).toHaveText("1");
  await expect(md.locator("a")).toHaveAttribute("target", "_blank");
});

// every kind of office document, each with the program the server chose for it
const office: [string, string][] = [
  ["doc", "application/msword"],
  ["docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"],
  ["odt", "application/vnd.oasis.opendocument.text"],
  ["rtf", "application/rtf"],
  ["xls", "application/vnd.ms-excel"],
  ["xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"],
  ["ods", "application/vnd.oasis.opendocument.spreadsheet"],
  ["ppt", "application/vnd.ms-powerpoint"],
  ["pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"],
  ["odp", "application/vnd.oasis.opendocument.presentation"],
];
for (const [ext, mime] of office) {
  test(`a .${ext} shown as the PDF the server made, where it has a program for it`, async ({ page }, info) => {
    const name = `hello.${ext}`;
    test.skip(!existsSync(`e2e/files/${name}`), `no ${name} to send`);
    const sent = await open(page, name, mime, info.project.name);
    const types: string[] = await page.evaluate(async () =>
      (await (await fetch("/api/documents", { headers: { "X-Everysaid": "1" } })).json()).types);
    if (!types.includes(mime)) { // no program here: the card downloads it
      await expect(sent.locator("a[download]")).toBeVisible({ timeout: 15000 });
      return;
    }
    await sent.locator("[data-document]").click({ timeout: 15000 });
    const pages = page.locator("[data-document-pages]");
    await expect(pages).toContainText(new RegExp(`hello from ${ext}`, "i"), { timeout: 60000 });
    if (["xls", "xlsx", "ods"].includes(ext)) await expect(pages).toContainText("second sheet"); // every sheet, not only the first
  });
}
