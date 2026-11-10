// Headless browser smoke test of the RASID web console.
//
// Starts the devnet with its demo IRP, opens one tab per party in one browser
// (as a presenter would), and clicks one invoice from registration to CLOSED.
// Then it re-issues the invoice under a new number, has the risk desk score
// it 80, and checks the ledger refuses to list it. It fails on any page
// error, any Content Security Policy violation, or a bid rate showing to a
// party that must not see it. Screenshots go to tests/ui/screenshots/.
//
//   node tests/ui/console.test.mjs          (needs Go and Playwright's Chromium)
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");
const shots = join(here, "screenshots");
const port = 18000 + Math.floor(Math.random() * 1000);
const url = `http://127.0.0.1:${port}`;
const work = mkdtempSync(join(tmpdir(), "rasid-ui-"));
mkdirSync(shots, { recursive: true });

const bin = join(work, "devnet");
execFileSync("go", ["build", "-o", bin, "."], { cwd: join(root, "app", "devnet"), stdio: "inherit" });
const devnet = spawn(bin, ["-listen", `127.0.0.1:${port}`, "-web", join(root, "app", "web"), "-demo-irp", "-quiet"], { stdio: ["ignore", "ignore", "pipe"] });
let devnetLog = "";
devnet.stderr.on("data", d => { devnetLog += d; });

const problems = [];
let browser;
const step = msg => console.log("  ✓ " + msg);

async function main() {
  for (let i = 0; i < 100; i++) {
    try { if ((await fetch(url + "/health")).ok) break; } catch { /* starting */ }
    await new Promise(r => setTimeout(r, 100));
  }
  browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 860 } });
  const tabs = {};
  for (const who of ["npci", "msme", "risk", "buyer", "citi", "nbfc"]) {
    const p = await ctx.newPage();
    p.on("pageerror", e => problems.push(`${who}: page error: ${e.message}`));
    p.on("console", m => {
      if (m.type() === "error" && !/401|Failed to load resource/.test(m.text())) problems.push(`${who}: console error: ${m.text()}`);
      if (/Content Security Policy/i.test(m.text())) problems.push(`${who}: CSP violation: ${m.text()}`);
    });
    await p.goto(url);
    await p.locator(".acct", { hasText: who }).first().click();
    await p.locator("#app:not([hidden])").waitFor();
    tabs[who] = p;
  }
  step("six parties signed in, one tab each");

  const { npci, msme, risk, buyer, citi, nbfc } = tabs;
  const ok = async (p, text) => p.locator("#toast.ok", { hasText: text }).waitFor({ timeout: 10000 });
  const button = (p, name) => p.locator("#detail button, section button", { hasText: name }).first();
  const open = async (p, card) => {
    const row = p.locator("section", { has: p.locator("h2", { hasText: card }) }).locator(".row[data-id]").first();
    await row.waitFor({ timeout: 10000 });
    await row.click();
    await p.locator("#detail h2", { hasText: "Invoice #" }).waitFor();
  };

  await npci.locator(".net", { hasText: "DRUNIX devnet" }).waitFor();
  await button(npci, "Use the demo IRP").click();
  await ok(npci, "IRP key trusted");
  step("NPCI trusted the demo IRP key");

  await button(msme, "Create a sample e-invoice").click();
  await msme.locator(".picked:not([hidden])").waitFor();
  await button(msme, "Register invoice").click();
  await ok(msme, "Invoice registered");
  await msme.locator("#toast small", { hasText: "Recorded on the ledger in block" }).waitFor();
  step("MSME registered a sample e-invoice; the ledger receipt shows its block");

  await open(risk, "Awaiting a score");
  await button(risk, "Record score").click();
  await ok(risk, "Score recorded");
  await open(buyer, "Needs your action");
  await button(buyer, "Accept").click();
  await ok(buyer, "Invoice accepted");
  await button(buyer, "Set up e-NACH mandate").click();
  await ok(buyer, "e-NACH mandate registered");
  step("risk desk scored it; buyer accepted and set up e-NACH");

  await open(msme, "Your invoices");
  await button(msme, "List for bids").click();
  await ok(msme, "Listed");
  for (const [p, units, rate] of [[citi, "6000", "10.5"], [nbfc, "4000", "11"]]) {
    await open(p, "Open for bids");
    const f = p.locator("#detail .form").first();
    await f.locator("input").nth(0).fill(units);
    await f.locator("input").nth(1).fill(rate);
    await button(p, "Place sealed bid").click();
    await ok(p, "Sealed bid placed");
  }
  step("Citi and the NBFC placed sealed bids");

  // Privacy in the screens: the NBFC never sees Citi's rate; the buyer sees none.
  await nbfc.reload(); await open(nbfc, "Open for bids");
  if (await nbfc.locator("#detail", { hasText: "10.50%" }).count()) problems.push("the NBFC sees Citi's rate");
  await open(buyer, "All your payables");
  if (await buyer.locator("#detail", { hasText: /1[01]\.\d\d%/ }).count()) problems.push("the buyer sees a bid rate");
  await open(msme, "Your invoices");
  await msme.locator("#detail", { hasText: "10.50%" }).waitFor();
  await button(msme, "Award 2 bids").click();
  await ok(msme, "Bids awarded");
  step("MSME saw both rates and awarded both bids; other parties saw none");

  for (const p of [citi, nbfc]) {
    await open(p, "Your portfolio");
    await button(p, "by RTGS").click();
    await ok(p, "Payment confirmed");
  }
  await open(npci, "Settlement queue");
  await button(npci, "via e-NACH").click();
  await ok(npci, "Repayment collected");
  for (let i = 0; i < 2; i++) {
    await button(npci, "Pay ₹").click();
    await ok(npci, "Payout made");
  }
  await npci.locator("#detail .steps .now", { hasText: "Closed" }).waitFor();
  const trail = await npci.locator("#detail .trail li").count();
  if (trail < 8) problems.push(`ledger trail has ${trail} entries, expected at least 8`);
  step(`funded, repaid and closed; ledger trail shows ${trail} versions`);

  // The AI demo: a re-issued invoice scored 80 by hand cannot be listed.
  await button(msme, "Re-issue the last one").click();
  await msme.locator(".picked", { hasText: "Re-issued copy" }).waitFor();
  await button(msme, "Register invoice").click();
  await ok(msme, "Invoice registered");
  await open(buyer, "Needs your action");
  await button(buyer, "Accept").click();
  await ok(buyer, "Invoice accepted");
  await open(risk, "Awaiting a score");
  await risk.locator("#detail .form input").first().fill("80");
  await risk.locator("#detail .form input").nth(1).fill("NEAR_DUPLICATE");
  await button(risk, "Record score").click();
  await ok(risk, "Score recorded");
  await msme.locator("section", { has: msme.locator("h2", { hasText: "Your invoices" }) }).locator(".row[data-id]", { hasText: "Accepted" }).first().click();
  await button(msme, "List for bids").click();
  await msme.locator("#toast:not(.ok)", { hasText: "exceeds listing limit" }).waitFor({ timeout: 10000 });
  step("a re-issued invoice scored 80 was refused listing by the ledger");

  // Filters and confirm steps.
  await npci.locator(".tools select").selectOption("done");
  const closedRows = await npci.locator("section", { has: npci.locator("h2", { hasText: "All invoices" }) }).locator(".row[data-id]").count();
  if (closedRows !== 1) problems.push(`"Closed" filter shows ${closedRows} rows, expected 1`);
  await npci.locator(".tools select").selectOption("");
  step("stage filter works");

  for (const [who, p] of Object.entries(tabs)) await p.screenshot({ path: join(shots, `${who}.png`), fullPage: true });
  step(`screenshots in ${shots}`);
}

let failed = false;
try {
  await main();
} catch (e) {
  failed = true;
  console.error("UI test failed: " + e.message);
  if (devnetLog) console.error(devnetLog.slice(-2000));
} finally {
  if (browser) await browser.close();
  devnet.kill();
  rmSync(work, { recursive: true, force: true });
}
if (problems.length) { failed = true; console.error("Problems:\n  " + problems.join("\n  ")); }
if (failed) process.exit(1);
console.log("UI smoke test passed");
