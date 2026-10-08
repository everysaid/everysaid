import { expect, test, type Page } from "@playwright/test";
import { signedIn } from "./demo";

// A chat as messages come and go: sending (from the end, from higher up, while still typing) and
// receiving (one, then another; at the end, or higher up). The demo's own source "sends" into the
// demo archive after a moment and answers 1.5 s later (not when the text has "quiet:"; with "late:"
// the sending says it is done only 1.5 s after the message is in, with "lost:" it says it failed);
// /api/demo/incoming brings a message from the other side whenever a test wants one.

const SCROLLER = "[data-stream] [data-virtuoso-scroller]";
let chat = "";

const gap = (page: Page) => page.evaluate((s) => {
  const sc = document.querySelector(s)!;
  return sc.scrollHeight - sc.scrollTop - sc.clientHeight;
}, SCROLLER);
const scrollTop = (page: Page) => page.evaluate((s) => document.querySelector(s)!.scrollTop, SCROLLER);
const up = async (page: Page, px = 1500) => {
  await page.evaluate(([s, d]) => { document.querySelector(s as string)!.scrollTop -= d as number; }, [SCROLLER, px] as const);
  await page.waitForTimeout(500);
};
const inStream = (page: Page, text: string) => page.locator("[data-stream]").getByText(text, { exact: true });
const field = (page: Page) => page.locator("[data-composer-body] textarea");
const incoming = (page: Page, text: string, service?: string) =>
  page.request.post("/api/demo/incoming", { data: { chat, text, service }, headers: { "X-Everysaid": "1" } });
const row = (page: Page, text: string) => page.locator("[data-stream] [id^=m]", { has: page.getByText(text, { exact: true }) });

async function send(page: Page, text: string, phone: boolean) {
  await field(page).fill(text);
  if (phone) await page.locator("[data-send]").click();   // on a phone Enter is a new line
  else await field(page).press("Enter");
}

async function y(page: Page, text: string) {
  return (await inStream(page, text).boundingBox())!.y;
}

test.beforeEach(async ({ page }, info) => {
  await signedIn(page, info.project.name);
  if (!chat) {
    // the person with the most messages, last active where something can send: a long chat to scroll in
    chat = await page.evaluate(async () => {
      const h = { "X-Everysaid": "1" };
      const chats = (await (await fetch("/api/chats", { headers: h })).json()).items.filter((c: any) => c.type === "person");
      let best = "", most = -1;
      for (const c of chats.slice(0, 12)) {
        const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
        if (d.sendable.includes(d.last_service) && d.person.stats.messages > most) [best, most] = [c.id, d.person.stats.messages];
      }
      return best;
    });
  }
  await page.goto(`/chat/${chat}`);
  await expect(page.locator(SCROLLER)).toBeVisible();
  await page.waitForTimeout(800);
});

test("at the end, sending: it shows at once, once, and the chat stays at its end", async ({ page }, info) => {
  const phone = info.project.name === "mobile";
  expect(await gap(page)).toBeLessThan(40);
  const words = `quiet: at the end ${Date.now()}`;
  await send(page, words, phone);
  await expect(inStream(page, words)).toBeVisible();
  await expect(field(page)).toHaveValue("");
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 10000 });
  await expect(inStream(page, words)).toHaveCount(1);
  expect(await gap(page)).toBeLessThan(40);
  if (!phone) expect(await page.evaluate(() => document.activeElement?.tagName)).toBe("TEXTAREA");
});

test("the message in before the sending says done: shown once", async ({ page }, info) => {
  const words = `quiet: late: ${Date.now()}`;
  await send(page, words, info.project.name === "mobile");
  await expect(inStream(page, words)).toBeVisible();
  await page.waitForTimeout(2500);
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0);
  await expect(inStream(page, words)).toHaveCount(1);
});

test("two of the same text at once: two, none left as being sent", async ({ page }, info) => {
  const phone = info.project.name === "mobile";
  const words = `quiet: twice ${Date.now()}`;
  await send(page, words, phone);
  await send(page, words, phone);
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 10000 });
  await expect(inStream(page, words)).toHaveCount(2);
});

test("said failed though it went: shown once, not as not sent", async ({ page }, info) => {
  const words = `quiet: lost: ${Date.now()}`;
  await send(page, words, info.project.name === "mobile");
  await page.waitForTimeout(2500);
  await expect(inStream(page, words)).toHaveCount(1);
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…|Δεν στάλθηκε|Not sent/)).toHaveCount(0);
});

test("higher up, sending: the chat goes to its end", async ({ page }, info) => {
  await up(page);
  expect(await gap(page)).toBeGreaterThan(1000);
  const words = `quiet: from higher up ${Date.now()}`;
  await send(page, words, info.project.name === "mobile");
  await expect(inStream(page, words)).toBeVisible();
  await page.waitForTimeout(600);
  expect(await gap(page)).toBeLessThan(40);
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 10000 });
  expect(await gap(page)).toBeLessThan(40);
});

test("typing while it sends: the new text is kept, the sent one shows once", async ({ page }, info) => {
  const words = `quiet: first ${Date.now()}`;
  await send(page, words, info.project.name === "mobile");
  await field(page).pressSequentially("the next one");
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 10000 });
  await expect(field(page)).toHaveValue("the next one");
  await expect(inStream(page, words)).toHaveCount(1);
});

test("at the end, one arrives and then another: the chat follows each, in order", async ({ page }) => {
  const one = `one ${Date.now()}`, two = `two ${Date.now()}`;
  await incoming(page, one);
  await expect(inStream(page, one)).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(600);
  expect(await gap(page)).toBeLessThan(40);
  await incoming(page, two);
  await expect(inStream(page, two)).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(600);
  expect(await gap(page)).toBeLessThan(40);
  expect(await y(page, one)).toBeLessThan(await y(page, two));
});

test("higher up, one arrives and then another: the chat stays where it is; they are there below", async ({ page }) => {
  await up(page);
  const at = await scrollTop(page);
  const one = `one up ${Date.now()}`, two = `two up ${Date.now()}`;
  await incoming(page, one);
  await page.waitForTimeout(1500);
  expect(Math.abs((await scrollTop(page)) - at)).toBeLessThan(5);       // no jump, up or down
  await incoming(page, two);
  await page.waitForTimeout(1500);
  expect(Math.abs((await scrollTop(page)) - at)).toBeLessThan(5);
  await page.getByRole("button", { name: /Στα πιο πρόσφατα|Latest/ }).click();
  await expect(inStream(page, two)).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(800);
  expect(await gap(page)).toBeLessThan(40);
  expect(await y(page, one)).toBeLessThan(await y(page, two));
});

test("sending, then the answer arrives: the chat follows it", async ({ page }, info) => {
  const words = `hello ${Date.now()}`;
  await send(page, words, info.project.name === "mobile");
  await expect(inStream(page, `↩ ${words}`)).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(600);
  expect(await gap(page)).toBeLessThan(40);
  expect(await y(page, words)).toBeLessThan(await y(page, `↩ ${words}`));
});

test("replying: pick a message, the bar shows it (Esc cancels), the answer goes out quoting it", async ({ page }, info) => {
  const phone = info.project.name === "mobile";
  // a service whose messages have their own id (an SMS has none: no answer to it can be sent)
  const service = await page.evaluate(async (c) => {
    const d = await (await fetch(`/api/chats/${c}`, { headers: { "X-Everysaid": "1" } })).json();
    return d.replyable.find((s: string) => s !== "sms");
  }, chat);
  const asked = `answer me ${Date.now()}`;
  await incoming(page, asked, service);
  await expect(inStream(page, asked)).toBeVisible({ timeout: 10000 });
  await page.waitForTimeout(500);

  const pick = async () => {
    if (phone) {
      // a swipe to the right on the bubble
      const box = (await inStream(page, asked).boundingBox())!;
      await page.evaluate(([x, y]) => {
        const el = document.elementFromPoint(x, y)!;
        const ev = (type: string, cx: number) => el.dispatchEvent(new PointerEvent(type, { bubbles: true, pointerType: "touch", clientX: cx, clientY: y }));
        ev("pointerdown", x); ev("pointermove", x + 40); ev("pointermove", x + 80); ev("pointerup", x + 80);
      }, [box.x + 10, box.y + box.height / 2]);
    } else {
      await row(page, asked).hover();
      await row(page, asked).locator("[data-reply]").click();
    }
    await expect(page.locator("[data-replying]")).toContainText(asked);
  };
  await pick();
  if (!phone) {
    await page.keyboard.press("Escape");
    await expect(page.locator("[data-replying]")).toHaveCount(0);
    await pick();
  }
  const words = `quiet: my answer ${Date.now()}`;
  await send(page, words, phone);
  await expect(page.locator("[data-replying]")).toHaveCount(0);
  await expect(row(page, words)).toContainText(asked);                         // quoted at once
  await expect(page.locator("[data-stream]").getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 10000 });
  await expect(inStream(page, words)).toHaveCount(1);
  await expect(row(page, words)).toContainText(asked);                         // and still, as it came back
});
