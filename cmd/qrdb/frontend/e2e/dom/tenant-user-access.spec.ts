import { test, expect } from "@playwright/test";
import { startDaemon, stopDaemon, daemonRequest, UI_BASE_URL, type DomDaemon } from "./fixtures/dom-daemon.ts";

let daemon: DomDaemon | null = null;

test.afterEach(async () => {
  await stopDaemon(daemon);
  daemon = null;
});

test("probe", async ({ page }) => {
  test.setTimeout(120_000);
  daemon = await startDaemon({ seal: "none-committed", tenantUser: { tenant: "acme" } });
  const d = daemon;
  const cdb = await daemonRequest(d.adminUrl, "POST", "/v1/databases", { name: "acmedb", backend: "pebble" }, d.tenantAdmin);
  console.log("PROBE createdb", cdb.status, cdb.text.slice(0, 300));
  const dbl = await daemonRequest(d.adminUrl, "GET", "/v1/databases/list", undefined, d.tenantUser);
  console.log("PROBE dblist", dbl.status, dbl.text.slice(0, 300));
  page.on("response", (r) => {
    if (r.status() >= 300) console.log("PROBE resp", r.status(), r.request().method(), r.url().replace(UI_BASE_URL, ""));
  });
  await page.goto(UI_BASE_URL);
  await page.waitForTimeout(5000);
  console.log("PROBE aside", JSON.stringify(await page.locator("aside").innerText()));
  console.log("PROBE main", JSON.stringify((await page.locator("main").innerText()).slice(0, 600)));
  console.log("PROBE toasts", JSON.stringify(await page.locator(".toast .alert").allInnerTexts()));
  await page.locator("aside").getByText("acmedb", { exact: true }).first().click();
  await page.waitForTimeout(3000);
  console.log("PROBE main2", JSON.stringify((await page.locator("main").innerText()).slice(0, 600)));
});
