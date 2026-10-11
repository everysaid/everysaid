import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// How far a chat was read: a chat with unread messages opens at the first of them, under a line
// "new messages"; they are read as they are seen (once a message's end has been in view), not by
// opening the chat; all of them once its end is.

const SCROLLER = "[data-stream] [data-virtuoso-scroller]";
const h = { "X-Everysaid": "1" };

const unread = (page: Page, chat: string) => page.evaluate(async ([id, hd]) => {
  const chats = (await (await fetch("/api/chats?limit=1000&archived=true", { headers: hd })).json()).items;
  return chats.find((c: any) => c.id === id)?.unread ?? -1;
}, [chat, h] as const);

test("a chat opens at its first unread, which are read as they are seen", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  // a group (one for each project: they run side by side), the least recent, archived first so that
  // the other tests' "latest group" stays theirs; read whole, then 40 messages come
  const chat = await page.evaluate(async ([hd, skip]) => {
    const groups = (await (await fetch("/api/chats?limit=1000", { headers: hd })).json()).items.filter((c: any) => c.type === "group");
    return groups[groups.length - 1 - skip].id as string;
  }, [h, info.project.name === "mobile" ? 1 : 0] as const);
  await page.request.patch(`/api/chats/${chat}`, { data: { archived: true }, headers: h });
  await page.request.post(`/api/chats/${chat}/read`, { data: {}, headers: h });
  for (let i = 0; i < 40; i++)
    await page.request.post("/api/demo/incoming", { data: { chat, text: `unread ${i}` }, headers: h });
  await expect.poll(() => unread(page, chat)).toBe(40);

  await page.goto(`/chat/${chat}`);
  await expect(page.locator(SCROLLER)).toBeVisible();
  const line = page.locator("[data-new-from]");
  await expect(line).toBeVisible();
  await expect(page.locator("[data-cursor]", { has: page.locator("[data-new-from]") }).getByText("unread 0", { exact: true })).toBeVisible();
  // the line near the top of the view, the end far below
  const box = (await page.locator(SCROLLER).boundingBox())!;
  expect((await line.boundingBox())!.y - box.y).toBeLessThan(box.height / 3);

  // what is in view is read; not the rest
  await expect.poll(() => unread(page, chat), { timeout: 5000 }).toBeLessThan(40);
  expect(await unread(page, chat)).toBeGreaterThan(0);

  // to the end: all read; the line stays while the chat is open
  for (let i = 0; i < 20; i++) {
    await page.evaluate((s) => { const el = document.querySelector(s)!; el.scrollTop = el.scrollHeight; }, SCROLLER);
    await page.waitForTimeout(150);
  }
  await expect.poll(() => unread(page, chat), { timeout: 5000 }).toBe(0);
  for (let i = 0; i < 20 && !(await line.count()); i++) {
    await page.evaluate((s) => { document.querySelector(s)!.scrollTop -= 400; }, SCROLLER);
    await page.waitForTimeout(150);
  }
  await expect(line).toHaveCount(1);

  // away and back: the line before the first not seen yet; none, all seen
  const away = () => page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const back = () => page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const toEnd = async () => {
    for (let i = 0; i < 10; i++) {
      await page.evaluate((s) => { const el = document.querySelector(s)!; el.scrollTop = el.scrollHeight; }, SCROLLER);
      await page.waitForTimeout(150);
    }
  };
  await away();
  for (let i = 0; i < 3; i++)
    await page.request.post("/api/demo/incoming", { data: { chat, text: `while away ${i}` }, headers: h });
  await page.waitForTimeout(1500);                     // brought meanwhile (below the view: not drawn)
  await back();
  await toEnd();
  await expect(page.locator("[data-cursor]", { has: line }).getByText("while away 0", { exact: true })).toHaveCount(1);
  await away();
  await back();
  await expect(line).toHaveCount(0);

  // and once the user answers
  await away();
  await page.request.post("/api/demo/incoming", { data: { chat, text: "while away again" }, headers: h });
  await page.waitForTimeout(1500);
  await back();
  await toEnd();
  await expect(page.locator("[data-cursor]", { has: line }).getByText("while away again", { exact: true })).toHaveCount(1);
  await page.locator("[data-composer-body] textarea").fill(`quiet: answered ${info.project.name}`);
  await page.locator("[data-send]").click();
  await expect(line).toHaveCount(0);

  // opened again with nothing unread: at its end, no line
  await page.reload();
  await expect(page.locator(SCROLLER)).toBeVisible();
  await page.waitForTimeout(800);
  await expect(page.locator("[data-new-from]")).toHaveCount(0);
});
