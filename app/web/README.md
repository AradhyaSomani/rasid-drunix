# RASID web console

One page, no build step. Everyone signs in, the server decides which ledger
identity that login acts as, and the page shows that party its own screen:

| Party | Demo login | Their screen |
|---|---|---|
| NPCI operator | `npci` | Network setup (trust the IRP key), settlement queue, every invoice |
| AI risk desk | `risk` | Invoices awaiting a score, scored invoices, a score form |
| MSME supplier | `msme` | Register an e-invoice, its own invoices, list and award |
| Corporate buyer | `buyer` | Invoices raised on it: accept or reject, set up the e-NACH mandate |
| Citi, NBFC | `citi`, `nbfc` | Invoices open for bids, a sealed bid form, its portfolio and payments |

The demo password is `rasid`. The login is kept per browser tab, so open one
tab per party.

Selecting an invoice opens its detail: amount, where it is in the lifecycle,
who holds its units, the sealed bids, and what the signed-in party can do
next. A bid's rate shows only to the bank that placed it and to the supplier.

Two ways to run it:

```bash
bash scripts/console-devnet.sh            # devnet with AI scoring: open http://localhost:8080
WORK=/tmp/rasid-net bash scripts/console-network.sh   # live Fabric: open http://127.0.0.1:8101
```

On devnet the page signs in at the server it came from. On a live network it
reads `gateways.json` next to `index.html`, which lists every organisation's
gateway, and signs each user in at the gateway that holds their
organisation's certificate:

```json
{"gateways":["http://127.0.0.1:8101","http://127.0.0.1:8102","http://127.0.0.1:8103",
 "http://127.0.0.1:8104","http://127.0.0.1:8105"],"demoPassword":"rasid"}
```

`demoPassword` is optional; without it the sign-in page lists the accounts but
not the password.

Notes:

- The buttons are a convenience. The chaincode checks every call against the
  identity that signed it, so a hidden button is never the only guard.
- Bid reveals (rate and salt) stand in for each bank sending its bid to the
  supplier off the ledger. They sit in the browser's localStorage, shared by
  the demo's tabs, and each is checked against its hash on the ledger before
  it is shown or awarded.
- Browsers allow about six connections per server and an event stream holds
  one open, so one tab per server holds the stream and passes events to the
  others.
- The lists load the first 200 receivables and keep any seen through events.
- The gateways need `CORS_ORIGIN` set when the page is served from another origin.
