# Running RASID on DRUNIX: what we found

RASID runs on NPCI's DRUNIX test network
([npci/drunix](https://github.com/npci/drunix), commit `8090e0c`): two
organisations, each with a lite peer, a committing peer, a stateless
validation service (VSCC), a YugabyteDB state database and a KeyDB transient
store, plus a Raft orderer. `network/drunix/` brings it up and deploys RASID;
`scripts/demo-drunix.sh` runs the whole flow to CLOSED.

Getting there turned up six things in DRUNIX 1.0.0. Each entry says what
happens, why, what RASID does about it, and what we would change upstream.
Each one ends with how we know: **seen** means we hit it on the running
network, and **read** means we found it in the source.

## 1. A committing peer can stop committing for good

**What happens.** A committing peer stopped committing about ten blocks after
the chaincode was deployed. After that, `peer channel getinfo` and Gateway
`CommitStatus` calls hung, and lite peers kept calling the newly committed
chaincode "unknown".

**Why.** For each lite peer that connects, the committing peer registers a
config listener. Its block notifier sends every lifecycle or config event to
each listener over an *unbuffered* channel, without checking whether that
listener is still alive
(`internal/pkg/gateway/commit/confignotifier.go`, `notifyConfig`). When a lite
peer reconnects, for example after a restart, its old listener stays
registered. The goroutine serving it (`ConfigEvents` in
`internal/pkg/gateway/configevents.go`) only notices that the stream is gone
when it tries to send the next event, and then exits. So the first event in a
block is fine. The second event in the same block, such as the second org's
approval when both approvals land together, blocks the notifier forever.

From then on the ledger's commit-notification channel fills up. Its buffer
holds 10. Once it is full, `kvLedger.sendCommitNotification` blocks while
holding the ledger's commit lock, so the peer stops committing, and every call
that reads the ledger waits on that lock. The same stuck notifier is also why
the lite peers never heard about the new chaincode definition.

A goroutine dump of the stuck peer shows all three pieces:
- the block notifier in `statusListener.receive` from `notifyConfig`, for 16 minutes;
- the committer in `sendCommitNotification` for block 14;
- 60 `CommitStatus` calls waiting on the ledger's read lock.

`notifyConfig` also walks the listener map without `configLock`, while gRPC
goroutines add to it, which is a data race.

**What RASID does.** `network/drunix/env.sh` has a `restart_peers` helper that
restarts the lite peers and then the committing peers, which clears the
stale listeners. `deploy.sh` approves for one organisation at a time, so each
lifecycle transaction gets its own block. With both changes, a deploy from
scratch needs no restarts, and the lite peers pick up the definition by
themselves.

**Upstream fix.** [`config-notifier-deadlock.patch`](config-notifier-deadlock.patch)
makes four changes:
- `notifyConfig` gives up on a listener that is done, and drops it;
- `notifyConfig` holds `configLock`;
- `ConfigEvents` returns as soon as its stream's context ends;
- two regression tests: one for the deadlock, one for the race.

We could not build or test DRUNIX in our environment. Run
`go test -race ./internal/pkg/gateway/commit/` before sending the patch.
(Seen, then traced in the source.)

## 2. YugabyteDB state needs JSON values

**What happens.** The committing peers failed with `invalid input syntax for
type json` and stopped. The same thing happened with chaincode that stored a
plain string, and with the lifecycle's `--init-required` marker.

**Why.** Each chaincode's state table keeps values in a `jsonb` column
(`db_value`). Fabric lets chaincode store any bytes.

**What RASID does.** Every value it stores is JSON; plain strings are stored as
JSON strings (`putString`/`getString` in `chaincode/rasid/contracts/ledger.go`).
`InitLedger` is an ordinary transaction instead of an init function, and it
refuses to run twice. A devnet test checks that every value the full flow
writes is JSON.

**Upstream fix.** Keep values that are not JSON in a `bytea` column, or wrap
them, so any Fabric chaincode runs unchanged. (Seen.)

## 3. Lite peers cannot stream events

**What happens.** Chaincode and block event streams, and the peer CLI's
`--waitForEvent`, do not work against a lite peer.

**Why.** A lite peer keeps no ledger. The Gateway forwards `CommitStatus` to
the committing peer, but not `ChaincodeEvents` or deliver.

**What RASID does.** The REST gateway takes `EVENTS_PEER_ENDPOINT` and reads
events and commit status from the organisation's committing peer. The scripts
use `--waitForEvent=false` and poll.

**Upstream fix.** Forward `ChaincodeEvents` (and block events) to the
committing peer, as `CommitStatus` already is. (Seen.)

## 4. The peer image has no chaincode-as-a-service builder

**What happens.** The install fails with `Unknown chaincodeType: CCAAS`.

**Why.** The peer's `core.yaml` names `/opt/hyperledger/ccaas_builder`, but
the image doesn't ship it.

**What RASID does.** `up.sh` copies the standard three-script builder into
each lite peer and restarts the peers.

**Upstream fix.** Ship the builder in `npcioss/drunix-peer`, as Fabric's own
peer image does. (Seen.)

## 5. Range queries ignore their end key

**What happens.** `GetStateByRange(start, end)` returns every key that
*contains* `start`, in no fixed order.

**Why.** The SQL scanner matches keys with `ILIKE '%start%'`, never uses
`endKey`, and pages with `OFFSET` without an `ORDER BY`. See
`core/ledger/kvledger/txmgmt/statedb/statesqldb`: `GetStateRangeScanIteratorWithPagination`,
plus `Next` and `NextKey` in `sql_client.go`. Chaincode that relies on
Fabric's sorted, bounded ranges can return wrong results.

**What RASID does.** RASID's queries use composite-key prefixes, which a
substring match still finds, and sort their own results. Its flows are not
affected.

**Upstream fix.** Use `key >= start AND key < end ORDER BY key`, with keyset
pagination. (Read.)

## 6. Chaincode installs only on lite peers

Committing peers refuse `peer lifecycle chaincode install`. This is by design,
since only lite peers run chaincode. The scripts install on the lite peers
only. (Seen.)
