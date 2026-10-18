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

## 2. Unit tests

The first run downloads Go modules, so give it a minute.

```bash
(cd chaincode/rasid && go test ./...)
(cd app/devnet && go test ./...)
(cd app/gateway && go test ./...)
(cd ai/risk-service && python3 -m unittest discover -s tests -t .)
(cd adapters/payments && python3 -m unittest discover -s tests -t .)
(cd analytics && python3 -m unittest discover -s tests -t .)
```

Every line should end in `ok` (Go) or `OK` (Python).

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

Terminal 2, make an IRP key and a signed invoice:

```bash
cd tools/mock-irp
go run . keygen -out /tmp/irp
pbcopy < /tmp/irp/irp-public.pem
```

That puts the public key on the clipboard, BEGIN and END lines included, for
step 1 below. Before step 2, put the signed invoice on the clipboard:

```bash
go run . sign -key /tmp/irp/irp-private.pem -invoice sample-invoice.json | pbcopy
```

On Linux, use `cat` instead of `pbcopy` and copy the output.

Open http://localhost:8080. Everyone signs in, and every demo account uses
the password `rasid`; clicking an account on the sign-in page signs you
straight in. Give each party its own tab: press Cmd-T and open the same
address (don't duplicate a tab, which copies its sign-in). Each tab stays
signed in as its own party, and an invoice opens on the right when you click
it.

1. Tab 1, **npci** (NPCI operator): paste the public key under Network setup
   and click "Trust IRP key".
2. Tab 2, **msme** (MSME supplier): paste the signed invoice, check the due
   date, click "Register invoice".
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

To watch the AI catch a re-issued invoice, sign a copy of the sample with
another document number in terminal 2 (still in `tools/mock-irp`), then
register it in the msme tab:

```bash
sed 's/0042/0043/' sample-invoice.json > /tmp/irp/invoice-2.json
go run . sign -key /tmp/irp/irp-private.pem -invoice /tmp/irp/invoice-2.json | pbcopy
```

The AI scores it about 50, as a near-duplicate of the first. In the risk tab,
override it to 80 and the ledger will refuse to list it, because the limit for
e-invoices is 70.

The bids' rates and salts stay in your browser, standing in for each bank
sending its bid to the MSME privately. Award picks the cheapest bids that fit
in the 10,000 units on offer.

Sign out is at the top right. Ctrl-C in terminal 1 stops the devnet and the
AI service; the ledger and the sign-ins are in memory, so a restart is clean
and signs everyone out. `cd app/devnet && go run .` also works, without the
AI: the risk desk then scores each invoice by hand.

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

## If something fails

- `address already in use`: something else is on that port (8080 for devnet,
  8090 for the risk service, 7050–9051 for Fabric). Stop it, or run
  `bash network/local/down.sh` if an earlier network is still up.
- `go: go.mod requires go >= 1.24`: run `brew upgrade go`.
- The network scripts write logs to `/tmp/rasid-net/logs/`; the peer and
  chaincode logs there say what went wrong.
