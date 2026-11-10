# Testing RASID on your own machine

Run every command from the `rasid-build` folder (the repo root) unless a step
says otherwise. Written for a Mac; Linux is the same minus Homebrew.

## 1. Install the tools (once)

```bash
brew install go jq
go version
python3 --version
```

Go must be 1.24 or newer and Python 3.9 or newer; the Mac's built-in python3
is fine. The code blocks here have no inline comments, so each one pastes
straight into zsh.

## 2. Run every check

One command runs every check: formatting, `go vet`, syntax, a scan for
committed private keys, all unit and security tests, the end-to-end demo and
a browser run of the console. The first run downloads Go modules, so give it
a few minutes.

```bash
bash scripts/check.sh
```

It ends with a table and `All checks passed`. Run it after every change and
before every push; GitHub Actions runs the same script on each push.
`bash scripts/check.sh --quick` skips the demo and the browser (about a
minute).

The browser check needs Node and Playwright once; without them it shows SKIP:

```bash
brew install node
cd tests/ui
npm install
npx playwright install chromium
cd ../..
```

The browser check also saves a screenshot of every party's screen in
`tests/ui/screenshots/`.

## 3. Whole flow in the terminal (no blockchain network needed)

```bash
bash scripts/demo-devnet.sh
```

It registers a ₹10,00,000 e-invoice, blocks a duplicate, scores it, takes two
sealed bids, funds, collects and pays out. It should finish with
`status CLOSED` and two `paid` lines.

## 4. Click through it in the browser

Terminal 1, start the devnet with the AI risk service switched on:

```bash
bash scripts/console-devnet.sh
```

The console's demo helpers stand in for the GST portal, so you need no
second terminal. (To sign invoices yourself instead, see "Signing invoices
by hand" below.)

Open http://localhost:8080. Everyone signs in, and every demo account uses
the password `rasid`; clicking an account on the sign-in page signs you
straight in. Give each party its own tab: press Cmd-T and open the same
address (don't duplicate a tab, which copies its sign-in). Each tab stays
signed in as its own party, and an invoice opens on the right when you click
it.

1. Tab 1, **npci** (NPCI operator): under Network setup, click "Use the demo
   IRP's key". The green note shows the block that recorded it.
2. Tab 2, **msme** (MSME supplier): click "Create a sample e-invoice", check
   the due date, click "Register invoice".
3. Tab 3, **risk** (AI risk desk): nothing to do. The AI scored the invoice
   the moment it was registered, and it is under "Scored by the AI" with its
   reasons. The risk desk can override a score, but doesn't have to.
4. Tab 4, **buyer** (Corporate buyer): click the invoice under "Needs your
   action", Accept, then "Set up e-NACH mandate".
5. Back in the **msme** tab: "List for bids".
6. Tab 5, **citi**: click the invoice under "Open for bids" and bid 6000 units
   at 10.5%. Tab 6, **nbfc**: bid 4000 units at 11%.
7. **msme**: both rates now show on the invoice. Click "Award 2 bids: 10,000
   units".
8. **citi**, then **nbfc**: click the invoice under "Your portfolio" and Pay.
   Status: Funded.
9. **npci**: click the invoice under "Settlement queue", Collect via e-NACH,
   then pay each holder. Status: Closed.

Each party sees only its own work: the buyer never sees a bid rate, Citi
cannot see the NBFC's, and only the operator has setup and settlement
controls. The server enforces this as well: a request without a sign-in gets
401, and the chaincode refuses any call the signed-in identity's role does not
allow.

To watch the AI catch a re-issued invoice, click "Re-issue the last one
under a new number" in the msme tab and register it. The AI scores it 76, as
a near-duplicate of the first, and the ledger refuses to list it, because the
limit for e-invoices is 70. The first invoice scores 38 and lists normally.
The risk desk can still override a score in either direction.

Each invoice's detail ends with its ledger trail: every version the ledger
committed, with the transaction that wrote it. The bar at the top shows the
ledger, its block height and whether the live feed is connected; the four
numbers under it are that party's headline figures.

The bids' rates and salts stay in your browser, standing in for each bank
sending its bid to the MSME privately. Award picks the cheapest bids that fit
in the 10,000 units on offer.

Sign out is at the top right. Ctrl-C in terminal 1 stops the devnet and the
AI service; the ledger and the sign-ins are in memory, so a restart is clean
and signs everyone out. `cd app/devnet && go run .` also works, without the
AI: the risk desk then scores each invoice by hand.

### Signing invoices by hand

The same flow works with invoices signed in a terminal, as on a live network:

```bash
cd tools/mock-irp
go run . keygen -out /tmp/irp
pbcopy < /tmp/irp/irp-public.pem
```

Paste that under Network setup and click "Trust IRP key", then sign an
invoice and paste it into the msme tab:

```bash
go run . sign -key /tmp/irp/irp-private.pem -invoice sample-invoice.json | pbcopy
```

## 5. Optional: a real 3-org Fabric network (no Docker)

Download the Fabric 2.5 tools once:

```bash
mkdir -p ~/fabric && cd ~/fabric
curl -sSLO https://raw.githubusercontent.com/hyperledger/fabric/main/scripts/install-fabric.sh
bash install-fabric.sh binary
```

Then, in the terminal you will use, back in `rasid-build`:

```bash
export PATH=$HOME/fabric/bin:$PATH FABRIC_CONFIG_DIR=$HOME/fabric/config WORK=/tmp/rasid-net
ulimit -n 4096
bash network/local/up.sh
bash network/local/deploy.sh
bash scripts/demo-network.sh
```

`ulimit` lifts macOS's 256 open-file default. `up.sh` starts the orderer and
the NPCI, BankA and Citi peers; `deploy.sh` installs the chaincode, issues the
identities and runs InitLedger; the demo runs the full flow through five
gateways and ends CLOSED.

To click through the same network in the browser, run
`bash scripts/console-network.sh` and open http://127.0.0.1:8101 (Ctrl-C
stops it). Sign in as npci, risk, msme, buyer or citi, password `rasid`, one
tab each; each signs in at its own organisation's gateway. The script prints
where the IRP public key is and how to sign an invoice. A few steps happen by
themselves here: the risk service scores each new invoice, Citi's adapter pays
its awards, and NPCI's adapter pays out the holders. When you are done,
`bash network/local/down.sh` stops everything.

If macOS asks whether `peer` or `orderer` may accept incoming connections,
either answer works; everything talks over localhost.

## 6. Optional: on NPCI's DRUNIX (Docker)

This runs RASID on DRUNIX's own test network: per organisation a lite peer,
a committing peer, a validation service, YugabyteDB and KeyDB, plus an
orderer. It needs Docker Desktop running (give it at least 6 GB of memory in
Settings, Resources), Go, git and make. The first setup downloads DRUNIX,
builds its command-line tools and pulls about 2.5 GB of images, so allow 15
minutes; after that, a fresh network takes about two minutes.

```bash
bash network/drunix/setup.sh
bash network/drunix/up.sh
bash network/drunix/deploy.sh
bash scripts/demo-drunix.sh
bash network/drunix/inspect.sh
```

`setup.sh` puts DRUNIX in `~/drunix` (set `DRUNIX_HOME` to use another
folder). The demo ends CLOSED, like the local one. `inspect.sh` shows which
container does what, the ledger height on each committing peer, the latest
receivables read straight from each organisation's YugabyteDB with SQL, and
the private data in KeyDB.

For the browser, run `bash scripts/console-drunix.sh` and open
http://127.0.0.1:8101, signing in as npci, risk, msme, buyer or citi
(password `rasid`). The bar at the top says "DRUNIX network".

To measure it, run `bash benchmarks/caliper/run-drunix.sh` (needs Node.js 18
or later). It writes `benchmarks/caliper/report.html`. The benchmark adds a
few thousand invoices, so before a live demo start again with `down.sh`,
`up.sh` and `deploy.sh`.

`bash network/drunix/down.sh` stops the network and deletes its data. Logs are
in `/tmp/rasid-drunix/logs/`; `last-run/` there has the gateway, AI and
adapter logs from the latest demo or console run.

On a Mac with Apple silicon, Docker runs DRUNIX's images through emulation,
so everything is slower than on an Intel machine but works the same.
[docs/drunix/findings.md](docs/drunix/findings.md) explains the workarounds the
scripts make for DRUNIX 1.0.0.

## If something fails

- `address already in use`: something else is on that port (8080 for devnet,
  8090 for the risk service, 7050–9051 for Fabric). Stop it, or run
  `bash network/local/down.sh` if an earlier network is still up.
- `go: go.mod requires go >= 1.24`: run `brew upgrade go`.
- The network scripts write logs to `/tmp/rasid-net/logs/`; the peer and
  chaincode logs there say what went wrong.
