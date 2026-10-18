# RASID devnet

Runs the real RASID chaincode (through `fabric-contract-api-go`'s own
dispatch) on an in-memory ledger, behind the same REST API as `app/gateway`.
Use it to build and demo everything off-chain before the DRUNIX network is up.

```bash
cd app/devnet
go run .                 # http://localhost:8080 serves the web console from ../web
```

## Sign-in

Every request needs a session. `POST /auth/login` with
`{"username","password"}` returns a token; send it as
`Authorization: Bearer <token>` (or `?token=` on `/events`, which browsers
cannot give headers). Each account acts as exactly one ledger identity. The
devnet turns that identity into an X.509 certificate carrying the same
attributes Fabric CA would issue, so the chaincode's role checks run
unchanged.

| Account | Acts as |
|---|---|
| `npci` | NPCIMSP, operator |
| `risk` | NPCIMSP, risk_oracle |
| `msme` | SupplierBankMSP, supplier, GSTIN 27AAACM1234A1Z5 |
| `buyer` | BuyerBankMSP, buyer, GSTIN 29AABCB5678B1Z2 |
| `citi` | CitiMSP, financier |
| `nbfc` | NbfcMSP, financier |

All use the password `rasid`. To use your own accounts instead, pass
`-users accounts.json`:

```json
[{"username":"axis","password":"choose-one","label":"Axis Bank",
  "identity":{"msp":"AxisMSP","role":"financier"}}]
```

| Method | Path | Does |
|---|---|---|
| POST | `/auth/login` | Returns `{"token","user"}` |
| GET | `/auth/me` | The signed-in account |
| POST | `/auth/logout` | Ends the session |
| GET | `/auth/accounts` | Who can sign in (never passwords; the demo password only for the built-in accounts) |

Sessions live in memory for 12 hours, so a restart signs everyone out, like
the ledger.

Scripts and tests can skip sign-in by naming the identity in
`X-Rasid-Identity`, for example `{"msp":"CitiMSP","role":"financier"}`, but
only when the devnet runs with `-trust-headers`; `scripts/demo-devnet.sh` does.
`X-Rasid-Time` (RFC 3339) sets the transaction time, to demo overdue and
default.

## AI risk scoring

With `-risk-url http://127.0.0.1:8090` the devnet runs the same risk bridge
as the risk-oracle org's gateway: each newly registered invoice is scored by
the AI risk service (`ai/risk-service`) and the score is anchored on the
ledger as NPCIMSP's risk oracle, and completed repayments are fed back.
`scripts/console-devnet.sh` starts both. Without it, the risk desk scores by
hand.

## What it is not

No endorsement, ordering, MVCC, private-data dissemination or history. A
failed transaction changes nothing, and, as on a Fabric peer, a transaction
cannot read its own writes.

`go test ./...` drives a full lifecycle over HTTP, including integer and
JSON-array arguments and transient data, and checks that a login can act only
as its own identity.
