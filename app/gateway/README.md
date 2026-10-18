# RASID gateway

REST front door to the RASID chaincode, built on the Fabric Gateway SDK that
DRUNIX peers expose. Each organisation runs its own gateway with its own
enrollment certificate, so every transaction is signed by the right role.

| Method | Path | Does |
|---|---|---|
| POST | `/tx/{contract}/{function}` | Submit. Body `{"args":[...], "transient":{...}, "endorsingOrgs":[...]}`. Returns `txId` and the result |
| GET | `/query/{contract}/{function}?arg=..` | Evaluate (read only) |
| POST | `/bids/{receivableId}` | Sealed bid `{"units","rateBps"}`; returns the reveal (with salt and bidId) to keep |
| GET | `/events` | Chaincode events as Server-Sent Events, for the web demo |
| POST | `/auth/login`, `/auth/logout` | Sign in and out, when `LOGIN_USERS` is set |
| GET | `/auth/me`, `/auth/accounts` | The signed-in user; who can sign in here |
| GET | `/health` | |
| GET | `/` | The web console, when `WEB_DIR` is set |

A call that carries transient data must name its endorsing orgs: the Fabric
gateway will not send private data to an org outside the collection. `/bids`
does this for you (the bidder's org and `OPERATOR_MSP`).

| Variable | Default | Meaning |
|---|---|---|
| `PEER_ENDPOINT`, `PEER_HOST_ALIAS`, `PEER_TLS_CA` | `localhost:7051`, `peer0.npci.rasid` | The org's own peer |
| `MSP_ID`, `CERT_PATH`, `KEY_PATH` | `NPCIMSP` | The identity this gateway signs as |
| `CHANNEL`, `CHAINCODE` | `rasid` | |
| `LISTEN` | `:8080` | |
| `OPERATOR_MSP` | `NPCIMSP` | Second endorser for sealed bids |
| `RISK_URL` | off | Turns on the risk bridge |
| `CORS_ORIGIN` | off | Allow a console served from another origin |
| `WEB_DIR` | off | Serve the console from this folder |
| `LOGIN_USERS` | off | `name:password[,name:password...]`; turns on sign-in |
| `LOGIN_LABEL` | the MSP | How the console names this gateway's users, e.g. `Citi` |

With `LOGIN_USERS` set, `/tx`, `/query`, `/bids` and `/events` need a
session: `POST /auth/login` returns a token to send as
`Authorization: Bearer <token>` (or `?token=` on `/events`). Everyone who signs
in at a gateway acts as that gateway's certificate, whose role and GSTIN the
gateway reads from the certificate's attributes. In production each bank's
own single sign-on would take this place.

Set `RISK_URL` on the risk-oracle org's gateway to turn on the risk bridge: it
scores each `ReceivableRegistered` receivable with the risk service and submits
`InvoiceRegistry:RecordRiskScore`, and feeds completed repayments back.

```bash
cd app/gateway
PEER_ENDPOINT=localhost:7051 PEER_HOST_ALIAS=peer0.npci.rasid PEER_TLS_CA=... \
MSP_ID=NPCIMSP CERT_PATH=... KEY_PATH=... CHANNEL=rasid CHAINCODE=rasid \
go run .
```

Example:

```bash
curl -X POST localhost:8080/tx/InvoiceRegistry/RegisterEInvoice \
  -d "{\"args\":[\"$(cat qr.jwt)\",\"2027-01-09\"]}"
curl "localhost:8080/query/InvoiceRegistry/GetReceivable?arg=<id>"
```

Ran against a local 3-org Fabric 2.5 network (`network/local/`): five
gateways, one per role, drove the full flow to CLOSED and served the console.
It has not yet run against DRUNIX itself.
