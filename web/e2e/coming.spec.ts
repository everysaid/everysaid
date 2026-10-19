import { expect, test } from "@playwright/test";
import { signedIn } from "./demo";

// A picture whose file has not come yet: the chat shows its place with a sign that it is loading,
// then the picture itself as soon as its file arrives, without a reload. The demo's
// /api/demo/incoming with file "coming" brings such a picture (its source says it is on its way),
// /api/demo/arrive its file.

const h = { "X-Everysaid": "1" };

test("a picture on its way shows its place, then the picture once it arrives", async ({ page }, info) => {
  await signedIn(page, info.project.name);
  // a person the demo's source reaches, among the least recent (one for each project: they run side
  // by side), with that source's service
  const [chat, service] = await page.evaluate(async ([hd, skip]) => {
    const chats = (await (await fetch("/api/chats?limit=1000", { headers: hd })).json()).items.filter((c: any) => c.type === "person");
    const found: [string, string][] = [];
    for (const c of chats.reverse()) {
      const d = await (await fetch(`/api/chats/${c.id}`, { headers: hd })).json();
      const s = ["whatsapp", "viber", "telegram"].find((x) => d.sendable?.includes(x));
      if (s) found.push([c.id, s]);
      if (found.length > skip) break;
    }
    return found[skip];
  }, [h, info.project.name === "mobile" ? 1 : 0] as const);
  expect(chat, "a person the demo's source reaches").toBeTruthy();

  await page.goto(`/chat/${chat}`);
  await expect(page.locator("[data-stream] [data-virtuoso-scroller]")).toBeVisible();
  const r = await page.request.post("/api/demo/incoming", { data: { chat, service, file: "coming" }, headers: h });
  expect(r.ok()).toBeTruthy();
  const { id } = await r.json();
  const row = page.locator(`[data-stream] #m${id}`);
  const place = row.locator("[data-file-coming]");
  await expect(place).toBeVisible();
  await expect(place).toHaveAttribute("aria-label", /Έρχεται|On its way/);
  await expect(row.locator("img")).toHaveCount(0);

  expect((await page.request.post("/api/demo/arrive", { data: { id }, headers: h })).ok()).toBeTruthy();
  await expect(place).toHaveCount(0);
  await expect(row.locator("img")).toBeVisible();
});
