import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// The answer to a sending lost on the way (the server sent the message, the app never heard), with
// no word from the server's events either: the app asks again with the message's id, the server
// says it went (it does not send it twice), and the message stops being "sending".

test("a sending whose answer is lost: asked again, sent once", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items.filter((c: any) => c.type === "person");
    for (const c of chats.slice(0, 12)) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes(d.last_service)) return c.id as string;
    }
    return "";
  });
  test.skip(!chat, "no chat to send in");
  // the server's events never reach the app (as a connection that died without saying)
  await page.routeWebSocket("**/api/events", (ws) => {
    const server = ws.connectToServer();
    server.onMessage(() => {});         // what the server says is dropped (what the app says still goes)
  });
  let first = true;
  await page.route("**/api/chats/*/send", async (route) => {
    if (!first) return route.continue();
    first = false;
    await route.fetch();                // it reaches the server, which sends it
    await new Promise(() => {});        // the answer never comes back
  });
  await page.goto(`/chat/${chat}`);
  const text = `quiet: lost answer ${info.project.name} ${Date.now()}`;
  await page.locator("[data-composer-body] textarea").fill(text);
  await page.locator("[data-send]").click();
  const stream = page.locator("[data-stream]");
  await expect(stream.getByText(text, { exact: true })).toBeVisible();
  await expect(stream.getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 25000 });
  await expect(stream.getByText(text, { exact: true })).toHaveCount(1);
});

// Two sendings answered in the other order (the first's answer late): shown in the order they were sent.
test("two sendings answered in the other order: in the order sent", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  const chat = await page.evaluate(async () => {
    const h = { "X-Everysaid": "1" };
    const chats = (await (await fetch("/api/chats", { headers: h })).json()).items.filter((c: any) => c.type === "person");
    for (const c of chats.slice(0, 12)) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: h })).json();
      if (d.sendable.includes(d.last_service)) return c.id as string;
    }
    return "";
  });
  test.skip(!chat, "no chat to send in");
  let first = true;
  await page.route("**/api/chats/*/send", async (route) => {
    if (!first) return route.continue();
    first = false;
    const answer = await route.fetch();
    await new Promise((ok) => setTimeout(ok, 4000));        // the first's answer comes after the second's
    await route.fulfill({ response: answer });
  });
  await page.goto(`/chat/${chat}`);
  const field = page.locator("[data-composer-body] textarea");
  const stamp = Date.now();
  const one = `quiet: first ${info.project.name} ${stamp}`, two = `quiet: second ${info.project.name} ${stamp}`;
  await field.fill(one);
  await page.locator("[data-send]").click();
  await page.waitForTimeout(1500);
  await field.fill(two);
  await page.locator("[data-send]").click();
  const stream = page.locator("[data-stream]");
  await expect(stream.getByText(/Αποστολή…|Sending…/)).toHaveCount(0, { timeout: 15000 });
  const y = async (t: string) => (await stream.getByText(t, { exact: true }).boundingBox())!.y;
  expect(await y(one)).toBeLessThan(await y(two));
});
