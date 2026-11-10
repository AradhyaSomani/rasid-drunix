# DRUNIX smoke run, 10 Nov 2026

`benchmark-drunix.yaml` against NPCI's DRUNIX test network (`network/drunix`,
npci/drunix at `8090e0c`, images `npcioss/drunix-*:1.0.0`): one Raft orderer;
per organisation a lite peer, a committing peer, a stateless validation
service, YugabyteDB as the state database and KeyDB as the transient store.
RASID chaincode as a service, endorsement policy Org1 AND Org2. Caliper 0.6.0
with the fabric-gateway binding, 2 workers, sending to Org2's lite peer.
Everything, including Caliper and both YugabyteDB servers, ran in Docker in
one 4-vCPU, 15 GB container.

| Round | Succ | Fail | Send rate (TPS) | Avg latency (s) | Max latency (s) | Throughput (TPS) |
|---|---|---|---|---|---|---|
| register-e-invoice (fixed 50 TPS) | 1,502 | 0 | 50.1 | 2.17 | 3.48 | 48.5 |
| month-end-spike (ramp 50 → 300 TPS) | 2,230 | 0 | 74.4 | 5.33 | 10.68 | 56.4 |
| check-duplicate (fixed 200 TPS, read-only) | 4,002 | 0 | 200.1 | 0.01 | 0.10 | 200.0 |

All 7,734 transactions succeeded. Every registration verified an RS256 IRP
signature on chain, was endorsed by both organisations' lite peers and was
committed by both committing peers into YugabyteDB.

The committing peers set the pace. During the run they committed 69 blocks
of about 54 transactions each. Each block took about 0.87 s: 0.37 s for state
validation reads and 0.49 s to write the state to YugabyteDB, with every
container sharing the same 4 vCPUs. That works out to roughly 60 TPS, which
is where the ramp levelled off while latency rose. The local Fabric network
keeps state in LevelDB inside the peer and showed 0.32 s latency. These are
correctness and plumbing numbers, not a DRUNIX performance claim. On separate
machines, with YugabyteDB on its own nodes, the full `benchmark.yaml` is the
real test.

Full Caliper report: `drunix-smoke-2026-11-10.html`.
