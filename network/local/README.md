# Local RASID network

A real Hyperledger Fabric 2.5 network on one machine, run as plain processes
(no Docker): one Raft orderer and one peer each for NPCI (operator and risk
oracle), BankA (MSME supplier and corporate buyer side) and Citi (financier),
on channel `rasid`. DRUNIX is Fabric-based, so this is the closest stand-in
until DRUNIX access arrives.

| File | Does |
|---|---|
| `crypto-config.yaml`, `configtx.yaml` | Orgs, anchor peers, Raft orderer, channel profile |
| `up.sh` | Generates crypto and the channel block, starts orderer and peers, joins the channel |
| `deploy.sh` | Builds the chaincode, deploys it as a service, issues demo identities with RASID attributes, runs `InitLedger` |
| `env.sh` | `as_admin`, `as_identity`, `all_peers` helpers for the peer CLI |
| `down.sh` | Stops everything |
| `builders/ccaas` | Chaincode-as-a-service external builder |

```bash
# Fabric binaries: from a Fabric 2.5 release, or built from source
GOBIN=$HOME/fabric-bin go install github.com/hyperledger/fabric/cmd/{peer,orderer,configtxgen,cryptogen,osnadmin}@release-2.5
export PATH=$HOME/fabric-bin:$PATH
export FABRIC_CONFIG_DIR=$(go env GOMODCACHE)/github.com/hyperledger/fabric@<version>/sampleconfig   # or the release's config/

export WORK=/tmp/rasid-net
bash network/local/up.sh
bash network/local/deploy.sh
bash scripts/demo-network.sh       # full flow through one gateway per identity
bash scripts/console-network.sh    # or click through it: web console on http://127.0.0.1:8101
bash network/local/down.sh
```

Endorsement policy: `AND('NPCIMSP.peer', OR('BankAMSP.peer','CitiMSP.peer'))`.
Identities come from `tools/issue-identity`, which signs certificates with each
org's cryptogen CA and adds the `rasid.role` / `rasid.gstin` attributes the way
Fabric CA would. Use Fabric CA for anything beyond a demo.

Tested on 18 Oct 2026: `up.sh`, `deploy.sh` and `scripts/demo-network.sh` ran
clean, and the Caliper smoke benchmark ran 8,132 transactions with no failures
(`benchmarks/caliper/results/`). A certificate from Citi's CA claiming the
operator role was refused on chain. The web console, driven through
`console-network.sh` in a headless browser, took an invoice to FUNDED with no
errors.
