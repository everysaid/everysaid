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

async function open(page: Page, name: string, mimeType: string, project: string) {
  await signedIn(page, project);
  const group = await whatsappGroup(page);
  test.skip(!group, "no WhatsApp group the demo can send to");
  await page.goto(`/chat/${group.id}`);
  await page.locator("[data-file-input]").setInputFiles({ name, mimeType, buffer: readFileSync(`e2e/files/${name}`) });
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
    await expect(page.locator("[data-document-pages] .textLayer")).toContainText(new RegExp(`hello from ${ext}`, "i"), { timeout: 60000 });
  });
}
