# Local smoke run, 18 Oct 2026

`benchmark-smoke.yaml` against `network/local` (Fabric 2.5 built from source):
one Raft orderer, one peer each for NPCI, BankA and Citi, LevelDB, RASID
chaincode as a service, endorsement policy NPCI AND (BankA OR Citi). Caliper
0.6.0 with the fabric-gateway binding, 2 workers. Everything, including
Caliper, ran in one 4-vCPU, 15 GB container.

| Round | Succ | Fail | Send rate (TPS) | Avg latency (s) | Max latency (s) | Throughput (TPS) |
|---|---|---|---|---|---|---|
| register-e-invoice (fixed 50 TPS) | 1,502 | 0 | 50.1 | 0.32 | 0.58 | 49.6 |
| month-end-spike (ramp 50 → 300 TPS) | 2,628 | 0 | 87.7 | 0.34 | 0.65 | 87.4 |
| check-duplicate (fixed 200 TPS, read-only) | 4,002 | 0 | 200.1 | 0.00 | 0.02 | 200.1 |

Every registration verified an RS256 IRP signature on chain and was endorsed
by two organisations.

The ramp averaged 88 TPS sent instead of the 175 the profile asks for, and
latency stayed flat, so this run did not reach the network's limit: the load
generator sharing 4 vCPUs with every node is the likely ceiling. These numbers
are a correctness and plumbing check, not a DRUNIX performance claim. The full
`benchmark.yaml` belongs on separate machines, with and without lite peers and
the stateless validator.

Full Caliper report: `local-smoke-2026-10-18.html`.
