# RASID settlement adapters

Python 3.10+, standard library only. Each adapter listens to its own org's
gateway event stream, moves money on a (mock) NPCI/RBI rail, and posts the UTR
back to the chaincode. The ledger state only changes once a UTR is posted.

| Mode | Org | Listens for | Pays | Then calls |
|---|---|---|---|---|
| `financier --msp CitiMSP` | each bank / NBFC | FundingRequested | MSME, for its own awards | Settlement:ConfirmFunding |
| `operator` | NPCI | RepaymentReceived | every unit holder's share | Settlement:ConfirmPayout |
| `collect --id <id>` | NPCI | (run on the due date) | debits the buyer's e-NACH mandate | Settlement:ConfirmRepayment |

Rail choice: UPI up to ₹1 lakh, IMPS up to ₹5 lakh, RTGS above that.
`rails.py` returns references in each rail's format (12-digit RRN for
UPI/IMPS, 16-character NEFT UTR, 22-character RTGS UTR). Replace `MockRails`
with NPCI sandbox clients when access is granted.

```bash
cd adapters/payments
python -m unittest discover -s tests -t .
python adapter.py financier --msp CitiMSP --gateway http://localhost:8081
```

Where the gateway has sign-in turned on, add `--login NAME:PASSWORD`. On a
devnet started with `-trust-headers`, `--identity` names the identity instead,
as `scripts/demo-devnet.sh` does.
