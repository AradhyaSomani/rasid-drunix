"use strict";
// Each login acts as exactly one ledger identity, decided by the server: the
// devnet maps the account to an identity, a live gateway signs with its own
// certificate. The screens below only choose what to show that identity.

const $ = id => document.getElementById(id);
const esc = v => String(v ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const rupees = p => "₹" + (p / 100).toLocaleString("en-IN", { maximumFractionDigits: 2 });
const count = n => Number(n).toLocaleString("en-IN");
const pct = bps => (bps / 100).toFixed(2) + "%";
const short = id => "#" + esc(String(id).slice(0, 8));
const today = () => new Date().toLocaleDateString("en-CA");
const day = (d, year) => d ? new Date(d + "T00:00:00").toLocaleDateString("en-IN", year ? { day: "numeric", month: "short", year: "numeric" } : { day: "numeric", month: "short" }) : "–";
const daysTo = d => Math.round((Date.parse(d) - Date.parse(today())) / 864e5);
const digits = n => Array.from(crypto.getRandomValues(new Uint8Array(n)), b => b % 10).join("");
const el = (tag, cls, html) => { const e = document.createElement(tag); if (cls) e.className = cls; if (html) e.innerHTML = html; return e; };
function stored(store, key, fallback) { try { return JSON.parse(store.getItem(key)) ?? fallback; } catch { return fallback; } }
function keep(store, key, v) { try { v == null ? store.removeItem(key) : store.setItem(key, JSON.stringify(v)); } catch { /* storage off */ } }

const ORG = { NPCIMSP: "NPCI", CitiMSP: "Citi", NbfcMSP: "NBFC", SupplierBankMSP: "Supplier bank", BuyerBankMSP: "Buyer bank", BankAMSP: "Bank A",
  Org1MSP: "NPCI (DRUNIX Org1)", Org2MSP: "Citi (DRUNIX Org2)" };
const org = msp => ORG[msp] || String(msp).replace(/MSP$/, "");
const ROLE = { operator: "Network operator", risk_oracle: "Risk desk", supplier: "Supplier", buyer: "Buyer", financier: "Financier" };
const ROLE_ORDER = ["operator", "supplier", "risk_oracle", "buyer", "financier"];
const STATUS = { REGISTERED: "Registered", ACCEPTED: "Accepted", LISTED: "Listed", FUNDING_PENDING: "Awarded", FUNDED: "Funded",
  OVERDUE: "Overdue", DEFAULTED: "Defaulted", REPAID: "Repaid", CLOSED: "Closed", REJECTED: "Rejected", CANCELLED: "Cancelled" };
const tone = s => ["REJECTED", "CANCELLED", "OVERDUE", "DEFAULTED"].includes(s) ? "bad" : ["FUNDED", "CLOSED"].includes(s) ? "ok" : "";
const PATH = ["REGISTERED", "ACCEPTED", "LISTED", "FUNDING_PENDING", "FUNDED", "REPAID", "CLOSED"];
const OPEN = ["REGISTERED", "ACCEPTED", "LISTED"];
// Scores the risk desk types in carry this model name; anything else came from the AI.
const MANUAL = "risk-desk-manual";
const byAI = r => !!r.risk && r.risk.modelVersion !== MANUAL;
const REASON = { CIRCULAR_TRADE: "circular trading", NEAR_DUPLICATE: "near-duplicate of another invoice", VELOCITY_SPIKE: "sudden spike in invoices",
  NEW_PAIR_LARGE: "large first invoice between these parties", BUYER_PAYS_LATE: "buyer pays late", ROUND_AMOUNT: "round amount",
  NOT_E_INVOICE: "not an e-invoice", INFLATED_AMOUNT: "amount far above this relationship's usual",
  UNPROVEN_BUYER: "buyer has no track record", THIN_HISTORY: "supplier is new to the network", OK: "nothing flagged" };
const reasons = r => (r.risk.reasonCodes || []).map(c => REASON[c] || c).join(", ") || "nothing flagged";
const BID = { SEALED: "Sealed", AWARDED: "Awarded", LOST: "Not awarded", WITHDRAWN: "Withdrawn" };
const EVENT = { ReceivableRegistered: "Invoice registered", ReceivableAccepted: "Buyer accepted", ReceivableRejected: "Buyer rejected",
  ReceivableCancelled: "Supplier cancelled", RiskScored: "Risk scored", ReceivableListed: "Listed for bids", BidSubmitted: "Sealed bid placed",
  FundingRequested: "Bids awarded", AwardsLapsed: "Unpaid awards lapsed", FundingConfirmed: "Funding paid", ReceivableFunded: "Fully funded",
  MandateRegistered: "e-NACH mandate set up", UnitsTransferred: "Units transferred", RepaymentReceived: "Repayment received",
  PayoutConfirmed: "Payout made", ReceivableClosed: "Closed", ReceivableOverdue: "Marked overdue", ReceivableDefaulted: "Marked defaulted" };

let BASES = [""];      // devnet serves this page itself; a live network lists one gateway per organisation
let DEMO = null;       // demo password, when the server publishes one
let ACCOUNTS = [];
let S = null;          // { token, base }, kept per tab so each tab can be a different party
let ME = null;
let RS = [];
let selected = null, shownSig = "", es = null;
let HEALTH = {};        // what /health says about the ledger this tab talks to
let HEIGHT = 0, LIVE = "connecting", LOADED = false, MORE = false;
const FILTER = { q: "", status: "" };
const known = new Set(), FEED = [];

let toastTimer;
function toast(msg, ok, receipt) {
  const t = $("toast");
  t.textContent = msg;
  if (receipt) t.appendChild(el("small", "", esc(receipt)));
  t.className = ok ? "ok" : ""; t.style.display = "block";
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.style.display = "none", ok ? 4500 : 7000);
}
// The ledger's proof that it recorded a transaction: its ID and block.
const receipt = out => out && out.txId ? `Recorded on the ledger${out.block ? ` in block ${count(out.block)}` : ""} · tx ${String(out.txId).slice(0, 16)}` : "";

// SHA-256 as hex. crypto.subtle needs a secure origin (localhost counts); the
// fallback covers a plain-http address.
async function sha256(s) {
  if (crypto.subtle) {
    const b = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s));
    return [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, "0")).join("");
  }
  const bytes = new TextEncoder().encode(s), K = [];
  let H = [];
  const prime = n => { for (let d = 2; d * d <= n; d++) if (n % d === 0) return false; return true; };
  const frac = x => ((x - Math.floor(x)) * 4294967296) | 0;
  for (let n = 2; K.length < 64; n++) if (prime(n)) { if (H.length < 8) H.push(frac(Math.sqrt(n))); K.push(frac(Math.cbrt(n))); }
  const l = bytes.length, words = new Int32Array((((l + 8) >> 6) + 1) << 4), w = new Int32Array(64);
  for (let i = 0; i < l; i++) words[i >> 2] |= bytes[i] << (24 - (i % 4) * 8);
  words[l >> 2] |= 0x80 << (24 - (l % 4) * 8);
  words[words.length - 1] = l * 8;
  const rot = (x, n) => (x >>> n) | (x << (32 - n));
  for (let j = 0; j < words.length; j += 16) {
    let [a, b, c, d, e, f, g, h] = H;
    for (let i = 0; i < 64; i++) {
      if (i < 16) w[i] = words[j + i];
      else { const x = w[i - 15], y = w[i - 2]; w[i] = (rot(x, 7) ^ rot(x, 18) ^ (x >>> 3)) + w[i - 7] + (rot(y, 17) ^ rot(y, 19) ^ (y >>> 10)) + w[i - 16]; }
      const t1 = h + (rot(e, 6) ^ rot(e, 11) ^ rot(e, 25)) + ((e & f) ^ (~e & g)) + K[i] + w[i];
      const t2 = (rot(a, 2) ^ rot(a, 13) ^ rot(a, 22)) + ((a & b) ^ (a & c) ^ (b & c));
      h = g; g = f; f = e; e = (d + t1) | 0; d = c; c = b; b = a; a = (t1 + t2) | 0;
    }
    H = [a, b, c, d, e, f, g, h].map((x, i) => (x + H[i]) | 0);
  }
  return H.map(x => (x >>> 0).toString(16).padStart(8, "0")).join("");
}

// ---- Server calls -------------------------------------------------------

async function api(path, opts = {}) {
  const r = await fetch(S.base + path, { ...opts, headers: { "Content-Type": "application/json", Authorization: "Bearer " + S.token } });
  const out = r.status === 204 ? null : await r.json().catch(() => ({}));
  if (r.status === 401) { signedOut("Your session has ended. Sign in again."); throw new Error("signed out"); }
  if (!r.ok) throw new Error(out.error || r.statusText);
  return out;
}
async function tx(fn, args, transient, opts = {}) {
  const [c, f] = fn.split(":");
  try {
    const out = await api(`/tx/${c}/${f}`, { method: "POST", body: JSON.stringify({ args, transient }) });
    if (out && out.block) setHeight(out.block);
    if (opts.done) toast(opts.done, true, receipt(out));
    return out;
  } catch (e) {
    if (!opts.quiet && S) toast(e.message);
    throw e;
  }
}
function query(fn, ...args) {
  const [c, f] = fn.split(":");
  return api(`/query/${c}/${f}?` + args.map(a => "arg=" + encodeURIComponent(a)).join("&"));
}

// ---- Sign-in ------------------------------------------------------------

async function boot() {
  try {
    const r = await fetch("gateways.json", { cache: "no-store" });
    if (r.ok) {
      const g = await r.json();
      BASES = g.gateways || [...new Set(Object.values(g))];
      if (g.demoPassword) DEMO = g.demoPassword;
    }
  } catch { /* devnet: this server is the only one */ }
  const saved = stored(sessionStorage, "rasid-session", null);
  if (saved) {
    S = saved;
    try { return await enter(await api("/auth/me")); } catch { /* expired or server restarted */ }
  }
  showLogin();
}

function showLogin(msg) {
  $("app").hidden = true;
  $("login").hidden = false;
  $("loginErr").textContent = msg || "";
  $("pass").value = "";
  loadAccounts();
}

async function loadAccounts() {
  const list = [];
  await Promise.all(BASES.map(async base => {
    try {
      const r = await fetch(base + "/auth/accounts");
      if (!r.ok) return;
      const j = await r.json();
      if (j.demoPassword) DEMO = j.demoPassword;
      for (const a of j.accounts || []) list.push({ ...a, base });
    } catch { /* that gateway is down */ }
  }));
  const rank = a => ROLE_ORDER.indexOf(a.identity.role);
  ACCOUNTS = list.sort((a, b) => rank(a) - rank(b) || a.label.localeCompare(b.label));
  const box = $("accts");
  box.innerHTML = ACCOUNTS.length ? `<h3>Demo accounts</h3><p class="muted small">${DEMO ? `Every demo account uses the password <b>${esc(DEMO)}</b>. Click one to sign in.` : "Click one to fill in the username."} Use a separate browser tab for each party.</p>` : "";
  for (const a of ACCOUNTS) {
    const b = el("button", "acct", `<b>${esc(a.label)}</b><small>${esc(ROLE[a.identity.role] || a.identity.role)} · ${esc(a.identity.msp)}</small><code>${esc(a.username)}</code>`);
    b.type = "button";
    b.onclick = () => {
      $("user").value = a.username;
      if (DEMO) signIn(a.username, DEMO); else $("pass").focus();
    };
    box.appendChild(b);
  }
}

async function signIn(username, password) {
  $("loginErr").textContent = "";
  const hit = ACCOUNTS.find(a => a.username === username.trim().toLowerCase());
  for (const base of hit ? [hit.base] : BASES) {
    try {
      const r = await fetch(base + "/auth/login", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ username, password }) });
      if (!r.ok) continue;
      const j = await r.json();
      S = { token: j.token, base };
      keep(sessionStorage, "rasid-session", S);
      return enter(j.user);
    } catch { /* try the next gateway */ }
  }
  $("loginErr").textContent = "Wrong username or password.";
}
$("loginForm").onsubmit = e => { e.preventDefault(); signIn($("user").value, $("pass").value); };

async function signOut() {
  if (S) fetch(S.base + "/auth/logout", { method: "POST", headers: { Authorization: "Bearer " + S.token } }).catch(() => {});
  signedOut();
}
$("signOut").onclick = signOut;

function signedOut(msg) {
  S = ME = selected = null;
  shownSig = ""; RS = []; FEED.length = 0; known.clear(); LOADED = false; MORE = false; HEIGHT = 0; LIVE = "connecting";
  stopStream();
  keep(sessionStorage, "rasid-session", null);
  showLogin(msg);
}

async function enter(user) {
  ME = { username: user.username, label: user.label, ...user.identity };
  ME.partyId = ME.role === "financier" ? "FIN:" + ME.msp
    : ME.gstin ? await sha256("GSTIN:" + ME.gstin.trim().toUpperCase()) : ME.msp;
  $("meLabel").textContent = ME.label;
  $("meDesc").textContent = [ROLE[ME.role] || ME.role, ME.msp, ME.gstin && "GSTIN " + ME.gstin].filter(Boolean).join(" · ");
  document.title = `${ME.label} · RASID`;
  await health();
  buildWorkspace();
  renderDetail(null);
  renderFeed();
  $("login").hidden = true;
  $("app").hidden = false;
  listen();
  await load();
}

// ---- Network bar ------------------------------------------------------------

async function health() {
  try { HEALTH = await (await fetch(S.base + "/health", { cache: "no-store" })).json(); } catch { HEALTH = {}; }
  if (HEALTH.height) HEIGHT = HEALTH.height;
  renderNet();
}
function setHeight(h) { if (+h > HEIGHT) { HEIGHT = +h; renderNet(); } }
function setLive(state) { if (state !== LIVE) { LIVE = state; renderNet(); } }
function renderNet() {
  const n = $("net");
  if (!n) return;
  const name = HEALTH.mode === "devnet" ? "DRUNIX devnet" : HEALTH.ledger || (HEALTH.msp ? `Ledger via ${org(HEALTH.msp)} gateway` : "Ledger");
  const ai = HEALTH.aiRisk === "on" ? " · AI scoring on" : HEALTH.aiRisk === "unreachable" ? " · AI scoring offline" : "";
  const state = { live: "live", connecting: "connecting", off: "offline" }[LIVE];
  n.innerHTML = `<span class="dot ${LIVE === "live" ? "live" : LIVE === "off" ? "off" : ""}"></span>
    <span><b>${esc(name)}</b> · ${esc(state)}${HEIGHT ? ` · <span class="h">block ${count(HEIGHT)}</span>` : ""}${esc(ai)}</span>`;
  n.title = HEALTH.ledger || "";
}

// ---- Workspaces, one per role ---------------------------------------------

const own = r => r.supplierId === ME.partyId;
const owes = r => r.buyerId === ME.partyId;
const won = r => (r.awards || []).some(a => a.financierMsp === ME.msp);
const needsMandate = r => !r.mandateRef && ["ACCEPTED", "LISTED", "FUNDING_PENDING", "FUNDED", "OVERDUE", "DEFAULTED"].includes(r.status);

const WS = {
  operator: {
    intro: "Set up the network, collect repayments through e-NACH and pay out every holder.",
    cards: [setupCard,
      { title: "Settlement queue", filter: r => ["FUNDED", "OVERDUE", "DEFAULTED", "REPAID"].includes(r.status), empty: "Nothing to collect or pay out." },
      { title: "All invoices", filter: () => true, empty: "Nothing registered yet." }],
  },
  risk_oracle: {
    intro: "The AI risk service scores each invoice for fraud and credit risk as it is registered. Check its scores here and override one when you have evidence it missed. The ledger will not list or award anything above the risk limit.",
    cards: [
      { title: "Awaiting a score", filter: r => OPEN.includes(r.status) && !r.risk, empty: "Every open invoice has a score." },
      { title: "Scored by the AI", filter: byAI, empty: "Nothing scored by the AI yet." },
      { title: "Set by hand", filter: r => !!r.risk && !byAI(r), empty: "No scores set by hand." }],
  },
  supplier: {
    intro: "Register your GST e-invoices, list them for bids and take the best offers.",
    cards: [registerCard, { title: "Your invoices", filter: own, empty: "Register a signed e-invoice to start." }],
  },
  buyer: {
    intro: "Confirm the invoices your suppliers raise on you and set up the e-NACH mandate that repays them.",
    cards: [
      { title: "Needs your action", filter: r => owes(r) && (r.status === "REGISTERED" || needsMandate(r)), empty: "Nothing waiting for you." },
      { title: "All your payables", filter: owes, empty: "No invoices have been raised on you yet." }],
  },
  financier: {
    intro: "Bid privately on listed invoices. Only you and the supplier ever see your rate.",
    cards: [
      { title: "Open for bids", filter: r => r.status === "LISTED", empty: "No invoices are open for bids right now." },
      { title: "Your portfolio", filter: won, empty: "You have not won any invoices yet." }],
  },
};
let CARDS = [];

function buildWorkspace() {
  const ws = $("workspace"), def = WS[ME.role];
  ws.innerHTML = "";
  CARDS = [];
  if (!def) { ws.innerHTML = `<section><p class="muted">This account has no RASID role.</p></section>`; return; }
  ws.appendChild(el("p", "intro", esc(def.intro)));
  ws.appendChild(el("div", "kpis"));
  const tools = el("div", "tools", `<input type="search" placeholder="Find an invoice by number or amount" aria-label="Find an invoice">
    <select aria-label="Show"><option value="">All stages</option><option value="open">Open</option><option value="money">Funded or in settlement</option>
    <option value="done">Closed</option><option value="bad">Rejected, overdue or defaulted</option></select>`);
  const [q, st] = tools.querySelectorAll("input, select");
  q.value = FILTER.q; st.value = FILTER.status;
  q.oninput = () => { FILTER.q = q.value.trim().toLowerCase(); renderLists(); };
  st.onchange = () => { FILTER.status = st.value; renderLists(); };
  for (const c of def.cards) {
    if (typeof c === "function") { ws.appendChild(c()); continue; }
    if (!CARDS.length) ws.appendChild(tools);
    const s = el("section", "", `<h2>${esc(c.title)}<span class="n"></span></h2><div class="rows"><p class="empty">Loading from the ledger…</p></div>`);
    CARDS.push({ ...c, n: s.querySelector(".n"), rows: s.querySelector(".rows") });
    ws.appendChild(s);
  }
}

const STAGE = { open: OPEN, money: ["FUNDING_PENDING", "FUNDED", "REPAID"], done: ["CLOSED"], bad: ["REJECTED", "CANCELLED", "OVERDUE", "DEFAULTED"] };
function matches(r) {
  if (FILTER.status && !STAGE[FILTER.status].includes(r.status)) return false;
  if (!FILTER.q) return true;
  const q = FILTER.q.replace(/^#/, "").replace(/[₹,\s]/g, "");
  return r.id.toLowerCase().startsWith(q) || String(r.amountPaise / 100).startsWith(q) || (STATUS[r.status] || "").toLowerCase().startsWith(FILTER.q);
}

function renderLists() {
  renderKpis();
  for (const c of CARDS) {
    if (!LOADED) continue;
    const rs = RS.filter(c.filter).filter(matches);
    c.n.textContent = rs.length || "";
    const html = rs.length
      ? `<div class="row head"><span>Invoice</span><span class="num">Amount</span><span>Due</span><span class="num risk">Risk</span><span>Status</span></div>` +
        rs.map(r => `<div class="row${r.id === selected ? " sel" : ""}" data-id="${esc(r.id)}" tabindex="0" role="button">
          <span>${short(r.id)}</span><span class="num">${rupees(r.amountPaise)}</span><span>${day(r.dueDate)}</span>
          <span class="num risk">${r.risk ? esc(r.risk.score) : "–"}</span><span class="st ${tone(r.status)}">${esc(STATUS[r.status] || r.status)}</span></div>`).join("")
      : `<p class="empty">${esc(FILTER.q || FILTER.status ? "Nothing matches the filter." : c.empty)}</p>`;
    // Replace the rows only when they changed, so a click in progress is not lost.
    if (html !== c.html) c.rows.innerHTML = c.html = html;
  }
}
// Headline numbers for the signed-in party, from the invoices it can see.
function renderKpis() {
  const box = document.querySelector(".kpis");
  if (!box || !ME) return;
  const sum = (rs, f) => rs.reduce((n, r) => n + (f(r) || 0), 0);
  const lakh = p => p >= 1e9 ? "₹" + (p / 1e9).toLocaleString("en-IN", { maximumFractionDigits: 2 }) + " cr" : p >= 1e7 ? "₹" + (p / 1e7).toLocaleString("en-IN", { maximumFractionDigits: 2 }) + " lakh" : rupees(p);
  let k = [];
  if (ME.role === "operator") {
    k = [[count(RS.length) + (MORE ? "+" : ""), MORE ? "invoices (latest shown)" : "invoices on the ledger"], [lakh(sum(RS, r => r.fundedPaise)), "financed to MSMEs"],
      [count(RS.filter(r => ["FUNDED", "OVERDUE", "DEFAULTED", "REPAID"].includes(r.status)).length), "in settlement"],
      [count(RS.filter(r => r.status === "CLOSED").length), "closed"]];
  } else if (ME.role === "risk_oracle") {
    const limit = 70, over = RS.filter(r => r.risk && r.risk.score > limit && OPEN.includes(r.status)).length;
    k = [[count(RS.filter(r => OPEN.includes(r.status) && !r.risk).length), "awaiting a score"], [count(RS.filter(byAI).length), "scored by the AI"],
      [count(over), `blocked, over ${limit}`, over > 0], [count(RS.filter(r => r.risk && !byAI(r)).length), "set by hand"]];
  } else if (ME.role === "supplier") {
    const mine = RS.filter(own);
    k = [[lakh(sum(mine, r => r.amountPaise)), "invoiced on RASID"], [lakh(sum(mine, r => r.fundedPaise)), "cash received"],
      [count(mine.filter(r => r.status === "LISTED").length), "open for bids"], [count(mine.filter(r => OPEN.includes(r.status)).length), "not yet financed"]];
  } else if (ME.role === "buyer") {
    const mine = RS.filter(owes), live = mine.filter(r => !["CLOSED", "REJECTED", "CANCELLED", "REPAID"].includes(r.status));
    k = [[lakh(sum(live, r => r.amountPaise - r.repaidPaise)), "payable"], [count(mine.filter(r => r.status === "REGISTERED" || needsMandate(r)).length), "need your action"],
      [count(live.filter(r => daysTo(r.dueDate) <= 30).length), "due in 30 days"], [count(mine.filter(r => r.breach45Days).length), "paid past 45 days", mine.some(r => r.breach45Days)]];
  } else if (ME.role === "financier") {
    const aw = RS.flatMap(r => (r.awards || []).filter(a => a.financierMsp === ME.msp && a.confirmed).map(a => ({ a, r })));
    const lent = sum(aw, x => x.a.fundingPaise), due = sum(aw, x => Math.floor(x.r.amountPaise * x.a.units / 10000));
    k = [[lakh(lent), "deployed"], [lakh(due), "back at maturity"], [lent ? pct(Math.round((due - lent) / lent * 10000)) : "–", "discount earned"],
      [count(RS.filter(r => r.status === "LISTED").length), "open for bids"]];
  }
  const html = k.map(([v, label, warn]) => `<div class="kpi${warn ? " warn" : ""}"><div class="v">${esc(v)}</div><div class="k">${esc(label)}</div></div>`).join("");
  if (box.dataset.html !== html) box.innerHTML = box.dataset.html = html;
}

$("workspace").addEventListener("click", e => { const row = e.target.closest(".row[data-id]"); if (row) pick(row.dataset.id); });
$("workspace").addEventListener("keydown", e => { const row = e.target.closest(".row[data-id]"); if (row && e.key === "Enter") pick(row.dataset.id); });

async function pick(id) {
  await showDetail(id, true);
  if (matchMedia("(max-width: 900px)").matches) $("detail").scrollIntoView({ behavior: "smooth" });
}

function setupCard() {
  const s = el("section", "", `<h2>Network setup</h2>
    <p class="note">Trust the GST IRP's public key so the ledger can check e-invoice signatures. Do this once per network.</p>
    <textarea placeholder="Paste irp-public.pem here"></textarea><div class="form"></div>`);
  const trust = act("Trust IRP key", async () => {
    let pem = s.querySelector("textarea").value.trim();
    if (!pem) { toast("Paste the IRP public key first."); return; }
    // Accept the key body alone, as people often copy it without the BEGIN/END lines.
    if (!pem.includes("-----BEGIN")) pem = `-----BEGIN PUBLIC KEY-----\n${pem.replace(/\s+/g, "")}\n-----END PUBLIC KEY-----\n`;
    await tx("InvoiceRegistry:InitLedger", [ME.msp, [ME.msp]], null, { quiet: true }).catch(() => {}); // refused once the ledger is set up
    await tx("InvoiceRegistry:SetIRPPublicKey", ["irp-2026", pem], null, { done: "IRP key trusted. Suppliers can now register e-invoices." });
  });
  s.querySelector(".form").appendChild(trust);
  if (HEALTH.demoIrp) {
    s.querySelector(".form").appendChild(act("Use the demo IRP's key", async () => {
      const k = await api("/demo/irp-key");
      s.querySelector("textarea").value = k.pem;
      trust.click();
    }, true));
  }
  return s;
}

function registerCard() {
  const s = el("section", "", `<h2>Register an e-invoice</h2>
    <p class="note">Paste the signed QR payload from the GST IRP. The ledger checks the signature and refuses an invoice already registered anywhere on the network.</p>
    <textarea placeholder="Signed QR from tools/mock-irp"></textarea><p class="picked" hidden></p>
    <div class="form"><label>Due date<input type="date"></label></div>`);
  const due = s.querySelector("input"), box = s.querySelector("textarea"), picked = s.querySelector(".picked");
  box.oninput = () => { picked.hidden = true; };
  if (HEALTH.demoIrp) {
    // Demo only: the devnet's built-in mock IRP signs an invoice for this
    // supplier's GSTIN, standing in for the GST portal.
    const demo = el("div", "demo");
    let last = stored(sessionStorage, "rasid-last-invoice", null);
    const sample = async (body, note) => {
      const out = await api("/demo/invoice", { method: "POST", body: JSON.stringify(body) });
      box.value = out.qr;
      last = out.invoice;
      keep(sessionStorage, "rasid-last-invoice", last);
      picked.hidden = false;
      picked.textContent = `${note}: ${out.invoice.DocNo}, ${rupees(Math.round(out.invoice.TotInvVal * 100))}, buyer GSTIN ${out.invoice.BuyerGstin}, signed by the demo IRP.`;
    };
    demo.appendChild(act("Create a sample e-invoice", () => sample({ rupees: 1000000 }, "Sample"), true));
    demo.appendChild(act("Re-issue the last one under a new number", () => {
      if (!last) { toast("Create a sample e-invoice first."); return; }
      const n = String(last.DocNo).match(/(\d+)$/), docNo = n ? last.DocNo.slice(0, -n[1].length) + String(+n[1] + 1).padStart(n[1].length, "0") : last.DocNo + "A";
      return sample({ rupees: last.TotInvVal, buyerGstin: last.BuyerGstin, docNo }, "Re-issued copy");
    }, true));
    s.insertBefore(demo, box);
  }
  due.value = new Date(Date.now() + 60 * 864e5).toLocaleDateString("en-CA");
  s.querySelector(".form").appendChild(act("Register invoice", async () => {
    const qr = s.querySelector("textarea").value.trim();
    if (!qr) { toast("Paste the signed QR first."); return; }
    const out = await tx("InvoiceRegistry:RegisterEInvoice", [qr, due.value], null, { done: "Invoice registered. IRP signature verified." });
    s.querySelector("textarea").value = "";
    picked.hidden = true;
    known.add(out.result.id);
    selected = out.result.id;
  }));
  return s;
}

// ---- Data -----------------------------------------------------------------

async function load() {
  let page;
  try { page = await query("InvoiceRegistry:ListReceivables", "", 200, ""); } catch (e) { if (S) toast(e.message); return; }
  const rs = page.records || [];
  MORE = rs.length >= 200; // the ledger holds more than this page; counts below cover the page
  const have = new Set(rs.map(r => r.id));
  // Receivables beyond the first page that this tab has seen through events.
  const extra = await Promise.all([...known].filter(id => !have.has(id)).map(id => query("InvoiceRegistry:GetReceivable", id).catch(() => null)));
  RS = [...rs, ...extra.filter(Boolean)].sort((a, b) => String(b.createdAt).localeCompare(String(a.createdAt)));
  LOADED = true;
  renderLists();
}

async function refresh() {
  if (!S) return;
  await load();
  if (selected) await showDetail(selected);
}
let soonTimer;
const soon = () => { clearTimeout(soonTimer); soonTimer = setTimeout(refresh, 150); };

// Bid reveals stand in for each bank sending its rate to the supplier off the
// ledger. They live in this browser's localStorage, so every tab of the demo
// shares them; each is checked against the hash committed on the ledger.
const reveals = () => stored(localStorage, "rasid-reveals", {});
async function liveReveals(id, bids) {
  const out = [];
  for (const rv of reveals()[id] || []) {
    const b = bids.find(x => x.bidId === rv.bidId);
    if (b && await sha256(`${id}|${rv.bidId}|${rv.units}|${rv.rateBps}|${rv.salt}`) === b.hash) out.push({ ...rv, bid: b });
  }
  return out;
}
// The supplier's choice: cheapest still-sealed bids first, up to the 10,000 units on offer.
function bestAwards(revs) {
  let left = 10000;
  return revs.filter(rv => rv.bid.status === "SEALED").sort((a, b) => a.rateBps - b.rateBps)
    .filter(rv => rv.units <= left && (left -= rv.units, true))
    .map(({ bidId, units, rateBps, salt }) => ({ bidId, units, rateBps, salt }));
}

// ---- Detail -----------------------------------------------------------------

async function showDetail(id, force) {
  let r, holdings, bids, trail;
  try {
    [r, holdings, bids, trail] = await Promise.all([query("InvoiceRegistry:GetReceivable", id), query("ReceivableToken:GetHoldings", id),
      query("Marketplace:ListBids", id), query("InvoiceRegistry:GetReceivableHistory", id).catch(() => [])]);
  } catch (e) { if (S) toast(e.message); return; }
  if (selected !== id && !force) return;
  selected = id;
  known.add(id);
  const revs = await liveReveals(id, bids || []);
  const sig = JSON.stringify([r, holdings, bids, revs, trail]);
  document.querySelectorAll(".row.sel").forEach(x => x.classList.remove("sel"));
  document.querySelectorAll(`.row[data-id="${CSS.escape(id)}"]`).forEach(x => x.classList.add("sel"));
  if (!force && sig === shownSig) return; // nothing changed; keep anything typed into a form
  shownSig = sig;
  renderDetail(r, holdings || [], bids || [], revs, trail || []);
}

// What one ledger version changed, read against the version before it.
function change(v, prev) {
  const r = v.receivable, p = prev && prev.receivable;
  if (!r) return "Removed";
  if (!p) return r.eInvoice ? "Registered, IRP signature checked" : "Registered";
  if (r.status !== p.status) return { ACCEPTED: "Accepted by the buyer", LISTED: "Listed for sealed bids", FUNDING_PENDING: `Awarded to ${(r.awards || []).map(a => org(a.financierMsp)).join(" and ")}`,
    FUNDED: "Fully funded", REPAID: "Repaid by the buyer", CLOSED: "Every holder paid; closed", REJECTED: "Rejected by the buyer", CANCELLED: "Cancelled by the supplier",
    OVERDUE: "Marked overdue", DEFAULTED: "Marked defaulted" }[r.status] || STATUS[r.status] || r.status;
  if (JSON.stringify(r.risk) !== JSON.stringify(p.risk)) return `Risk scored ${r.risk.score}${byAI(r) ? " by the AI" : " by hand"}`;
  if (r.mandateRef !== p.mandateRef) return "e-NACH mandate set up";
  if (r.fundedPaise !== p.fundedPaise) return `Funding paid, ${rupees(r.fundedPaise - p.fundedPaise)}`;
  const paid = x => (x.payouts || []).filter(q => q.confirmed).length;
  if (paid(r) !== paid(p)) return "Payout made";
  if ((r.awards || []).length !== (p.awards || []).length) return "Unpaid awards lapsed";
  return "Updated";
}

const holder = (hid, r) => hid.startsWith("FIN:") ? org(hid.slice(4)) : hid === r.supplierId ? "Supplier" : hid === r.buyerId ? "Buyer" : short(hid);

function renderDetail(r, holdings, bids, revs, trail = []) {
  const d = $("detail");
  if (!r) { d.innerHTML = `<h2>Invoice</h2><p class="muted">Select an invoice to see where it stands and what you can do next.</p>`; return; }
  const at = PATH.indexOf(["OVERDUE", "DEFAULTED"].includes(r.status) ? "FUNDED" : r.status);
  const off = { OVERDUE: `Overdue${r.daysLate ? `, ${r.daysLate} days late` : ""}`, DEFAULTED: `Defaulted${r.daysLate ? `, ${r.daysLate} days late` : ""}`,
    REJECTED: `Rejected by the buyer${r.rejectReason ? ` (${r.rejectReason})` : ""}`, CANCELLED: "Cancelled by the supplier" }[r.status];
  const facts = [
    ["Issued", day(r.invoiceDate, true)],
    ["Due", `${day(r.dueDate, true)}${OPEN.includes(r.status) || r.status === "FUNDED" ? `, in ${daysTo(r.dueDate)} days` : ""}`],
    ["Source", r.eInvoice ? `IRP-signed e-invoice, via ${org(r.originMsp)}` : `Manual invoice co-signed by the buyer, via ${org(r.originMsp)}`],
    ["Risk score", r.risk ? `${r.risk.score} of 100 · ${reasons(r)}` : "Not scored yet"],
    ...(r.risk ? [["Scored by", byAI(r) ? `AI model ${r.risk.modelVersion}` : "The risk desk, by hand"]] : []),
    ["e-NACH mandate", r.mandateRef || "Not set up"],
  ];
  if (r.fundedPaise) facts.push(["Funded", `${rupees(r.fundedPaise)} paid to the supplier`]);
  if (r.repaidPaise) facts.push(["Repaid", `${rupees(r.repaidPaise)} of ${rupees(r.amountPaise)}`]);
  if (r.daysLate && !off) facts.push(["Paid late", `${r.daysLate} days${r.breach45Days ? ", past the MSMED Act's 45-day limit" : ""}`]);

  const colours = ["var(--accent)", "var(--accent-2)", "var(--accent-3)"];
  const held = holdings.filter(h => h.units > 0)
    .map(h => ({ ...h, name: holder(h.holderId, r) })).sort((a, b) => (a.name === "Supplier") - (b.name === "Supplier") || b.units - a.units)
    .map((h, i) => ({ ...h, colour: h.name === "Supplier" ? "var(--hold)" : colours[i % colours.length] }));

  const mineToSee = rv => rv && (rv.bid.bidderMsp === ME.msp || own(r));
  const bidRows = bids.map(b => {
    const rv = revs.find(x => x.bidId === b.bidId), see = mineToSee(rv);
    const aw = (r.awards || []).find(a => a.bidId === b.bidId);
    const units = see ? rv.units : aw ? aw.units : null;
    return `<div><span>${esc(org(b.bidderMsp))}${b.bidderMsp === ME.msp ? " (you)" : ""}</span>
      <span class="num">${units != null ? count(units) + " units" : "sealed"}</span><span class="num">${see ? pct(rv.rateBps) : "sealed"}</span>
      <span>${esc(BID[b.status] || b.status)}</span></div>`;
  }).join("");
  const bidNote = own(r) ? "You see each rate its bank revealed to you, checked against the hash on the ledger."
    : ME.role === "financier" ? "Other banks' rates and sizes stay sealed." : "Rates stay sealed; only hashes are on the ledger.";

  d.innerHTML = `<h2>Invoice ${short(r.id)}</h2>
    <p class="amount">${rupees(r.amountPaise)}</p>
    <ol class="steps">${PATH.map((s, i) => `<li class="${at < 0 || i > at ? "" : i < at ? "done" : "now"}">${STATUS[s]}</li>`).join("")}</ol>
    ${off ? `<p class="off">${esc(off)}</p>` : ""}
    <dl>${facts.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join("")}</dl>
    ${held.length ? `<h3>Who holds it</h3>
      <div class="bar">${held.map(h => `<span style="width:${h.units / 100}%;background:${h.colour}"></span>`).join("")}</div>
      <div class="legend">${held.map(h => `<div><span class="sw" style="background:${h.colour}"></span><span>${esc(h.name)}</span>
        <span class="num">${count(h.units)} units</span><span class="num muted">${(h.units / 100).toFixed(h.units % 100 ? 1 : 0)}%</span></div>`).join("")}</div>` : ""}
    ${bids.length ? `<h3>Sealed bids</h3><div class="bids"><div class="head"><span>Bidder</span><span class="num">Size</span><span class="num">Rate</span><span>Outcome</span></div>${bidRows}</div>
      <p class="muted small">${esc(bidNote)}</p>` : ""}
    ${trail.length ? `<h3>Ledger trail</h3><ol class="trail">${trail.map((v, i) => `<li><time>${esc(v.timestamp ? new Date(v.timestamp).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" }) : "")}</time>
      <span>${esc(change(v, trail[i - 1]))}</span><code title="${esc(v.txId)}">${esc(String(v.txId).length > 12 ? String(v.txId).slice(0, 8) + "…" : v.txId)}</code></li>`).join("")}</ol>` : ""}`;

  const m = moves(r, revs);
  const box = el("div", "move", `<h3>${m.mine ? "Your move" : "Next step"}</h3><p>${esc(m.say)}</p>`);
  if (m.items.length) { const a = el("div", "actions"); m.items.forEach(x => a.appendChild(x)); box.appendChild(a); }
  d.appendChild(box);
}

function waiting(r) {
  switch (r.status) {
    case "REGISTERED": return r.risk ? "Waiting for the buyer to confirm the invoice." : "Waiting for the buyer to confirm it and for its risk score.";
    case "ACCEPTED": return r.risk ? "Waiting for the supplier to list it for bids." : "Waiting for its risk score.";
    case "LISTED": return "Open for sealed bids. The supplier awards the best ones.";
    case "FUNDING_PENDING": return "Waiting for the winning financiers to pay the supplier.";
    case "FUNDED": return r.mandateRef ? "Funded. The buyer's e-NACH mandate repays it on the due date." : "Funded. Waiting for the buyer to set up an e-NACH mandate.";
    case "OVERDUE": case "DEFAULTED": return "Waiting for the buyer to repay.";
    case "REPAID": return "Repaid. Waiting for the operator to pay out each holder.";
    case "CLOSED": return "Settled and closed. Every holder has been paid.";
    case "REJECTED": return "The buyer rejected this invoice, so it cannot be financed.";
    case "CANCELLED": return "The supplier withdrew this invoice. It cannot be registered again anywhere.";
    default: return "";
  }
}

// What the signed-in party can do with this receivable. The chaincode checks
// every call again, so a button here is a convenience, not a permission.
function moves(r, revs) {
  const id = r.id, items = [];
  let say = "";
  if (ME.role === "operator") {
    const left = r.amountPaise - r.repaidPaise;
    if (["FUNDED", "OVERDUE", "DEFAULTED"].includes(r.status) && r.mandateRef) {
      say = `Collect the ${rupees(left)} due through the buyer's e-NACH mandate. It is split across the holders by units.`;
      items.push(act(`Collect ${rupees(left)} via e-NACH`, () => tx("Settlement:ConfirmRepayment", [id, "NACH" + digits(12), left], null, { done: "Repayment collected. Payouts are ready." })));
    }
    const unpaid = (r.payouts || []).filter(p => !p.confirmed);
    if (unpaid.length) {
      say = "Pay each holder its share of the repayment.";
      for (const p of unpaid) items.push(act(`Pay ${rupees(p.amountPaise)} to ${holder(p.holderId, r)}`, () => tx("Settlement:ConfirmPayout", [id, p.payoutId, digits(12)], null, { done: "Payout made" })));
    }
    if (r.status === "FUNDED" && daysTo(r.dueDate) < 0) items.push(act("Mark overdue", () => tx("Settlement:MarkOverdue", [id], null, { done: "Marked overdue" }), true));
    if (r.status === "OVERDUE" && daysTo(r.dueDate) < -90) items.push(act("Mark defaulted", () => tx("Settlement:MarkDefaulted", [id], null, { done: "Marked defaulted" }), true, "Click again to mark it defaulted"));
  }
  if (ME.role === "risk_oracle" && OPEN.includes(r.status)) {
    say = !r.risk ? "Not scored yet. When the AI risk service is running it scores new invoices itself; you can also score this one by hand."
      : byAI(r) ? `The AI scored it ${r.risk.score}. Override it only if you have evidence the model missed. A score over the limit blocks listing and award.`
      : "You set this score by hand. Change it if new evidence comes in. A score over the limit blocks listing and award.";
    items.push(scoreForm(r));
  }
  if (ME.role === "supplier" && own(r)) {
    if (r.status === "ACCEPTED" && r.risk) {
      say = "The buyer has confirmed it and it has a risk score. List it so banks and NBFCs can bid.";
      items.push(act("List for bids", () => tx("Marketplace:ListReceivable", [id], null, { done: "Listed. Financiers can bid now." })));
    }
    if (r.status === "LISTED") {
      const best = bestAwards(revs), units = best.reduce((n, a) => n + a.units, 0);
      say = best.length ? `${best.length === 1 ? "One bid fits" : best.length + " bids fit"} in the 10,000 units on offer, cheapest first.`
        : "No bids revealed to you yet. Each bank sends its rate to you alone.";
      if (best.length) items.push(act(`Award ${best.length} bid${best.length > 1 ? "s" : ""}: ${count(units)} units`, () => tx("Marketplace:AwardBids", [id], { awards: best }, { done: "Bids awarded. The winners can pay now." })));
    }
    if (OPEN.includes(r.status)) items.push(act("Cancel invoice", () => tx("InvoiceRegistry:CancelInvoice", [id], null, { done: "Invoice cancelled" }), true, "Click again to cancel it for good"));
  }
  if (ME.role === "buyer" && owes(r)) {
    if (r.status === "REGISTERED") {
      say = "Confirm that you owe this invoice. Once you do, your supplier can finance it.";
      items.push(act("Accept", () => tx("InvoiceRegistry:AcceptInvoice", [id], null, { done: "Invoice accepted" })),
        act("Reject", () => tx("InvoiceRegistry:RejectInvoice", [id, "NOT_RECOGNISED"], null, { done: "Invoice rejected" }), true, "Click again to reject it"));
    } else if (needsMandate(r)) {
      say = "Set up the e-NACH mandate that repays this invoice on its due date.";
      items.push(act("Set up e-NACH mandate", () => tx("Settlement:RegisterMandate", [id, "UMRN" + digits(12), "E_NACH"], null, { done: "e-NACH mandate registered" })));
    }
  }
  if (ME.role === "financier") {
    if (r.status === "LISTED") {
      say = "Bid privately. Only a hash goes on the ledger, and your rate is revealed to the supplier alone.";
      items.push(bidForm(r));
    }
    for (const w of (r.awards || []).filter(w => !w.confirmed && w.financierMsp === ME.msp)) {
      say = "You won. Pay the supplier and the units move to you.";
      items.push(act(`Pay ${rupees(w.fundingPaise)} by RTGS`, () => tx("Settlement:ConfirmFunding", [id, w.bidId, "MOCKR" + digits(17), "RTGS", w.fundingPaise], null, { done: "Payment confirmed. The units are yours." })));
    }
  }
  return { say: say || waiting(r), items, mine: !!say };
}

// A button that runs one ledger action. With confirm, the first click asks
// and only a second click within four seconds acts.
function act(label, fn, ghost, confirm) {
  const b = el("button", ghost ? "ghost" : "");
  b.type = "button";
  b.textContent = label;
  let armed = 0;
  b.onclick = async () => {
    if (confirm && Date.now() - armed > 4000) {
      armed = Date.now();
      b.textContent = confirm; b.classList.add("confirm");
      setTimeout(() => { if (b.isConnected && Date.now() - armed >= 4000) { b.textContent = label; b.classList.remove("confirm"); } }, 4100);
      return;
    }
    armed = 0; b.textContent = label; b.classList.remove("confirm");
    b.disabled = true;
    try { await fn(); } catch { /* already shown */ }
    b.disabled = false;
    if (S) { await load(); if (selected) await showDetail(selected, true); }
  };
  return b;
}

function scoreForm(r) {
  const f = el("div", "form", `<label>Score, 0 to 100<input type="number" min="0" max="100" value="${r.risk ? esc(r.risk.score) : 15}"></label>
    <label>Reason codes<input value="${esc(r.risk ? (r.risk.reasonCodes || []).join(", ") : "OK")}" style="width:180px"></label>`);
  const [score, codes] = f.querySelectorAll("input");
  const list = () => codes.value.split(",").map(x => x.trim()).filter(Boolean);
  const save = act(!r.risk ? "Record score" : byAI(r) ? "Override score" : "Update score", async () => {
    const report = JSON.stringify({ id: r.id, score: +score.value, reasons: list(), by: ME.username, at: new Date().toISOString() });
    await tx("InvoiceRegistry:RecordRiskScore", [r.id, +score.value, MANUAL, list(), await sha256(report)], null, { done: "Score recorded on the ledger" });
  });
  // An existing score can only be replaced by a different one, so clicking
  // through never turns the AI's score into a hand-set copy of itself.
  if (r.risk) {
    const same = () => +score.value === r.risk.score && list().join(",") === (r.risk.reasonCodes || []).join(",");
    save.disabled = true;
    score.oninput = codes.oninput = () => { save.disabled = same(); };
  }
  f.appendChild(save);
  return f;
}

function bidForm(r) {
  const f = el("div", "", `<div class="form"><label>Units, of 10,000<input type="number" min="1" max="10000" value="10000"></label>
    <label>Yearly rate, %<input type="number" min="0.05" max="50" step="0.05" value="11.00"></label></div><p class="muted small"></p>`);
  const [units, rate] = f.querySelectorAll("input"), est = f.querySelector("p");
  const days = Math.max(0, daysTo(r.dueDate));
  const update = () => {
    const share = Math.floor(r.amountPaise * +units.value / 10000), bps = Math.round(+rate.value * 100);
    est.textContent = `If awarded, you pay about ${rupees(Math.floor(share - share * bps * days / 3650000))} now and collect ${rupees(share)} in ${days} days.`;
  };
  units.oninput = rate.oninput = update;
  update();
  f.querySelector(".form").appendChild(act("Place sealed bid", async () => {
    let out;
    try {
      out = await api(`/bids/${encodeURIComponent(r.id)}`, { method: "POST", body: JSON.stringify({ units: +units.value, rateBps: Math.round(+rate.value * 100) }) });
    } catch (e) { if (S) toast(e.message); return; }
    const all = reveals();
    (all[r.id] ||= []).push(out.reveal);
    keep(localStorage, "rasid-reveals", all);
    toast("Sealed bid placed. Only its hash is on the ledger.", true, out.commitment && out.commitment.hash ? `Commitment ${String(out.commitment.hash).slice(0, 16)}… · tx ${String(out.reveal.bidId).slice(0, 16)}` : "");
  }));
  return f;
}

// ---- Live events --------------------------------------------------------------

// Browsers allow about six connections to one server, and an event stream
// holds one open for good, so a demo with a tab per role would run out. One
// tab per server holds the stream (a Web Lock picks it) and passes each event
// to the other tabs; when it closes or signs out, another tab takes over.
let channel = null, stopStream = () => {};
function listen() {
  stopStream();
  const name = "rasid-events " + (S.base || location.origin);
  channel = "BroadcastChannel" in window ? new BroadcastChannel(name) : null;
  if (channel) channel.onmessage = e => onEvent({ data: e.data });
  const open = () => {
    es = new EventSource(`${S.base}/events?token=${encodeURIComponent(S.token)}`);
    es.onopen = () => { setLive("live"); if (channel) channel.postMessage(JSON.stringify({ name: "__live" })); };
    es.onmessage = m => { onEvent(m); if (channel) channel.postMessage(m.data); };
    es.onerror = () => {
      setLive("off");
      if (es && es.readyState === EventSource.CLOSED) api("/auth/me").then(() => setTimeout(() => { if (S && es) { es.close(); open(); } }, 3000)).catch(() => {});
    };
  };
  if (!navigator.locks || !channel) { open(); stopStream = () => { if (es) es.close(); es = null; }; return; }
  // Another tab holds the stream; events arriving through it mean it is live.
  setTimeout(() => { if (LIVE === "connecting" && S) api("/auth/me").then(() => setLive("live")).catch(() => {}); }, 1500);
  const quit = new AbortController();
  let release = () => {};
  navigator.locks.request(name, { signal: quit.signal }, () => new Promise(done => { release = done; open(); })).catch(() => { /* gave up waiting */ });
  stopStream = () => { quit.abort(); release(); if (es) es.close(); es = null; if (channel) channel.close(); channel = null; stopStream = () => {}; };
}

function onEvent(m) {
  let e;
  try { e = JSON.parse(m.data); } catch { return; }
  setLive("live");
  if (e.name === "__live") return;
  if (e.block) setHeight(e.block);
  const id = e.payload?.id;
  if (id) known.add(id);
  FEED.unshift({ name: EVENT[e.name] || e.name, id, by: e.payload?.bidderMsp, at: new Date() });
  FEED.length = Math.min(FEED.length, 40);
  renderFeed();
  soon();
}

function renderFeed() {
  const f = $("feed");
  if (!FEED.length) { f.innerHTML = `<p class="empty">Ledger events appear here as they happen.</p>`; return; }
  f.innerHTML = FEED.map(x => `<div data-id="${esc(x.id || "")}"><span>${esc(x.name)}${x.by ? " by " + esc(org(x.by)) : ""}
    ${x.id ? `<small>${short(x.id)}</small>` : ""}</span><small>${x.at.toLocaleTimeString("en-GB")}</small></div>`).join("");
}
$("feed").addEventListener("click", e => { const row = e.target.closest("[data-id]"); if (row && row.dataset.id) pick(row.dataset.id); });

// Another tab placed a bid: its reveal just arrived in localStorage.
addEventListener("storage", e => { if (e.key === "rasid-reveals" && S && selected) showDetail(selected); });
document.addEventListener("visibilitychange", () => { if (!document.hidden && S) refresh(); });

boot();
